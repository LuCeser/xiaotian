/**
 * @file Parent page — 扫描 PDF 导入纯函数 (Issue #7): the page-plan status
 *       machine, the per-page result merge and the commit slicing rules of
 *       the scanned-PDF import. Everything here is PURE (no DOM, no fetch,
 *       no pdf.js) and exposed on the window for the Node unit tests
 *       (tests/test-parent-photo-pdf.js) — the 前端纯函数 convention
 *       (window.parseBatch precedent).
 *
 * Contract (Issue #7 acceptance criteria, ADR-0004):
 *   - The PDF itself is split INTO PAGES IN THE BROWSER (parent-pdf.js
 *     runtime); each page rides the ordinary photo recognize call. These
 *     functions carry no I/O of any kind.
 *   - validatePdfFile gates BEFORE the file is ever loaded (AC-3: 大小／页
 *     数超限给出明确反馈，不开始处理): type application/pdf or a .pdf name
 *     (iPad Safari sometimes hands an empty MIME type), size ≤ 40MiB.
 *   - createPagePlan/applyPageEvent/retryPage/planSummary are the per-page
 *     status machine (AC-3 处理过程显示状态, AC-4 失败页可单独重试).
 *     applyPageEvent/retryPage return a NEW plan — the old one is never
 *     mutated, so re-render diffing stays honest.
 *   - mergePageRows keeps multi-page results IN PAGE ORDER regardless of
 *     completion order, and re-recognizing a page REPLACES that page's
 *     segment (AC-2 不乱序、不重复; edits to other pages' rows survive;
 *     the cross-page same-word dedup itself rides classifyPhotoEntries
 *     over the merged array — the single-photo duplicate semantics
 *     extended to the whole book).
 *   - rowsByPage/applyLessonToPage back the per-page lesson batch (AC-6 可
 *     批量调整课次); chunkBatch slices the commit into the server's batch
 *     caps (100 entries per add call, 500 ids per assign call) so a whole
 *     textbook commits without the parent splitting it by hand (AC-5).
 */

(function (global) {
  'use strict';

  /**
   * File/page caps (ADR-0004, user-confirmed 2026-10-14): a whole
   * elementary textbook fits in 100 pages, and 40MiB keeps the in-browser
   * ArrayBuffer loadable on older iPads. The server's per-image cap stays
   * the backstop for every page REQUEST — it never sees the PDF itself.
   */
  var MAX_PDF_BYTES = 40 * 1024 * 1024;
  var MAX_PDF_PAGES = 100;

  /**
   * validatePdfFile — the pre-load gate. Pure: takes the picker's file
   * shape ({name, type, size}), never the File itself.
   *   { ok: true }
   *   { ok: false, code: 'not-pdf' | 'pdf-too-large', message }
   */
  function validatePdfFile(file) {
    if (!file) {
      return { ok: false, code: 'not-pdf', message: '请选择一个 PDF 文件' };
    }
    var isPdf = file.type === 'application/pdf' ||
      /\.pdf$/i.test(String(file.name || ''));
    if (!isPdf) {
      return { ok: false, code: 'not-pdf', message: '只支持 PDF 扫描件，请换一个文件' };
    }
    if (file.size > MAX_PDF_BYTES) {
      var mb = Math.round(MAX_PDF_BYTES / (1024 * 1024));
      return {
        ok: false,
        code: 'pdf-too-large',
        message: 'PDF 超过 ' + mb + 'MB 上限，请分批上传（每份最多 ' + mb + 'MB）',
      };
    }
    return { ok: true };
  }

  /**
   * createPagePlan — one pending entry per page after pdf.js reports the
   * page count. Over MAX_PDF_PAGES refuses the whole file (nothing is
   * processed — the honest over-limit outcome).
   */
  function createPagePlan(pageCount) {
    var count = pageCount || 0;
    if (count > MAX_PDF_PAGES) {
      return {
        ok: false,
        code: 'pdf-too-many-pages',
        limit: MAX_PDF_PAGES,
        message: 'PDF 有 ' + count + ' 页，超过 ' + MAX_PDF_PAGES + ' 页上限，请分批上传',
      };
    }
    var pages = [];
    for (var no = 1; no <= count; no++) {
      pages.push({ no: no, status: 'pending', error: '' });
    }
    return { ok: true, pages: pages };
  }

  /**
   * applyPageEvent — one lifecycle step for ONE page (the runner is
   * strictly sequential, so 'start' lands on the current pending page).
   * Returns a NEW plan; an out-of-lifecycle event changes nothing (the
   * runner's own sequencing is the guard, not thrown errors mid-render).
   */
  function applyPageEvent(plan, no, event, error) {
    return mapPlan(plan, function (page) {
      if (page.no !== no) {
        return page;
      }
      if (event === 'start' && page.status === 'pending') {
        return { no: page.no, status: 'running', error: '' };
      }
      if (event === 'done' && page.status === 'running') {
        return { no: page.no, status: 'done', error: '' };
      }
      if (event === 'fail' && page.status === 'running') {
        return { no: page.no, status: 'failed', error: error || '识别失败' };
      }
      // Stopped or superseded mid-flight — return to pending so 继续/重试 work.
      if (event === 'abort' && page.status === 'running') {
        return { no: page.no, status: 'pending', error: '' };
      }
      return page;
    });
  }

  /** retryPage — a failed page goes back to pending (error cleared). */
  function retryPage(plan, no) {
    return mapPlan(plan, function (page) {
      if (page.no === no && page.status === 'failed') {
        return { no: page.no, status: 'pending', error: '' };
      }
      return page;
    });
  }

  /**
   * abortRunningPages (Issue #80 §3.2, U6) — the stop/invalidate sweep:
   * every page still marked running returns to pending (error cleared) so
   * 继续/重试 stay reachable once the run's wind-down settles; done and
   * failed pages are untouched — stop never undoes finished work. The
   * batch-level stop transition (parent-import-core) calls this; a NEW
   * plan comes back (the mapPlan convention).
   */
  function abortRunningPages(plan) {
    return mapPlan(plan, function (page) {
      if (page.status === 'running') {
        return { no: page.no, status: 'pending', error: '' };
      }
      return page;
    });
  }

  /** mapPlan — rebuild the plan with fn applied to each page entry. */
  function mapPlan(plan, fn) {
    if (!plan || !plan.pages) {
      return plan;
    }
    return { ok: plan.ok, code: plan.code, limit: plan.limit, pages: plan.pages.map(fn) };
  }

  /**
   * planSummary — the whole-file verdicts. processing while anything is
   * pending/running; allSettled once nothing is; allSucceeded only when
   * every page is done (a partial failure is never a whole-file success).
   */
  function planSummary(plan) {
    var s = { total: 0, pending: 0, running: 0, done: 0, failed: 0,
      processing: false, allSettled: true, allSucceeded: false };
    ((plan && plan.pages) || []).forEach(function (page) {
      s.total += 1;
      if (s[page.status] !== undefined) {
        s[page.status] += 1;
      }
    });
    s.processing = s.pending + s.running > 0;
    s.allSettled = s.total > 0 && s.pending + s.running === 0;
    s.allSucceeded = s.allSettled && s.failed === 0;
    return s;
  }

  /** A one-level row copy (rows are flat; the page tag rides every copy). */
  function shallowCopyRow(row, page) {
    var copy = {};
    Object.keys(row || {}).forEach(function (key) {
      copy[key] = row[key];
    });
    if (page !== undefined) {
      copy.page = page;
    }
    return copy;
  }

  /**
   * mergePageRows — replace ONE page's segment in the row array. this.rows
   * stays the single source of truth (edits the parent already made to
   * OTHER pages' rows survive every merge by reference); the page's old
   * rows are dropped and the freshly classified ones inserted at the
   * page's position (after smaller page numbers, before larger ones), so
   * out-of-order completion (a retried page) can never disorder the array.
   * New rows are tagged page=no here — the one place the page attribution
   * lands on a row (AC-1 保留结果所属页面; preview-level metadata,
   * ADR-0004 #3) — and every row is re-indexed to its global position:
   * index === position-in-array is the invariant the conflict radios and
   * cell edit handlers rely on (form.rows[index]).
   */
  function mergePageRows(rows, pageNo, pageRows) {
    var kept = (rows || []).filter(function (row) {
      return !row || (row.page || 0) !== pageNo;
    });
    var tagged = (pageRows || []).map(function (row) {
      return shallowCopyRow(row, pageNo);
    });
    var merged = [];
    var inserted = false;
    kept.forEach(function (row) {
      if (!inserted && (row.page || 0) > pageNo) {
        merged = merged.concat(tagged);
        inserted = true;
      }
      merged.push(row);
    });
    if (!inserted) {
      merged = merged.concat(tagged);
    }
    merged.forEach(function (row, i) {
      row.index = i;
    });
    return merged;
  }

  /**
   * rowsByPage — the flattened rows grouped in page order (page 0 = the
   * plain photo flow's untagged rows, one group). The preview's per-page
   * section headers render from this.
   */
  function rowsByPage(rows) {
    var byNo = {};
    var order = [];
    (rows || []).forEach(function (row) {
      var no = row && row.page ? row.page : 0;
      if (!Object.prototype.hasOwnProperty.call(byNo, no)) {
        byNo[no] = [];
        order.push(no);
      }
      byNo[no].push(row);
    });
    order.sort(function (a, b) { return a - b; });
    return order.map(function (no) {
      return { no: no, rows: byNo[no] };
    });
  }

  /**
   * applyLessonToPage — the 批量指定课次 toolbar's semantics scoped to ONE
   * page (AC-6): the lesson lands on the page's committable checked rows
   * only. isCommittable is looked up lazily (parent-photo.js loads after
   * this file; the call happens at click time, never at load time). Returns
   * a NEW array — the input rows are never mutated.
   */
  function applyLessonToPage(rows, pageNo, lesson) {
    var isCommittable = global.isCommittable || function () { return false; };
    var value = lesson == null ? '' : String(lesson);
    return (rows || []).map(function (row) {
      if (!row || (row.page || 0) !== pageNo) {
        return row;
      }
      // textbookMode is always true here: the per-page toolbar exists only
      // in textbook mode (the page head renders the input).
      if (isCommittable(row, true) && row.checked !== false) {
        var copy = shallowCopyRow(row);
        copy.lesson = value;
        return copy;
      }
      return row;
    });
  }

  /**
   * chunkBatch — slice a commit into the server's batch caps (100 entries
   * per add call, 500 ids per assign call) so a whole textbook commits
   * without the parent splitting it by hand (AC-5). Order preserved.
   */
  function chunkBatch(items, size) {
    var n = size > 0 ? size : 1;
    var chunks = [];
    var all = items || [];
    for (var i = 0; i < all.length; i += n) {
      chunks.push(all.slice(i, i + n));
    }
    return chunks;
  }

  // Window-or-module-global tail (state-manager.js convention).
  global.MAX_PDF_BYTES = MAX_PDF_BYTES;
  global.MAX_PDF_PAGES = MAX_PDF_PAGES;
  global.validatePdfFile = validatePdfFile;
  global.createPagePlan = createPagePlan;
  global.applyPageEvent = applyPageEvent;
  global.retryPage = retryPage;
  global.abortRunningPages = abortRunningPages;
  global.planSummary = planSummary;
  global.mergePageRows = mergePageRows;
  global.rowsByPage = rowsByPage;
  global.applyLessonToPage = applyLessonToPage;
  global.chunkBatch = chunkBatch;
})(typeof window !== 'undefined' ? window : this);
