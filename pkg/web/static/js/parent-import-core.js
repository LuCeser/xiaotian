/**
 * Issue #80 path B — the import-flow state core.
 * U3: per-photo recognition identity — each page/photo slot holds its own
 * header meta and OCR entries (including original lessons). Retries
 * REPLACE the slot; they never append a second snapshot for the same
 * photo index.
 * U6: the batch run state machine (§3.2) — run-level facts (停止 aborted /
 * 真实执行 executing / 失效 invalidated) beside the shared page plan, the
 * batch transitions 开始/停止/收尾/继续/重试/失效, and the ONE button rule.
 */
(function (global) {
  'use strict';

  function normalizePhotoPageNo(pageNo) {
    var n = pageNo == null ? 0 : Number(pageNo);
    return n > 0 ? n : 1;
  }

  function copyMeta(meta) {
    if (!meta || typeof meta !== 'object') {
      return null;
    }
    var out = {};
    Object.keys(meta).forEach(function (key) {
      var v = meta[key];
      out[key] = v == null ? '' : String(v);
    });
    return out;
  }

  /**
   * copyEntry — one entry copy plus, when known, its photo-local ordinal
   * (entryIndex: the entry's position within ITS photo's entries — the
   * stable identity word edits cannot erase; the row carries the same
   * ordinal, so a reclassified row still links to ITS original entry even
   * among same-page duplicate words).
   */
  function copyEntry(entry, entryIndex) {
    var copy = { word: '', meaning: '', lesson: '' };
    if (entry) {
      copy.word = entry.word == null ? '' : String(entry.word);
      copy.meaning = entry.meaning == null ? '' : String(entry.meaning);
      copy.lesson = entry.lesson == null ? '' : String(entry.lesson);
    }
    var idx = entry && entry.entryIndex != null ? entry.entryIndex : entryIndex;
    if (idx != null) {
      copy.entryIndex = Number(idx);
    }
    return copy;
  }

  function createImportPhotoState() {
    return { photos: {} };
  }

  function clearImportPhotoState(state) {
    if (!state) {
      return;
    }
    state.photos = {};
  }

  /**
   * setPhotoRecognition — register one landing from a real recognize entry
   * (single / multi / retry). Parsed shape matches parseRecognized.
   */
  function setPhotoRecognition(state, pageNo, parsed) {
    if (!state) {
      return;
    }
    var key = String(normalizePhotoPageNo(pageNo));
    var entries = (parsed && parsed.entries ? parsed.entries : []).map(
      function (entry, i) { return copyEntry(entry, i); });
    state.photos[key] = {
      meta: parsed && parsed.meta ? copyMeta(parsed.meta) : null,
      entries: entries,
    };
  }

  function getPhotoRecognition(state, pageNo) {
    if (!state) {
      return null;
    }
    return state.photos[String(normalizePhotoPageNo(pageNo))] || null;
  }

  /** Ordered meta slots for mergePhotoMetas (index 0 → page 1). */
  function photoMetaListFromState(state) {
    if (!state || !state.photos) {
      return [];
    }
    var nums = Object.keys(state.photos).map(Number).filter(function (n) {
      return n > 0;
    }).sort(function (a, b) { return a - b; });
    if (!nums.length) {
      return [];
    }
    var max = nums[nums.length - 1];
    var list = [];
    for (var p = 1; p <= max; p++) {
      var rec = state.photos[String(p)];
      list.push(rec && rec.meta ? copyMeta(rec.meta) : null);
    }
    return list;
  }

  function recognitionEntriesForPage(state, pageNo) {
    var rec = getPhotoRecognition(state, pageNo);
    if (!rec || !rec.entries) {
      return [];
    }
    return rec.entries.map(function (entry) { return copyEntry(entry); });
  }

  function recognitionMetaForPage(state, pageNo) {
    var rec = getPhotoRecognition(state, pageNo);
    return rec && rec.meta ? copyMeta(rec.meta) : null;
  }

  /**
   * Original OCR row for a preview row. Primary link: the row's photo-local
   * entryIndex ordinal (stamped at landing, carried by reclassify/edits) —
   * the parent may have corrected the word face, and same-page duplicate
   * words must link to their OWN entries, so the CURRENT word is not an
   * identity. Word matching remains only as the fallback for rows that
   * predate the ordinal.
   */
  function originalEntryForRow(state, row) {
    if (!row) {
      return null;
    }
    var rec = getPhotoRecognition(state, row.page);
    if (!rec || !rec.entries) {
      return null;
    }
    if (row.entryIndex != null) {
      var idx = Number(row.entryIndex);
      if (idx >= 0 && idx < rec.entries.length) {
        return copyEntry(rec.entries[idx]);
      }
      return null;
    }
    var word = row.word == null ? '' : String(row.word);
    for (var i = 0; i < rec.entries.length; i++) {
      if (rec.entries[i].word === word) {
        return copyEntry(rec.entries[i]);
      }
    }
    return null;
  }

  // ===== Issue #80 path B U6 — the batch run state machine (§3.2) =====

  /**
   * The run-level facts the §3.2 button table needs, kept BESIDE the page
   * plan (parent-pdf-core owns the per-page statuses pending/running/done/
   * failed; the page transitions 成功/失败 ride its applyPageEvent):
   *   plan        — the page plan this run drives (createPagePlan shape)
   *   aborted     — the parent STOPPED the batch (停止)
   *   executing   — an actual runner or its wind-down is in flight (真实执行)
   *   invalidated — the run was superseded/released; its results are dead
   *
   * The core rule: 剩余工作 (pending pages) is NOT execution. `executing`
   * is the ONLY busy truth — "pending means processing" is exactly the
   * conflation this machine replaces (rethink §1.1 row 4). U7/U8 wire the
   * photo/PDF UI to these transitions; until then the form keeps its own
   * flags. Transitions mutate the passed state (the setPhotoRecognition
   * precedent) and return it; page moves go through the shared page
   * machine via lazy globals (the applyLessonToPage convention).
   */
  function createImportRunState(plan) {
    return {
      plan: plan || { pages: [] },
      aborted: false,
      executing: false,
      invalidated: false,
    };
  }

  /**
   * 开始 — the batch starts executing (expects a fresh all-pending plan).
   * 失效是终态：a superseded run is never resurrected — a new run gets a
   * new state.
   */
  function beginImportRun(state) {
    if (!state || state.executing || state.invalidated) {
      return state; // no overlapping execution — ever
    }
    state.aborted = false;
    state.executing = true;
    return state;
  }

  /**
   * 停止 — the parent stops the batch. The in-flight page keeps running
   * until the wind-down settles (executing stays true → the 停止中 phase);
   * settleImportRun is what restores it to pending.
   */
  function stopImportRun(state) {
    if (!state) {
      return state;
    }
    state.aborted = true;
    return state;
  }

  /**
   * 收尾 — the runner chain (or a stopped run's wind-down) has fully
   * settled: executing goes false and any page still marked running
   * returns to pending, so 继续/重试 stay reachable even when a late
   * handler never fired (§3.2: 停止后当前 running 项恢复 pending).
   */
  function settleImportRun(state) {
    if (!state) {
      return state;
    }
    state.executing = false;
    if (state.plan && global.abortRunningPages) {
      state.plan = global.abortRunningPages(state.plan);
    }
    return state;
  }

  /**
   * 继续 — run the REMAINING work only: pages already done/failed are
   * never reset (继续不重跑 done); the runner takes pendingImportPages().
   */
  function resumeImportRun(state) {
    if (!state || state.executing || state.invalidated) {
      return state; // wind-down first — no overlapping execution; a dead run never resumes
    }
    state.aborted = false;
    state.executing = true;
    return state;
  }

  /**
   * 重试 — ONE failed page goes back to pending (失败重试只处理目标张);
   * the other failed pages stay failed. Starting it is an execution.
   */
  function retryImportPage(state, pageNo) {
    if (!state || state.executing || state.invalidated) {
      return state;
    }
    if (global.retryPage) {
      state.plan = global.retryPage(state.plan, pageNo);
    }
    state.aborted = false;
    state.executing = true;
    return state;
  }

  /**
   * 失效 — the run was superseded/released (a new run took over, the
   * subject switched): no result of this run may land again. Mirrors the
   * form-level epoch bump's plan-level effect; a dead run offers nothing.
   */
  function invalidateImportRun(state) {
    if (!state) {
      return state;
    }
    state.aborted = true;
    state.executing = false;
    state.invalidated = true;
    if (state.plan && global.abortRunningPages) {
      state.plan = global.abortRunningPages(state.plan);
    }
    return state;
  }

  /** The pages 开始/继续 will run — ONLY the pending ones (剩余工作). */
  function pendingImportPages(state) {
    if (!state || !state.plan || !state.plan.pages) {
      return [];
    }
    return state.plan.pages.filter(function (page) {
      return page.status === 'pending';
    }).map(function (page) { return page.no; });
  }

  /**
   * importRunCounts — the status counts the button rule and the summary
   * line need. Deliberately NOT planSummary: that helper still derives a
   * `processing` verdict from page counts — the very inference U6 removes
   * from the run verdicts (U8 drops it when the UI stops calling it).
   */
  function importRunCounts(state) {
    var c = { total: 0, pending: 0, running: 0, done: 0, failed: 0 };
    ((state && state.plan && state.plan.pages) || []).forEach(function (page) {
      c.total += 1;
      if (c[page.status] !== undefined) {
        c[page.status] += 1;
      }
    });
    return c;
  }

  /**
   * importRunDecision — THE button rule: §3.2's table as one function, so
   * no caller ever re-derives button state from scattered booleans and
   * page counts again (U7/U8 render from this). opts.noun is 页 (PDF) or
   * 张 (photos) — labels only, never the rule.
   *
   *   phase       §3.2 row                    buttons
   *   idle        尚未开始                    开始识别
   *   running     正在运行                    停止；继续/重试禁用；禁止保存
   *   stopping    已停止、旧执行未收尾        停止中（禁用）；继续暂禁；禁止保存
   *   stopped     已停止、已收尾、仍有剩余    继续处理；失败张可单独重试；可保存
   *   failed      已结束、含 failed           逐张重试；可保存成功部分
   *   done        全部完成                    无运行按钮
   *   invalidated 失效                        无按钮（迟到结果由执行身份拒绝）
   *
   * saveAllowed is the RUN-level gate only (忙碌禁止保存); whether anything
   * is committable at all stays the draft's own business (导入与进度分离 —
   * nothing is written before the parent confirms).
   */
  function importRunDecision(state, opts) {
    var noun = opts && opts.noun ? String(opts.noun) : '张';
    var c = importRunCounts(state);
    // 真实执行（含收尾）是唯一的忙碌事实——剩余工作绝不参与推断。
    var busy = !!(state && state.executing);
    var phase;
    if (state && state.invalidated) {
      phase = 'invalidated';
    } else if (busy) {
      phase = state.aborted ? 'stopping' : 'running';
    } else if (c.pending > 0) {
      // Settled with remaining work: virgin (no progress yet) shows 开始,
      // anything with progress — stopped or partially retried — shows 继续.
      phase = (c.done + c.failed + c.running === 0) ? 'idle' : 'stopped';
    } else {
      phase = c.failed > 0 ? 'failed' : 'done';
    }
    var failedPages = [];
    ((state && state.plan && state.plan.pages) || []).forEach(function (page) {
      if (page.status === 'failed') {
        failedPages.push(page.no);
      }
    });
    return {
      phase: phase,
      busy: busy,
      remaining: c.pending,
      counts: c,
      failedPages: failedPages,
      saveAllowed: !busy,
      actions: {
        start: {
          shown: phase === 'idle',
          enabled: phase === 'idle',
          label: '开始识别',
        },
        stop: {
          shown: phase === 'running' || phase === 'stopping',
          enabled: phase === 'running',
          label: phase === 'stopping'
            ? '停止中…'
            : '停止（保留已完成' + noun + '）',
        },
        resume: {
          shown: phase === 'running' || phase === 'stopping' || phase === 'stopped',
          enabled: phase === 'stopped',
          label: '继续处理（剩 ' + c.pending + ' ' + noun + '）',
        },
        retry: {
          shown: failedPages.length > 0,
          enabled: failedPages.length > 0 && !busy,
          pages: failedPages,
        },
      },
    };
  }

  global.createImportPhotoState = createImportPhotoState;
  global.clearImportPhotoState = clearImportPhotoState;
  global.setPhotoRecognition = setPhotoRecognition;
  global.getPhotoRecognition = getPhotoRecognition;
  global.photoMetaListFromState = photoMetaListFromState;
  global.recognitionEntriesForPage = recognitionEntriesForPage;
  global.recognitionMetaForPage = recognitionMetaForPage;
  global.originalEntryForRow = originalEntryForRow;
  global.normalizePhotoPageNo = normalizePhotoPageNo;
  global.createImportRunState = createImportRunState;
  global.beginImportRun = beginImportRun;
  global.stopImportRun = stopImportRun;
  global.settleImportRun = settleImportRun;
  global.resumeImportRun = resumeImportRun;
  global.retryImportPage = retryImportPage;
  global.invalidateImportRun = invalidateImportRun;
  global.pendingImportPages = pendingImportPages;
  global.importRunCounts = importRunCounts;
  global.importRunDecision = importRunDecision;
})(typeof window !== 'undefined' ? window : this);
