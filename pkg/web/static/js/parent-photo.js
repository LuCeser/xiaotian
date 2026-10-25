/**
 * @file Parent page — 照片导入 (Issue #3 + #5): the parent photographs an
 *       English word list (PNG/JPEG), the service recognizes DRAFT entries
 *       AND the textbook header it can actually read through the vision
 *       seam, and the parent reviews/edits/checks everything before any
 *       commit.
 *
 * Contract (Issue #3 acceptance criteria):
 *   - Recognition result is a DRAFT ONLY. Nothing is written until 确认添加
 *     posts through the ordinary POST /api/v1/words path; 放弃识别结果 (or
 *     navigating away) leaves the library untouched — the server keeps no
 *     draft state at all.
 *   - The preview shows EVERY recognized entry with the photo's own
 *     meaning; word and meaning are inline-editable, every 新词 row is
 *     checked by default, and phrases keep their spaces (one row = one
 *     dictation entry).
 *   - Existing words are never re-added (the server's UNIQUE(word,
 *     category) would skip them anyway — history is never reset). Rows
 *     whose word already exists show 已有; when the existing meaning
 *     DIFFERS, the row shows the conflict, names the library's meaning and
 *     is NOT submittable in this flow (keep-the-original rule; the full
 *     multi-source merge is a later task).
 *   - The commit result reports per-item outcomes (已添加 / 已有 / 未添加
 *     with reasons) plus a summary — a partial success is NEVER rendered
 *     as a plain success (transparency prohibition, batch-form precedent).
 *   - No example field: the first flow does not require example sentences.
 *
 * Contract (Issue #5 acceptance criteria, on top):
 *   - The preview shows the READ-off-the-photo textbook header (版本·学段·
 *     年级·上下册·模块·课次); unread fields show 待确认 and are NEVER
 *     fabricated from the grade or the filename (AC2).
 *   - 教材归属 lets the parent confirm an EXISTING textbook (preselected
 *     only on an honest meta match) or create a NEW one prefilled from the
 *     meta — the missing background is confirmed ONCE per textbook (the
 *     book stores it; AC1/AC2).
 *   - The whole page (or any selection) can be given one lesson in bulk;
 *     per-row lesson editing stays available; the CONFIRMED result — not
 *     the initial recognition — decides the final attribution (AC3).
 *   - Not-yet-learned lessons store fine but stay out of automatic
 *     selection (the #4 scope predicate enforces this server-side; the
 *     receipt says so honestly, AC4).
 *   - Rows without a lesson: the page-level 「待确认课次的词直接加入默写范围」
 *     checkbox is the explicit include-in-scope handling; supplementary
 *     classification never requires inventing a lesson (AC5).
 *   - 不指定教材 keeps the exact Issue #3 supplementary flow.
 *
 * Contract (Issue #81, the A 并排核对 workspace — on top of #3/#5/#7/#8):
 *   - Once a file is picked the tab becomes a two-column workspace on wide
 *     screens: LEFT 材料与学习安排 (the original photo large beside the
 *     review, the 教材归属 panel, the 释义补充 bar), RIGHT 词条核对 (the
 *     editable rows with 单词/释义 visually primary, 课次 secondary, the
 *     status cell separate) + the footer actions. Narrow screens stack in
 *     the same task order: 材料 → 学习安排 → 词条 → 操作.
 *   - A compact state line above the workspace states 教材 · 已学进度 ·
 *     本批词数 · 本批课次 and the honest scheduling note (已学到 → 保存后
 *     可安排默写; 未学到 → 暂不自动安排). It is INFORMATION ONLY — known
 *     progress is silently reused, the UI never asks about it.
 *   - The receipt separates 保存结果 (the per-item buckets, unchanged
 *     wording) from 默写安排 (counts + reasons + a next-step hint) —
 *     never one flat "success".
 *   - PDF runs show the CURRENT page render in the left column (PdfPage-
 *     Runner blobs) with the page status bar right under it.
 *
 * Contract (Issue #7 acceptance criteria, on top):
 *   - A scanned PDF picked here splits INTO PAGES IN THE BROWSER
 *     (PdfPageRunner, parent-pdf.js; ADR-0004): pages run sequentially
 *     through the SAME recognize call as a photo, one page in memory at a
 *     time. The draft stays client-side only — nothing is written until
 *     确认添加, exactly like a photo.
 *   - Page attribution is preview-level metadata: rows carry page, the
 *     preview groups by page (页头 = 页码 + 状态 + 本页批量课次), and
 *     re-recognizing a page REPLACES its segment (mergePageRows) so a
 *     retry can never duplicate or disorder results. The cross-page
 *     same-word dedup rides classifyPhotoEntries over the merged array.
 *   - Page-level failures land on that page's status entry with the
 *     reason and a 重试该页 button; completed pages keep their results
 *     (AC-4). A run with any failed page is NEVER a whole-file success
 *     (AC-3's 不伪称).
 *   - 文件 40MiB／100 页上限在加载前拒绝（ADR-0004 拍板）；整本教材的
 *     提交自动按 100/批新增、500/批课次指定分片，聚合真实响应出回执。
 *
 * classifyPhotoEntries / matchTextbookForMeta / distinctLessons /
 * distinctRowLessons / planAssignments are PURE functions exposed on the
 * window for the Node unit tests (tests/test-parent-photo-classify.js,
 * tests/test-parent-photo-textbook.js, tests/test-parent-photo-pdf.js —
 * window.parseBatch precedent); the PDF pure functions live in
 * parent-pdf-core.js; everything DOM-ish lives in PhotoAddForm.
 *
 * Security (Pitfall 10): all rendering goes through document.createElement
 * + Node.textContent — no raw-markup setters anywhere in this file.
 */

(function (global) {
  'use strict';

  /**
   * Client-side upload target. The server caps one photo at
   * photo.maxImageBytes (default 1MiB, pkg/infra PhotoConfig); the client
   * stays under it with headroom, mirroring the student photo controller's
   * 768KiB target.
   */
  var MAX_UPLOAD_BYTES = 768 * 1024;

  /** Per-request entry cap — mirrors pkg/web/api maxEntries. */
  var MAX_BATCH_ENTRIES = 100;

  /**
   * Assign-batch cap — mirrors pkg/web/api validate.go's batch-ids cap;
   * one (lesson, includeInScope) group larger than this is sliced into
   * sequential assign calls (ADR-0004 #6).
   */
  var MAX_ASSIGN_IDS = 500;

  /** Field caps — mirror pkg/web/api validate.go (runes, not bytes). */
  var MAX_WORD_RUNES = 200;
  var MAX_MEANING_RUNES = 1000;

  /**
   * Canvas downscale ladder (student photo-controller shape): longest edge
   * then JPEG quality, stopping at ≤ MAX_UPLOAD_BYTES. A small enough
   * original never touches the ladder — it uploads verbatim, which is the
   * only path that preserves a PNG as PNG end-to-end.
   */
  var QUALITY_LADDER = [
    { edge: 1600, quality: 0.8 },
    { edge: 1600, quality: 0.6 },
    { edge: 1280, quality: 0.6 },
    { edge: 1024, quality: 0.5 },
  ];

  /**
   * Classify recognized entries against the CURRENT library snapshot
   * (existingWords: [{word, meaning}] for the english category). Pure: no
   * DOM, no fetch — the Node unit test pins the contract.
   *
   * Statuses:
   *   'new'      — submittable (checked by default)
   *   'exists'   — same word AND same meaning already in the library
   *   'conflict' — word exists with a DIFFERENT meaning: show the conflict,
   *                keep the original, NOT submittable in this flow
   *   'invalid'  — fails an add-path rule (reason carries the cause)
   *
   * Comparison is exact-after-trim on word (the server's UNIQUE semantics)
   * and trim on meaning. Later duplicates within the photo are 'invalid'
   * with reason 'duplicate:<firstIndex+1>'.
   */
  function classifyPhotoEntries(entries, existingWords) {
    var byWord = {};
    (existingWords || []).forEach(function (w) {
      if (w && w.word) {
        byWord[String(w.word).trim()] = w.meaning == null ? '' : String(w.meaning).trim();
      }
    });

    var seen = {};
    return (entries || []).map(function (e, i) {
      var word = (e && e.word ? String(e.word) : '').trim();
      var meaning = (e && e.meaning != null ? String(e.meaning) : '').trim();
      var lesson = (e && e.lesson != null ? String(e.lesson) : '').trim();
      var row = { index: i, word: word, meaning: meaning, lesson: lesson, status: 'new', reason: '', existingMeaning: '', resolution: '' };

      if (!word) {
        row.status = 'invalid';
        row.reason = 'empty-word';
        return row;
      }
      if (word.indexOf(',') >= 0) {
        row.status = 'invalid';
        row.reason = 'word-comma';
        return row;
      }
      if (word.length > MAX_WORD_RUNES) {
        row.status = 'invalid';
        row.reason = 'word-too-long';
        return row;
      }
      if (meaning.length > MAX_MEANING_RUNES) {
        row.status = 'invalid';
        row.reason = 'meaning-too-long';
        return row;
      }
      if (seen[word] !== undefined) {
        row.status = 'invalid';
        row.reason = 'duplicate:' + (seen[word] + 1);
        return row;
      }
      seen[word] = i;

      if (Object.prototype.hasOwnProperty.call(byWord, word)) {
        var existingMeaning = byWord[word];
        row.existingMeaning = existingMeaning;
        if (existingMeaning === meaning) {
          row.status = 'exists';
        } else {
          row.status = 'conflict';
        }
      }
      return row;
    });
  }

  /**
   * parseRecognized — one home for the recognize-reply shape shared by the
   * photo path and every PDF page: the entries array (absent → []), the
   * textbook header (an all-empty one cut to null — honesty cuts both
   * ways), and since Issue #11 the mixed-page arrays: whole poems (absent
   * → []), whole passages (absent → [], Issue #13) and the display-only
   * other blocks (absent → []).
   */
  function parseRecognized(data) {
    var entries = data && Array.isArray(data.entries) ? data.entries : [];
    var poems = data && Array.isArray(data.poems) ? data.poems : [];
    var passages = data && Array.isArray(data.passages) ? data.passages : [];
    var others = data && Array.isArray(data.others) ? data.others : [];
    var meta = data && data.textbook && typeof data.textbook === 'object'
      ? data.textbook : null;
    if (meta && !META_KEYS.concat(['lesson']).some(function (key) {
      return metaField(meta[key]);
    })) {
      meta = null;
    }
    return { entries: entries, poems: poems, passages: passages, others: others, meta: meta };
  }

  // ------------------------------------------------------------------
  // Issue #11 — whole-poem draft functions (window-exposed for the Node
  // test, tests/test-parent-photo-poem.js — the classifyPhotoEntries kin).
  // ------------------------------------------------------------------

  /** Poem field caps — mirror pkg/web/api validate.go (runes, not bytes). */
  var MAX_POEM_TITLE_RUNES = 200;
  var MAX_POEM_AUTHOR_RUNES = 100;
  var MAX_POEM_LINE_RUNES = 200;
  var MAX_POEM_LINES = 64;
  /** One POST /poems call carries at most 20 poems (validate.go maxPoems). */
  var MAX_POEM_BATCH = 20;

  /**
   * normalizePoemText — the CLIENT mirror of the server's fingerprint
   * normalization: trimmed title + trimmed author + trimmed non-empty
   * lines joined in order. The existing-poems comparison is exact over
   * this text (identity is the FULL work, never the title — the server
   * re-verifies with its own hash; the client classification is a preview
   * convenience, the commit's per-poem outcome stays authoritative).
   */
  function normalizePoemText(title, author, lines) {
    var clean = (lines || []).map(function (l) {
      return l == null ? '' : String(l).trim();
    }).filter(function (l) { return l !== ''; });
    return metaField(title) + '\n' + metaField(author) + '\n' + clean.join('\n');
  }

  /**
   * poemRequiredLabel — the page's own explicit 背诵/默写 mark as a badge
   * label ('' when the page printed none). Only the two known marks carry
   * a label; anything else the model reported still reads honestly as an
   * unspecified requirement.
   */
  function poemRequiredLabel(required) {
    var v = metaField(required);
    if (v === '背诵' || v === '默写') {
      return '要求' + v;
    }
    return '';
  }

  /**
   * classifyPoemDrafts (Issue #11) — classify recognized whole poems
   * against the CURRENT poem library snapshot (existingPoems: [{title,
   * author, lines}] — the GET /poems shape). Pure: no DOM, no fetch; the
   * Node unit test pins the contract.
   *
   * Statuses (the classifyPhotoEntries kin):
   *   'new'      — committable; checked defaults to the page's own explicit
   *                背诵/默写 mark (明确背诵/默写要求影响默认选择 — an
   *                unmarked poem stays unchecked for the parent to choose)
   *   'exists'   — the FULL normalized work already stored (重复确认不复制;
   *                a same-title different-text poem stays 'new' — identity
   *                is the complete text, never the title)
   *   'invalid'  — fails a commit rule (reason carries the cause)
   */
  function classifyPoemDrafts(poems, existingPoems) {
    var byText = {};
    (existingPoems || []).forEach(function (p) {
      if (p && p.title) {
        byText[normalizePoemText(p.title, p.author, p.lines)] = p;
      }
    });

    var seen = {};
    return (poems || []).map(function (p, i) {
      var title = metaField(p && p.title);
      var author = metaField(p && p.author);
      var lines = (p && Array.isArray(p.lines) ? p.lines : [])
        .map(function (l) { return l == null ? '' : String(l).trim(); })
        .filter(function (l) { return l !== ''; });
      var lesson = metaField(p && p.lesson);
      var required = metaField(p && p.required);
      var row = {
        index: i, title: title, author: author, lines: lines, lesson: lesson,
        required: required, status: 'new', reason: '', checked: false, page: 0,
      };

      if (!title) {
        row.status = 'invalid';
        row.reason = 'empty-title';
        return row;
      }
      if (title.length > MAX_POEM_TITLE_RUNES) {
        row.status = 'invalid';
        row.reason = 'title-too-long';
        return row;
      }
      if (author.length > MAX_POEM_AUTHOR_RUNES) {
        row.status = 'invalid';
        row.reason = 'author-too-long';
        return row;
      }
      if (!lines.length) {
        row.status = 'invalid';
        row.reason = 'no-lines';
        return row;
      }
      if (lines.length > MAX_POEM_LINES) {
        row.status = 'invalid';
        row.reason = 'too-many-lines';
        return row;
      }
      if (lines.some(function (l) { return l.length > MAX_POEM_LINE_RUNES; })) {
        row.status = 'invalid';
        row.reason = 'line-too-long';
        return row;
      }

      var text = normalizePoemText(title, author, lines);
      if (seen[text] !== undefined) {
        row.status = 'invalid';
        row.reason = 'duplicate:' + (seen[text] + 1);
        return row;
      }
      seen[text] = i;

      // The page's own explicit mark drives the default selection; an
      // unmarked poem is selectable but not auto-checked.
      row.checked = poemRequiredLabel(required) !== '';

      if (Object.prototype.hasOwnProperty.call(byText, text)) {
        row.status = 'exists';
      }
      return row;
    });
  }

  /** Human-readable text for one invalid poem reason. */
  function describePoemInvalidReason(reason) {
    if (reason === 'empty-title') {
      return '诗题不能为空';
    }
    if (reason === 'title-too-long') {
      return '诗题超过 ' + MAX_POEM_TITLE_RUNES + ' 个字符';
    }
    if (reason === 'author-too-long') {
      return '作者超过 ' + MAX_POEM_AUTHOR_RUNES + ' 个字符';
    }
    if (reason === 'no-lines') {
      return '诗句不能为空';
    }
    if (reason === 'too-many-lines') {
      return '诗句超过 ' + MAX_POEM_LINES + ' 行';
    }
    if (reason === 'line-too-long') {
      return '单行诗句超过 ' + MAX_POEM_LINE_RUNES + ' 个字符';
    }
    if (/^duplicate:/.test(reason)) {
      return '与第 ' + reason.slice('duplicate:'.length) + ' 首重复';
    }
    return reason;
  }

  /**
   * mergePagePoems (Issue #11, PDF runs) — the mergePageRows kin for poem
   * drafts: the newly recognized page's poems REPLACE that page's segment
   * (a retry can never duplicate), other pages' poems survive untouched,
   * and the incoming drafts re-classify against the existing library
   * (classifyPoemDrafts) so cross-page duplicate poems flag themselves.
   */
  function mergePagePoems(current, pageNo, incoming, existingPoems) {
    var kept = (current || []).filter(function (r) { return r.page !== pageNo; });
    var fresh = (incoming || []).map(function (p, i) {
      return Object.assign({ page: pageNo }, p, { index: i });
    });
    var classified = classifyPoemDrafts(fresh, existingPoems);
    return kept.concat(classified.map(function (r) {
      r.page = pageNo;
      return r;
    }));
  }

  /**
   * mergePageOthers (Issue #11, PDF runs) — the display-only others array
   * replaces the retried page's segment the same way poems do (page-tagged
   * by arrival; nothing here is committable).
   */
  function mergePageOthers(current, pageNo, incoming) {
    var kept = (current || []).filter(function (o) { return o.page !== pageNo; });
    (incoming || []).forEach(function (o) {
      kept.push({
        page: pageNo,
        kind: o && o.kind === 'exercise' ? 'exercise' : 'text',
        summary: metaField(o && o.summary),
      });
    });
    return kept;
  }

  // ------------------------------------------------------------------
  // Issue #13 — whole-passage draft functions (window-exposed for the
  // Node test, tests/test-parent-photo-passages.js — the poem kin).
  // ------------------------------------------------------------------

  /** Passage field caps — mirror pkg/web/api validate.go (runes, not bytes). */
  var MAX_PASSAGE_TITLE_RUNES = 200;
  var MAX_PASSAGE_AUTHOR_RUNES = 100;
  var MAX_PASSAGE_PARAGRAPH_RUNES = 1000;
  var MAX_PASSAGE_PARAGRAPHS = 60;
  /** One POST /passages call carries at most 10 passages (validate.go maxPassages). */
  var MAX_PASSAGE_BATCH = 10;

  /**
   * normalizePassageText — the CLIENT mirror of the server's passage
   * fingerprint normalization: trimmed title + trimmed author + trimmed
   * non-empty paragraphs joined in order. The existing-passages comparison
   * is exact over this text (identity is the FULL original, never the
   * title — a corrected text is a different passage until confirmed; the
   * server re-verifies with its own hash; the client classification is a
   * preview convenience, the commit's per-passage outcome stays
   * authoritative).
   */
  function normalizePassageText(title, author, paragraphs) {
    var clean = (paragraphs || []).map(function (p) {
      return p == null ? '' : String(p).trim();
    }).filter(function (p) { return p !== ''; });
    return metaField(title) + '\n' + metaField(author) + '\n' + clean.join('\n');
  }

  /**
   * passageRequiredLabel — the page's own whole-text 背诵/默写 mark as a
   * badge label ('' when the page printed none). A passage's mark usually
   * reads 背诵课文/默写课文, so the two known verbs are matched as
   * substrings; anything else the model reported still reads honestly as
   * an unspecified requirement.
   */
  function passageRequiredLabel(required) {
    var v = metaField(required);
    if (v.indexOf('背诵') >= 0) {
      return '要求背诵';
    }
    if (v.indexOf('默写') >= 0) {
      return '要求默写';
    }
    return '';
  }

  /**
   * classifyPassageDrafts (Issue #13) — classify recognized whole passages
   * against the CURRENT passage library snapshot (existingPassages:
   * [{title, author, paragraphs}] — the GET /passages shape). Pure: no
   * DOM, no fetch; the Node unit test pins the contract.
   *
   * Statuses (the classifyPoemDrafts kin):
   *   'new'      — committable; the default range follows the page's own
   *                marks (有背诵标记的段落默认勾选 — required_paragraphs
   *                selects exactly those paragraphs; a whole-text 背诵/默写
   *                mark selects 整篇; an unmarked passage stays unchecked
   *                with NO range — 其他正文可按篇或段选择, the parent
   *                chooses; a checked-but-rangeless passage commits as
   *                text-only, the range is picked later on the content
   *                page)
   *   'exists'   — the FULL normalized original already stored (重复确认
   *                不复制; a same-title different-text passage stays 'new')
   *   'invalid'  — fails a commit rule (reason carries the cause)
   *
   * The row carries the scope choice the preview edits: scopeWhole +
   * scopeParagraphs (1-based 自然段 numbers over the kept paragraphs).
   */
  function classifyPassageDrafts(passages, existingPassages) {
    var byText = {};
    (existingPassages || []).forEach(function (p) {
      if (p && p.title) {
        byText[normalizePassageText(p.title, p.author, p.paragraphs)] = p;
      }
    });

    var seen = {};
    return (passages || []).map(function (p, i) {
      var title = metaField(p && p.title);
      var author = metaField(p && p.author);
      var paragraphs = (p && Array.isArray(p.paragraphs) ? p.paragraphs : [])
        .map(function (t) { return t == null ? '' : String(t).trim(); })
        .filter(function (t) { return t !== ''; });
      var lesson = metaField(p && p.lesson);
      var required = metaField(p && p.required);
      var row = {
        index: i, title: title, author: author, paragraphs: paragraphs,
        lesson: lesson, required: required, requiredParagraphs: [],
        status: 'new', reason: '', checked: false, page: 0,
        scopeWhole: false, scopeParagraphs: [],
      };

      if (!title) {
        row.status = 'invalid';
        row.reason = 'empty-title';
        return row;
      }
      if (title.length > MAX_PASSAGE_TITLE_RUNES) {
        row.status = 'invalid';
        row.reason = 'title-too-long';
        return row;
      }
      if (author.length > MAX_PASSAGE_AUTHOR_RUNES) {
        row.status = 'invalid';
        row.reason = 'author-too-long';
        return row;
      }
      if (!paragraphs.length) {
        row.status = 'invalid';
        row.reason = 'no-paragraphs';
        return row;
      }
      if (paragraphs.length > MAX_PASSAGE_PARAGRAPHS) {
        row.status = 'invalid';
        row.reason = 'too-many-paragraphs';
        return row;
      }
      if (paragraphs.some(function (t) { return t.length > MAX_PASSAGE_PARAGRAPH_RUNES; })) {
        row.status = 'invalid';
        row.reason = 'paragraph-too-long';
        return row;
      }

      var text = normalizePassageText(title, author, paragraphs);
      if (seen[text] !== undefined) {
        row.status = 'invalid';
        row.reason = 'duplicate:' + (seen[text] + 1);
        return row;
      }
      seen[text] = i;

      // The page's own marks drive the default range (有背诵标记的段落
      // 默认勾选): per-paragraph marks select exactly those paragraphs, a
      // whole-text mark selects 整篇, anything else leaves the choice open.
      var marks = (p && Array.isArray(p.required_paragraphs) ? p.required_paragraphs : [])
        .filter(function (n) { return n >= 1 && n <= paragraphs.length; })
        .sort(function (a, b) { return a - b; })
        .filter(function (n, j, arr) { return j === 0 || arr[j - 1] !== n; });
      row.requiredParagraphs = marks;
      if (marks.length) {
        row.checked = true;
        row.scopeWhole = false;
        row.scopeParagraphs = marks.slice();
      } else if (passageRequiredLabel(required)) {
        row.checked = true;
        row.scopeWhole = true;
        row.scopeParagraphs = [];
      }

      if (Object.prototype.hasOwnProperty.call(byText, text)) {
        row.status = 'exists';
      }
      return row;
    });
  }

  /** Human-readable text for one invalid passage reason. */
  function describePassageInvalidReason(reason) {
    if (reason === 'empty-title') {
      return '课文标题不能为空';
    }
    if (reason === 'title-too-long') {
      return '课文标题超过 ' + MAX_PASSAGE_TITLE_RUNES + ' 个字符';
    }
    if (reason === 'author-too-long') {
      return '作者超过 ' + MAX_PASSAGE_AUTHOR_RUNES + ' 个字符';
    }
    if (reason === 'no-paragraphs') {
      return '自然段不能为空';
    }
    if (reason === 'too-many-paragraphs') {
      return '自然段超过 ' + MAX_PASSAGE_PARAGRAPHS + ' 段';
    }
    if (reason === 'paragraph-too-long') {
      return '单个自然段超过 ' + MAX_PASSAGE_PARAGRAPH_RUNES + ' 个字符';
    }
    if (/^duplicate:/.test(reason)) {
      return '与第 ' + reason.slice('duplicate:'.length) + ' 篇重复';
    }
    return reason;
  }

  /**
   * mergePagePassages (Issue #13, PDF runs) — the mergePagePoems kin for
   * passage drafts: the newly recognized page's passages REPLACE that
   * page's segment (a retry can never duplicate), other pages' passages
   * survive untouched, and the incoming drafts re-classify against the
   * existing library so cross-page duplicate passages flag themselves.
   */
  function mergePagePassages(current, pageNo, incoming, existingPassages) {
    var kept = (current || []).filter(function (r) { return r.page !== pageNo; });
    var fresh = (incoming || []).map(function (p, i) {
      return Object.assign({ page: pageNo }, p, { index: i });
    });
    var classified = classifyPassageDrafts(fresh, existingPassages);
    return kept.concat(classified.map(function (r) {
      r.page = pageNo;
      return r;
    }));
  }

  /** Human-readable text for one invalid reason (batch-parser copy kin). */
  function describeInvalidReason(reason) {
    if (reason === 'empty-word') {
      return '单词不能为空';
    }
    if (reason === 'word-comma') {
      return '单词不能包含英文逗号';
    }
    if (reason === 'word-too-long') {
      return '单词超过 ' + MAX_WORD_RUNES + ' 个字符';
    }
    if (reason === 'meaning-too-long') {
      return '释义超过 ' + MAX_MEANING_RUNES + ' 个字符';
    }
    if (/^duplicate:/.test(reason)) {
      return '与第 ' + reason.slice('duplicate:'.length) + ' 行重复';
    }
    return reason;
  }

  // ------------------------------------------------------------------
  // Issue #5 textbook pure functions — window-exposed for the Node test
  // (tests/test-parent-photo-textbook.js).
  // ------------------------------------------------------------------

  /**
   * The meta fields that identify a textbook (lesson excluded — it is
   * per-page/per-word, not part of the book's identity). ONE encoding of
   * the key/label/placeholder clump serves the meta line, the create form
   * and the backfill patch.
   */
  var META_FIELDS = [
    { key: 'edition', label: '版本', ph: '如 人教版PEP' },
    { key: 'stage', label: '学段', ph: '如 小学' },
    { key: 'grade', label: '年级', ph: '如 三年级' },
    { key: 'volume', label: '上下册', ph: '如 上册' },
    { key: 'module', label: '模块', ph: '如 Unit 1-3' },
  ];
  var META_KEYS = META_FIELDS.map(function (f) { return f.key; });

  /** Trim to a string ('' for null-ish) — the meta/lesson value shape. */
  function metaField(v) {
    return v == null ? '' : String(v).trim();
  }

  /**
   * distinctLessons — the distinct non-empty trimmed lesson NAMES, keeping
   * first-appearance order (teaching order is explicit; never sorted).
   * For plain string lists (a book's lesson list, the append candidates).
   */
  function distinctLessons(names) {
    var seen = {};
    var lessons = [];
    (names || []).forEach(function (name) {
      var value = metaField(name);
      if (value && !seen[value]) {
        seen[value] = true;
        lessons.push(value);
      }
    });
    return lessons;
  }

  /**
   * distinctRowLessons — the distinct non-empty lessons among preview ROWS
   * ({lesson} objects), in row order. Shared by the datalist options, the
   * append-note and the final lesson list.
   */
  function distinctRowLessons(rows) {
    return distinctLessons((rows || []).map(function (row) {
      return row && row.lesson ? row.lesson : '';
    }));
  }

  /** The two parent confirmations a 释义差异 row can carry (Issue #6). */
  var RESOLUTIONS = ['keep', 'adopt'];

  /** isResolved — the ONE home for "this conflict row carries a parent
   * confirmation" (the literal validity rule every consumer asks). */
  function isResolved(row) {
    return !!row && row.status === 'conflict' &&
      RESOLUTIONS.indexOf(row.resolution) >= 0;
  }

  /**
   * isCommittable — one home for the textbook-mode rule: a row takes part
   * in the commit when it is new, or already in the library and only
   * needs its lesson assigned (exists + textbook mode); a conflict joins
   * ONLY after the parent resolved it (Issue #6 AC-3: 家长确认后处理 —
   * unresolved conflicts stay 待处理差异, never silently merged; the
   * resolution choice is deliberately textbook-mode-only — supplementary
   * imports have no source to merge, so their conflicts stay dead ends);
   * invalid never does.
   */
  function isCommittable(row, textbookMode) {
    if (!row) {
      return false;
    }
    if (row.status === 'new') {
      return true;
    }
    if (!textbookMode) {
      return false;
    }
    return row.status === 'exists' || isResolved(row);
  }

  /**
   * submitChunked — ONE home for the sliced sequential add loop (both
   * commit paths): the rows go out in MAX_BATCH_ENTRIES-sized calls, the
   * outcomes aggregate, and a mid-run slice failure stops the run and
   * rejects with err.partialOutcome carrying what already landed (Issue
   * #7, ADR-0004 #6 — a whole textbook commits without the parent
   * splitting it by hand).
   */
  function submitChunked(form, rows) {
    var unit = form.unitInput ? form.unitInput.value.trim() : '';
    var outcome = { added: 0, skipped: 0 };
    var chain = Promise.resolve();
    global.chunkBatch(rows, MAX_BATCH_ENTRIES).forEach(function (part) {
      chain = chain.then(function () {
        return form.submitWords({
          // Issue #10: the DRAFT's subject — locked at recognize time, so
          // a mid-flow radio switch (which resets the draft) can never
          // commit rows under the other subject.
          category: form.draftCategory,
          unit: unit,
          entries: part.map(function (r) {
            return {
              word: r.word,
              meaning: r.meaning,
              example: '',
              // Issue #8: the provenance rides the add path (ADR-0005 #3);
              // the only distinguishable value is 'generated' — everything
              // else (material original, hand-typed, cancelled-missing)
              // stores 'material'.
              meaning_source: r.meaningSource === 'generated' ? 'generated' : 'material',
            };
          }),
        }).then(function (result) {
          outcome.added += (result && result.added) || 0;
          outcome.skipped += (result && result.skipped) || 0;
        });
      });
    });
    return chain.then(function () {
      return outcome;
    }).catch(function (err) {
      err.partialOutcome = outcome;
      throw err;
    });
  }

  /** Clear one element through the shared kit (fallback: textContent). */
  function clearEl(el) {
    var kit = global.__parentFormKit || {};
    if (kit.clearContainer) {
      kit.clearContainer(el);
    } else {
      el.textContent = '';
    }
  }

  /**
   * matchTextbookForMeta — pick the existing textbook the recognized meta
   * ACTUALLY describes, or null when none does (the parent then confirms
   * or creates — AC2's no-fabrication rule starts here: a lookalike is
   * never auto-selected). A candidate qualifies when every NON-EMPTY meta
   * field equals the book's field; the winner is the candidate with the
   * most matched non-empty fields, ties resolved by list order (creation
   * order). Pure.
   */
  function matchTextbookForMeta(books, meta) {
    if (!books || !books.length || !meta) {
      return null;
    }
    var best = null;
    var bestScore = 0;
    books.forEach(function (book) {
      var score = 0;
      var qualifies = true;
      META_KEYS.forEach(function (key) {
        var value = metaField(meta[key]);
        if (!value) {
          return; // unread field constrains nothing
        }
        if (metaField(book[key]) === value) {
          score++;
        } else {
          qualifies = false;
        }
      });
      if (qualifies && score > bestScore) {
        best = book;
        bestScore = score;
      }
    });
    return bestScore > 0 ? best : null;
  }

  /**
   * planAssignments — group the committable rows into textbook-assign
   * calls (POST /textbooks/{id}/assign takes ONE lesson + one
   * includeInScope per batch). Committable = checked new/exists rows and
   * conflicts the parent RESOLVED (Issue #6), each with a card id
   * resolved from the post-commit library snapshot (unresolved conflicts
   * and invalid rows never reach a textbook). Groups keep
   * first-appearance order. includePending=true (the page-level 「待确认
   * 课次的词直接加入默写范围」checkbox) lifts ONLY the lesson-less group
   * into scope — rows with a real lesson always ride the textbook's
   * progress.
   *
   * Sticky include (Issue #6 AC-4): snapshotRows is the GET /words shape
   * ({id, word, textbooks:[{id, lesson, includeInScope}]}); when the word
   * ALREADY carries include_in_scope=1 on the TARGET textbook, the assign
   * call re-sends include=true — a re-import must never silently clear a
   * parent's explicit include (the assign upsert is last-write-wins, so
   * the preservation has to ride the payload). Pure.
   */
  function planAssignments(rows, snapshotRows, bookId, includePending) {
    var byWord = {};
    (snapshotRows || []).forEach(function (w) {
      if (w && w.word) {
        byWord[String(w.word).trim()] = w;
      }
    });
    var groups = [];
    var byKey = {};
    (rows || []).forEach(function (row) {
      if (!row || !isCommittable(row, true) || row.checked === false) {
        return;
      }
      var snap = Object.prototype.hasOwnProperty.call(byWord, row.word) ? byWord[row.word] : null;
      if (!snap || !snap.id) {
        return; // no resolved card → no honest assignment (receipt says so)
      }
      var lesson = metaField(row.lesson);
      var sticky = false;
      (snap.textbooks || []).forEach(function (a) {
        if (a && a.id === bookId && a.includeInScope) {
          sticky = true;
        }
      });
      var include = sticky || (includePending && !lesson);
      var key = lesson + '\u0000' + (include ? '1' : '0');
      if (!Object.prototype.hasOwnProperty.call(byKey, key)) {
        byKey[key] = { lesson: lesson, includeInScope: include, cardIds: [], words: [] };
        groups.push(byKey[key]);
      }
      byKey[key].cardIds.push(snap.id);
      byKey[key].words.push(row.word);
    });
    return groups;
  }

  // ------------------------------------------------------------------
  // PhotoAddForm — pick → recognize → review/edit → commit (photo tab)
  // ------------------------------------------------------------------

  /**
   * options:
   *   recognizePhoto(blob, category) → Promise<{textbook, entries: [...]}> —
   *       POST /api/v1/words/photo/recognize?category= (wired in
   *       parent-app.js with its own longer timeout; the vision call can
   *       take ~20s). The subject selects the extraction contract
   *       (Issue #10: 语文词语表 vs English word list).
   *   submitWords(payload) → Promise<{added, skipped}> — POST /api/v1/words
   *   fetchWords(category) → Promise<Array> — GET /api/v1/words?category=
   *       (the classification snapshot; ALSO re-fetched after commit to
   *       reconcile raced duplicates honestly AND to resolve card ids for
   *       the textbook assignments)
   *   textbookDeps — {fetchTextbooks, createTextbook, updateTextbook,
   *       assignWords}, the Issue #4 surfaces reused as-is and travelling
   *       as ONE all-or-nothing set (parent-app wires them together;
   *       absent or partial ⇒ the textbook panel degrades to the plain
   *       Issue #3 supplementary flow)
   *   submitPoems(payload) → Promise<{results, added, existing}> —
   *       POST /api/v1/poems (Issue #11): the confirmed whole poems. An
   *       absent handler ⇒ the poem cards' commit affordance degrades
   *       with its reason (the 词语 flow keeps working — poems are an
   *       ADDITIVE surface on the same mixed page).
   *   fetchPoems(category) → Promise<Array> — GET /api/v1/poems?category=
   *       (the poem library snapshot the draft classification reads)
   *   submitPassages(payload) → Promise<{results, added, existing}> —
   *       POST /api/v1/passages (Issue #13): the confirmed whole
   *       passages WITH their chosen practice ranges. An absent handler ⇒
   *       the passage cards' commit affordance degrades with its reason
   *       (the 词语/古诗 flows keep working — passages are another
   *       ADDITIVE surface on the same mixed page).
   *   fetchPassages(category) → Promise<Array> — GET /api/v1/passages?
   *       category= (the passage library snapshot the draft
   *       classification reads)
   *   onCommitted(result) — fired after a successful commit (refresh hook)
   *   onCategoryChange(v) — fired when the parent picks a subject (the
   *       D-06 radio has no pre-selection) so the shared 单元 datalist
   *       populates for that subject
   */
  function PhotoAddForm(container, options) {
    options = options || {};
    this.recognizePhoto = options.recognizePhoto || function () {
      return Promise.reject(new Error('PhotoAddForm: missing recognizePhoto handler'));
    };
    this.submitWords = options.submitWords || function () {
      return Promise.reject(new Error('PhotoAddForm: missing submitWords handler'));
    };
    this.fetchWords = options.fetchWords || function () {
      return Promise.reject(new Error('PhotoAddForm: missing fetchWords handler'));
    };
    // Issue #11: the whole-poem commit + snapshot surfaces (optional; a
    // degraded wiring disables the poem commit with its reason, never the
    // 词语 flow).
    this.submitPoems = options.submitPoems || null;
    this.fetchPoems = options.fetchPoems || null;
    // Issue #13: the whole-passage commit + snapshot surfaces (optional;
    // a degraded wiring disables the passage commit with its reason,
    // never the 词语/古诗 flows).
    this.submitPassages = options.submitPassages || null;
    this.fetchPassages = options.fetchPassages || null;
    var td = options.textbookDeps || {};
    this.fetchTextbooks = td.fetchTextbooks || null;
    this.createTextbook = td.createTextbook || null;
    this.updateTextbook = td.updateTextbook || null;
    this.assignWords = td.assignWords || null;
    // Issue #6: the 采用照片释义 resolution rides the ordinary edit path
    // (PUT /words/{id} — text changes never touch FSRS state).
    this.editWord = td.editWord || null;
    // Issue #8: POST /api/v1/words/meaning/generate — absent ⇒ the
    // supplement affordance never mounts (the preview flow is unchanged).
    this.generateMeanings = options.generateMeanings || null;
    this.onCommitted = options.onCommitted || function () {};
    this.onCategoryChange = options.onCategoryChange || function () {};

    var kit = global.__parentFormKit || {};

    this.form = document.createElement('form');
    this.form.className = 'photo-add-form';
    this.form.noValidate = true;

    // Issue #10: the SAME D-06 category radio the manual add forms use —
    // one entry point recognizes either subject's list; the selection
    // rides the recognize call (?category=), the classification snapshot,
    // the commit and the textbook attribution. Without the shared kit the
    // tab cannot mount an honest selector, so it falls back to the old
    // closed note rather than a fake default.
    this.categoryKit = kit.buildCategoryField
      ? kit.buildCategoryField('parent-category-photo', this.onCategorySelected.bind(this))
      : null;
    if (this.categoryKit) {
      this.form.appendChild(this.categoryKit.field);
    } else {
      var catField = document.createElement('div');
      catField.className = 'form-field';
      var catLabel = document.createElement('span');
      catLabel.className = 'form-label';
      catLabel.textContent = '分类';
      var catNote = document.createElement('p');
      catNote.className = 'form-hint';
      catNote.textContent = '分类组件未加载，请刷新页面';
      catField.appendChild(catLabel);
      catField.appendChild(catNote);
      this.form.appendChild(catField);
    }

    // 单元 — same shared-datalist field as the other add forms.
    var unit = kit.buildUnitField ? kit.buildUnitField('parent-unit-photo') : null;
    this.unitInput = unit ? unit.input : null;
    if (unit) {
      this.form.appendChild(unit.field);
    }

    // Photo/PDF picker row: one file at a time (Issue #3 settles the
    // single-file limit; Issue #7 adds the scanned PDF — pages split in
    // the browser, ADR-0004). The .pdf extension rides accept because
    // iPad Safari sometimes hands a PDF an empty MIME type.
    var pickField = document.createElement('div');
    pickField.className = 'form-field';
    var pickLabel = document.createElement('label');
    pickLabel.className = 'form-label';
    pickLabel.textContent = '词表照片／扫描 PDF';
    var pickHint = document.createElement('p');
    pickHint.className = 'form-hint';
    pickHint.textContent = '拍摄或选择一张英语单词表或语文词语表照片（PNG / JPEG），或整本扫描 PDF（最多 100 页），无需裁图。';
    var pickRow = document.createElement('div');
    pickRow.className = 'photo-pick-row';
    this.fileInput = document.createElement('input');
    this.fileInput.type = 'file';
    this.fileInput.accept = 'image/png,image/jpeg,application/pdf,.pdf';
    this.fileInput.className = 'photo-file-input';
    this.fileInput.id = 'parent-photo-file';
    this.pickBtn = document.createElement('button');
    this.pickBtn.type = 'button';
    this.pickBtn.className = 'btn btn-secondary';
    this.pickBtn.textContent = '选择照片';
    this.fileMeta = document.createElement('span');
    this.fileMeta.className = 'photo-file-meta';
    pickRow.appendChild(this.fileInput);
    pickRow.appendChild(this.pickBtn);
    pickRow.appendChild(this.fileMeta);
    pickField.appendChild(pickLabel);
    pickField.appendChild(pickHint);
    pickField.appendChild(pickRow);

    this.thumb = document.createElement('img');
    this.thumb.className = 'photo-thumb';
    this.thumb.alt = '词表照片预览';
    this.thumb.hidden = true;
    pickField.appendChild(this.thumb);
    this.form.appendChild(pickField);

    // Recognize (secondary — the accent stays on 确认添加).
    this.recognizeBtn = kit.buildLoadingButton
      ? kit.buildLoadingButton('开始识别')
      : this.buildFallbackButton('开始识别');
    this.recognizeBtn.type = 'button';
    this.recognizeBtn.disabled = true;
    this.form.appendChild(this.recognizeBtn);

    // Issue #10: the form's OWN error surface carries an id — the panel now
    // also holds the shared category radio's inline error (p.form-error,
    // always present), so a scoped selector is the only unambiguous way to
    // address THIS element from tests and future code.
    this.formError = document.createElement('p');
    this.formError.id = 'parent-photo-form-error';
    this.formError.className = 'form-error';
    this.formError.setAttribute('aria-live', 'polite');
    this.formError.hidden = true;
    this.form.appendChild(this.formError);

    // ------------------------------------------------------------------
    // Issue #81 — the A 并排核对 workspace. Hidden until a file is picked
    // (before that the compact pick form above is the whole tab); the form
    // carries photo-workspace-open / photo-workspace-active classes so CSS
    // can drop the small thumbnail once the large 本次材料 view shows.
    //   state line (教材 · 进度 · 本批词数 · 本批课次 + honest hint)
    //   grid:  LEFT  材料与学习安排 (photo/PDF page + status + 教材归属
    //          panel + 释义补充 bar)   RIGHT  词条核对 (table, poem/
    //          passage/other cards, footer actions)
    // Narrow screens: one column in the same task order.
    // ------------------------------------------------------------------
    this.workspace = document.createElement('div');
    this.workspace.className = 'photo-workspace';
    this.workspace.hidden = true;

    this.stateLine = document.createElement('div');
    this.stateLine.className = 'photo-state-line';
    // Deliberately NOT an aria-live region: it rebuilds on every keystroke
    // (a polite announcer would chatter); the commit banner already owns
    // the role=status announcements.
    this.stateLine.hidden = true;
    this.workspace.appendChild(this.stateLine);

    var grid = document.createElement('div');
    grid.className = 'photo-workspace-grid';

    var sourceCol = document.createElement('aside');
    sourceCol.className = 'photo-col-source';
    sourceCol.setAttribute('aria-label', '材料与学习安排');
    var sourceInner = document.createElement('div');
    sourceInner.className = 'photo-col-source-inner';
    sourceCol.appendChild(sourceInner);

    var sourceCard = document.createElement('section');
    sourceCard.className = 'photo-source-card';
    var sourceLabel = document.createElement('h4');
    sourceLabel.className = 'photo-source-label';
    sourceLabel.textContent = '本次材料';
    sourceCard.appendChild(sourceLabel);
    this.sourceView = document.createElement('div');
    this.sourceView.className = 'photo-source-view';
    this.sourceView.hidden = true;
    this.sourceImg = document.createElement('img');
    this.sourceImg.className = 'photo-source-img';
    this.sourceImg.alt = '词表照片大图预览';
    this.sourceView.appendChild(this.sourceImg);
    sourceCard.appendChild(this.sourceView);
    // The caption + zoom row sits OUTSIDE the image frame: a picked PDF
    // has its name here from the moment of selection, before any page
    // render exists.
    var sourceFoot = document.createElement('div');
    sourceFoot.className = 'photo-source-foot';
    this.sourceCaption = document.createElement('span');
    this.sourceCaption.className = 'photo-source-caption';
    this.zoomBtn = document.createElement('button');
    this.zoomBtn.type = 'button';
    this.zoomBtn.className = 'photo-source-zoom-btn';
    this.zoomBtn.textContent = '放大查看';
    this.zoomBtn.hidden = true;
    sourceFoot.appendChild(this.sourceCaption);
    sourceFoot.appendChild(this.zoomBtn);
    sourceCard.appendChild(sourceFoot);
    var sourceNote = document.createElement('p');
    sourceNote.className = 'photo-source-note';
    sourceNote.textContent = '原图只在本机预览，用于对照核对；核对时可随时放大。';
    sourceCard.appendChild(sourceNote);
    sourceInner.appendChild(sourceCard);

    // Per-page status bar (Issue #7): one chip per PDF page (页码 + 状态，
    // failed pages carry a 重试该页 button) plus the whole-file summary
    // and a 停止 button while a run is in flight. Hidden for photos.
    // Issue #81: it rides the 本次材料 column, right under the page render.
    this.pdfBar = document.createElement('div');
    this.pdfBar.className = 'pdf-status-bar';
    this.pdfBar.hidden = true;
    sourceInner.appendChild(this.pdfBar);

    // Textbook attribution panel (Issue #5): the 学习安排 half of the left
    // column once a recognition exists; hidden until then.
    this.textbookPanel = document.createElement('div');
    this.textbookPanel.className = 'photo-textbook-panel';
    this.textbookPanel.hidden = true;
    sourceInner.appendChild(this.textbookPanel);

    // Supplement affordance (Issue #8): one strip under the textbook panel
    // — 补充缺失释义（N 词）/ 重试生成（N 词） plus the honesty note when
    // words went unanswered. Rendered by renderMeaningBar; hidden until a
    // recognition with a confirmed textbook context exists.
    this.meaningBar = document.createElement('div');
    this.meaningBar.className = 'photo-meaning-bar';
    this.meaningBar.hidden = true;
    sourceInner.appendChild(this.meaningBar);

    grid.appendChild(sourceCol);

    var reviewCol = document.createElement('section');
    reviewCol.className = 'photo-col-review';
    reviewCol.setAttribute('aria-label', '词条核对');
    var reviewHead = document.createElement('div');
    reviewHead.className = 'photo-review-head';
    var reviewTitle = document.createElement('h4');
    reviewTitle.className = 'photo-review-title';
    reviewTitle.textContent = '词条核对';
    var reviewNote = document.createElement('p');
    reviewNote.className = 'photo-review-note';
    reviewNote.textContent = '对照原图，直接修改单词或释义，确认后一次保存。';
    reviewHead.appendChild(reviewTitle);
    reviewHead.appendChild(reviewNote);
    reviewCol.appendChild(reviewHead);

    // Review table: rendered after recognition; every row editable.
    this.preview = document.createElement('div');
    this.preview.className = 'preview-scroll';
    this.previewHint = document.createElement('p');
    this.previewHint.className = 'preview-empty';
    this.previewHint.textContent = '选择照片或 PDF 后点「开始识别」';
    this.preview.appendChild(this.previewHint);
    reviewCol.appendChild(this.preview);

    // Issue #11: the mixed-page poem cards and the display-only others
    // render as their own sections right under the 词语 table (a poem is
    // one whole work, not a table row; others never join the commit).
    // Issue #13: the passage cards sit between them — one whole original
    // with its 自然段 and the practice-range picker, never table rows.
    // Issue #81: they stay in the 词条核对 column, below the table.
    this.poemHost = document.createElement('div');
    this.poemHost.className = 'photo-poem-host';
    this.poemHost.hidden = true;
    reviewCol.appendChild(this.poemHost);
    this.passageHost = document.createElement('div');
    this.passageHost.className = 'photo-passage-host';
    this.passageHost.hidden = true;
    reviewCol.appendChild(this.passageHost);
    this.othersHost = document.createElement('div');
    this.othersHost.className = 'photo-others-host';
    this.othersHost.hidden = true;
    reviewCol.appendChild(this.othersHost);

    // 确认添加（{n} 个） + 放弃识别结果, with the draft's save-note above.
    var actions = document.createElement('div');
    actions.className = 'photo-actions';
    this.commitHint = document.createElement('p');
    this.commitHint.className = 'photo-commit-hint';
    this.commitHint.hidden = true;
    this.commitBtn = kit.buildLoadingButton
      ? kit.buildLoadingButton('确认添加（0 个）')
      : this.buildFallbackButton('确认添加（0 个）');
    this.commitBtn.disabled = true;
    this.discardBtn = document.createElement('button');
    this.discardBtn.type = 'button';
    this.discardBtn.className = 'btn btn-secondary';
    this.discardBtn.textContent = '放弃识别结果';
    this.discardBtn.hidden = true;
    actions.appendChild(this.commitHint);
    actions.appendChild(this.commitBtn);
    actions.appendChild(this.discardBtn);
    reviewCol.appendChild(actions);

    grid.appendChild(reviewCol);
    this.workspace.appendChild(grid);
    this.form.appendChild(this.workspace);

    // Result banner host (rendered after each commit).
    this.banner = document.createElement('div');
    this.form.appendChild(this.banner);

    // State.
    this.currentFile = null;
    this.rows = []; // current classified rows (source of truth on edit)
    this.existingWords = [];
    // Issue #11: the mixed-page companions — classified poem drafts
    // (poemRows), the display-only other blocks (otherSections) and the
    // poem library snapshot the classification reads (existingPoems).
    // Issue #13 adds the passage kin: passageRows/existingPassages.
    this.poemRows = [];
    this.otherSections = [];
    this.existingPoems = [];
    this.passageRows = [];
    this.existingPassages = [];
    // Issue #10: the subject the CURRENT draft belongs to, locked at
    // recognize time. Every subject-scoped call after that — snapshot,
    // commit, textbook attribution, reconcile — rides it, so switching
    // the radio mid-flow can never split one draft across two subjects:
    // the switch resets the draft instead (onCategorySelected).
    this.draftCategory = '';
    this.detectedMeta = null; // the READ-off-the-photo textbook header
    this.books = []; // the draft's subject's textbooks (panel list)
    this.bookChoice = 'none'; // 'none' | 'new' | a textbook id (number)
    this.includePending = false; // 待确认课次的词直接加入默写范围
    // Issue #7: while a PDF is in play — { doc, plan, aborted, metaDone,
    // panelRendered }. doc stays open for page retries until discard /
    // commit reset / a new file; rows themselves live in this.rows.
    this.pdfState = null;
    // Issue #8: a supplement run is in flight (the affordance disables
    // and says 生成中 rather than queueing a second overlapping call).
    this.meaningBusy = false;
    // Issue #81: the 本次材料 large view. sourceKind 'photo' shares the
    // picker's object URL (owned by handleFilePicked/clearPickUI);
    // 'pdf' owns this.pdfPageURL (one page render at a time, revoked on
    // every swap) and this.sourcePageNo (which page the caption tracks).
    this.sourceKind = '';
    this.pdfPageURL = null;
    this.sourcePageNo = 0;

    var self = this;
    this.pickBtn.addEventListener('click', function () {
      self.fileInput.click();
    });
    this.fileInput.addEventListener('change', function () {
      self.handleFilePicked();
    });
    this.recognizeBtn.addEventListener('click', function () {
      self.handleRecognize();
    });
    this.commitBtn.addEventListener('click', function (event) {
      event.preventDefault();
      self.handleCommit();
    });
    this.discardBtn.addEventListener('click', function () {
      self.handleDiscard();
    });
    // Issue #81: the 放大查看 toggle (the image itself taps too — the
    // button is the focusable affordance, never a hover-only trick).
    this.zoomBtn.addEventListener('click', function () {
      self.toggleSourceZoom();
    });
    this.sourceImg.addEventListener('click', function () {
      self.toggleSourceZoom();
    });

    container.appendChild(this.form);

    // No mount-time category: the D-06 radio has NO pre-selection (the
    // single/batch forms' shape) — the unit datalist populates when the
    // parent picks a subject, via onCategorySelected.
  }

  /**
   * onCategorySelected (Issue #10) — the D-06 radio's change hook: the
   * shared 单元 datalist re-populates for the new subject, and a draft
   * recognized under the OTHER subject resets — a preview's rows,
   * textbook panel and meta were classified against the other subject's
   * library, so keeping them would straddle subjects at commit. The
   * category itself stays selected (the parent just switched to it).
   */
  PhotoAddForm.prototype.onCategorySelected = function (category) {
    this.onCategoryChange(category);
    if (this.draftCategory && category !== this.draftCategory) {
      // The draft belongs to the OTHER subject: resetEntries clears the
      // rows/panel state; the snapshot, book list and supplement bar are
      // the other subject's too. currentFile STAYS — the photo itself is
      // subject-agnostic, 重新识别 under the new subject is the retry.
      // An in-flight PDF run is STOPPED first (releasePdf alone does not
      // signal the page loop — without the abort flag the loop would keep
      // rendering pages of the closed document under the new subject).
      if (this.pdfState) {
        this.pdfState.aborted = true;
      }
      this.draftCategory = category;
      this.existingWords = [];
      this.existingPoems = [];
      this.existingPassages = [];
      this.books = [];
      this.releasePdf();
      this.meaningBar.hidden = true;
      this.resetEntries();
      this.renderPreviewHint('分类已切换，请重新点「开始识别」');
      this.updateCommitButton();
      return;
    }
    this.draftCategory = category;
  };

  /** Button builder used only when the parent-modes kit is unavailable. */
  PhotoAddForm.prototype.buildFallbackButton = function (labelText) {
    var btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'btn btn-primary';
    btn.textContent = labelText;
    return btn;
  };

  /** Set the inline form error (empty string hides it). */
  PhotoAddForm.prototype.showFormError = function (message) {
    this.formError.textContent = message;
    this.formError.hidden = !message;
  };

  /** Clear the picked-file UI (meta line, thumbnail, button state). */
  PhotoAddForm.prototype.clearPickUI = function () {
    this.fileMeta.textContent = '';
    this.thumb.hidden = true;
    this.thumb.removeAttribute('src');
    this.recognizeBtn.disabled = true;
    // Issue #81: the large 本次材料 view mirrors the pick — drop it here
    // (the photo object URL's own revoke stays handleFilePicked's job).
    if (this.sourceKind === 'photo') {
      this.sourceKind = '';
      this.sourceImg.removeAttribute('src');
      this.sourceView.hidden = true;
      this.setSourceCaption('');
    }
    this.syncWorkspace();
  };

  PhotoAddForm.prototype.clearPdfSource = function () {
    if (this.pdfPageURL) {
      URL.revokeObjectURL(this.pdfPageURL);
      this.pdfPageURL = null;
    }
    if (this.sourceKind === 'pdf') {
      this.sourceKind = '';
      this.sourcePageNo = 0;
      this.sourceImg.removeAttribute('src');
      this.sourceView.hidden = true;
      // The picked PDF's name stays the caption while the file is in play.
      this.setSourceCaption(this.currentFile ? this.currentFile.name : '');
    }
  };

  /**
   * showPdfPageBlob (Issue #81) — feed the left column's 本次材料 view with
   * the page PdfPageRunner just rendered: one object URL at a time (the
   * previous page's is revoked on the swap), caption 第 N 页. The blob is
   * the SAME one the recognize call gets; the draft stays client-side.
   */
  PhotoAddForm.prototype.showPdfPageBlob = function (blob, pageNo) {
    if (this.pdfPageURL) {
      URL.revokeObjectURL(this.pdfPageURL);
    }
    this.pdfPageURL = URL.createObjectURL(blob);
    this.sourceKind = 'pdf';
    this.sourcePageNo = pageNo;
    this.sourceImg.alt = '第 ' + pageNo + ' 页大图预览';
    this.sourceImg.src = this.pdfPageURL;
    this.sourceView.hidden = false;
    this.setSourceCaption('第 ' + pageNo + ' 页 · 识别中…');
    this.syncWorkspace();
  };

  PhotoAddForm.prototype.setSourceCaption = function (text) {
    this.sourceCaption.textContent = text;
  };

  /**
   * toggleSourceZoom (Issue #81) — the CSS zoom affordance: 放大查看 drops
   * the max-height cap so a dense word list becomes readable beside the
   * review; 还原 puts the compact frame back.
   */
  PhotoAddForm.prototype.toggleSourceZoom = function () {
    if (this.sourceView.hidden) {
      return;
    }
    var zoomed = this.sourceView.classList.toggle('photo-source-zoomed');
    this.zoomBtn.textContent = zoomed ? '还原' : '放大查看';
  };

  /**
   * syncWorkspace (Issue #81) — ONE owner of the workspace visibility:
   * open once a file is picked, a PDF run is in play, or a draft exists;
   * the ACTIVE class (draft) additionally hides the small picker
   * thumbnail — the large 本次材料 view has taken over.
   */
  PhotoAddForm.prototype.syncWorkspace = function () {
    var hasDraft = !!(this.rows.length || this.poemRows.length || this.passageRows.length);
    var open = !!(this.currentFile || this.pdfState || hasDraft);
    this.workspace.hidden = !open;
    this.form.classList.toggle('photo-workspace-open', open);
    this.form.classList.toggle('photo-workspace-active', hasDraft);
    // The zoom toggle only makes sense over a live image.
    this.zoomBtn.hidden = this.sourceView.hidden;
    if (this.sourceView.hidden) {
      this.sourceView.classList.remove('photo-source-zoomed');
      this.zoomBtn.textContent = '放大查看';
    }
  };

  /**
   * draftLessonNames — the distinct non-empty lessons across EVERY content
   * kind of the draft (words, poems, passages), in appearance order. The
   * state line's 本批课次 fact and the progress verdict read this.
   */
  PhotoAddForm.prototype.draftLessonNames = function () {
    var pool = this.rows.concat(this.poemRows).concat(this.passageRows);
    return distinctRowLessons(pool);
  };

  /**
   * schedulePreview — the state line / save-note's honest pre-commit
   * estimate of what the commit will be able to schedule (the RECEIPT
   * re-derives the truth from the actual assign results — this is the
   * informational twin). Verdict per lesson name follows the server's
   * scope rule: 已学 = 在课次表内且位置 ≤ 已学完第 N 课（含）; 课表外 and
   * 待确认 never count as learned; 加入范围 (the include checkbox) lifts
   * the 待确认 group; 补充词汇 (no textbook) default in.
   */
  PhotoAddForm.prototype.schedulePreview = function () {
    var mode = this.bookChoice;
    var rows = mode === 'none' ? this.submittableRows() : this.committedRows();
    var book = (mode !== 'none' && mode !== 'new') ? this.chosenBook() : null;
    var lessons = (book && book.lessons) || [];
    var learned = (book && book.learnedLessons) || 0;
    var out = {
      mode: mode, book: book, learned: learned, total: lessons.length,
      schedulable: 0, joined: 0, heldUnlearned: 0, heldPending: 0,
      unlearnedNames: [], withinNames: [],
    };
    rows.forEach(function (r) {
      var lesson = metaField(r.lesson);
      if (mode === 'none') {
        out.joined += 1; // 补充词汇默认加入范围
        return;
      }
      if (!lesson) {
        if (this.includePending) {
          out.joined += 1;
        } else {
          out.heldPending += 1;
        }
        return;
      }
      var idx = lessons.indexOf(lesson);
      if (idx >= 0 && idx + 1 <= learned) {
        out.schedulable += 1;
        if (out.withinNames.indexOf(lesson) < 0) {
          out.withinNames.push(lesson);
        }
      } else {
        out.heldUnlearned += 1;
        if (out.unlearnedNames.indexOf(lesson) < 0) {
          out.unlearnedNames.push(lesson);
        }
      }
    }, this);
    return out;
  };

  /**
   * updateStateLine — the compact facts above the workspace: 教材 · 已学
   * 进度 · 本批词数 · 本批课次, plus ONE honest scheduling note. Known
   * progress inside the batch is stated as a fact (「已学到，保存后即可安排
   * 默写」) — never a question; exceeding/unknown progress says 暂不自动
   * 安排. Pure text (no inputs) so it is safe to rebuild on every edit.
   */
  PhotoAddForm.prototype.updateStateLine = function () {
    var hasDraft = !!(this.rows.length || this.poemRows.length || this.passageRows.length);
    if (!hasDraft) {
      this.stateLine.hidden = true;
      clearEl(this.stateLine);
      return;
    }
    this.stateLine.hidden = false;
    clearEl(this.stateLine);

    var sched = this.schedulePreview();
    var bookName = '';
    if (sched.mode === 'none') {
      bookName = '未指定（补充词汇）';
    } else if (sched.mode === 'new') {
      bookName = '新建教材' + (this.readCreateForm().name ? '「' + this.readCreateForm().name + '」' : '');
    } else {
      bookName = sched.book ? sched.book.name : '已选教材';
    }
    var self = this;
    var addFact = function (label, value) {
      var fact = document.createElement('span');
      fact.className = 'photo-state-fact';
      var lab = document.createElement('span');
      lab.textContent = label;
      var val = document.createElement('strong');
      val.textContent = value;
      fact.appendChild(lab);
      fact.appendChild(val);
      self.stateLine.appendChild(fact);
    };

    addFact('教材', bookName);
    if (sched.mode !== 'none' && sched.book) {
      addFact('进度', global.progressLabel
        ? global.progressLabel(sched.book)
        : (sched.learned ? '已学完第 ' + sched.learned + ' 课' : '尚未登记'));
    }
    var count = this.rows.length + ' 词';
    if (this.poemRows.length) {
      count += ' · ' + this.poemRows.length + ' 首古诗';
    }
    if (this.passageRows.length) {
      count += ' · ' + this.passageRows.length + ' 篇课文';
    }
    addFact('本批', count);
    var lessons = this.draftLessonNames();
    addFact('课次', lessons.length ? lessons.join('、') : '全部待确认');

    var hint = document.createElement('p');
    hint.className = 'photo-state-hint';
    var verdict = scheduleHint(sched, this.rows.length > 0);
    hint.className += ' photo-state-hint-' + verdict.tone;
    hint.textContent = verdict.text;
    this.stateLine.appendChild(hint);
    if (sched.heldPending && sched.mode !== 'none') {
      var pend = document.createElement('p');
      pend.className = 'photo-state-hint photo-state-hint-neutral';
      pend.textContent = sched.includePending
        ? '待确认课次的 ' + sched.heldPending + ' 词已勾选直接加入默写范围。'
        : '待确认课次的 ' + sched.heldPending + ' 词暂不自动安排，可指定课次或勾选直接加入范围。';
      this.stateLine.appendChild(pend);
    }
  };

  /**
   * scheduleHint — the state line's ONE scheduling note as a pure
   * verdict {tone, text} over schedulePreview's counts (updateStateLine
   * renders it; window-exposed for the Node tests). Branch order mirrors
   * schedulePreview: none/new first, then progress verdicts; the
   * direct-join branch (P2) must fire BEFORE the all-unchecked note —
   * joined > 0 means a save WILL land words in the scope, so 「全部未
   * 勾选……不会保存」 must never contradict the footer's 「N 词将加入
   * 默写范围」.
   */
  function scheduleHint(sched, hasRows) {
    if (sched.mode === 'none') {
      return { tone: 'good', text: '补充词汇保存后默认加入默写范围。' };
    }
    if (sched.mode === 'new' || (sched.book && !sched.learned)) {
      return {
        tone: 'warn',
        text: '这本教材还没有已学进度：保存后暂不自动安排默写，之后在「教材管理」登记进度即可。',
      };
    }
    if (sched.schedulable && !sched.heldUnlearned && !sched.heldPending) {
      return {
        tone: 'good',
        text: '课次' + quoteList(sched.withinNames) + '已学到，保存后即可安排默写，无需再确认进度。',
      };
    }
    if (sched.heldUnlearned) {
      var text = '课次' + quoteList(sched.unlearnedNames) +
        '尚未学到：保存后这部分词暂不自动安排默写，进度不受影响。';
      if (sched.schedulable) {
        text += '其余 ' + sched.schedulable + ' 词课次已学，保存后即可安排。';
      }
      return { tone: 'warn', text: text };
    }
    if (sched.schedulable) {
      return {
        tone: 'good',
        text: '课次' + quoteList(sched.withinNames) + '已学到，保存后即可安排默写；其余词课次待确认。',
      };
    }
    if (sched.heldPending) {
      return { tone: 'neutral', text: '本批词全部待确认课次，保存后暂不自动安排默写。' };
    }
    if (sched.joined) {
      // Checked 待确认 words lifted by the page-level 直接加入默写范围
      // checkbox: they save INTO the scope, so the all-unchecked error
      // below must not fire (P2: the state line once claimed 全部未勾选
      // while the footer promised N 词将加入默写范围).
      return {
        tone: 'good',
        text: '待确认课次的 ' + sched.joined + ' 词已勾选直接加入默写范围，保存后即可安排默写。',
      };
    }
    if (hasRows) {
      // Every count zero with word rows on display: they are all unchecked.
      return {
        tone: 'neutral',
        text: '本批词语条目全部未勾选——取消勾选的内容不会保存。',
      };
    }
    // Poems/passages only: their own scope notes cover them.
    return {
      tone: 'neutral',
      text: '本批为整篇内容（古诗/课文），学习范围随教材已学进度生效。',
    };
  }

  /** 「A」「B」 lesson-name quoting for the state line / save note. */
  function quoteList(names) {
    return names.map(function (n) { return '「' + n + '」'; }).join('、');
  }

  /**
   * updateCommitHint — the footer's save-note (the prototype's one-line
   * 保存后… note): what the commit WILL do for scheduling, stated before
   * the parent presses 确认添加. Informational only.
   */
  PhotoAddForm.prototype.updateCommitHint = function () {
    var hasDraft = !!(this.rows.length || this.poemRows.length || this.passageRows.length);
    this.commitHint.hidden = !hasDraft;
    if (!hasDraft) {
      this.commitHint.textContent = '';
      return;
    }
    var sched = this.schedulePreview();
    var parts = [];
    if (sched.schedulable) {
      parts.push(sched.schedulable + ' 词可安排默写（课次已学）');
    }
    if (sched.joined && sched.mode === 'none') {
      parts.push(sched.joined + ' 词将加入默写范围（补充词汇）');
    } else if (sched.joined) {
      parts.push(sched.joined + ' 词将加入默写范围（已勾选直接加入）');
    }
    if (sched.heldUnlearned) {
      parts.push(sched.heldUnlearned + ' 词暂不自动安排（课次' + quoteList(sched.unlearnedNames) + '尚未学到）');
    }
    if (sched.heldPending) {
      parts.push(sched.heldPending + ' 词暂不自动安排（课次待确认）');
    }
    // Nothing to say (poems/passages only, or nothing checked): the note
    // hides rather than stating a vacuous scheduling promise.
    this.commitHint.hidden = parts.length === 0;
    this.commitHint.textContent = parts.length
      ? '保存后：' + parts.join('；') + '。'
      : '';
  };

  PhotoAddForm.prototype.handleFilePicked = function () {
    var file = this.fileInput.files && this.fileInput.files[0];
    this.showFormError('');
    this.currentFile = file || null;
    if (!file) {
      this.clearPickUI();
      this.releasePdf();
      return;
    }
    var verdict = (global.validatePdfFile || function () {
      return { ok: false, code: 'not-pdf' };
    })(file);
    if (verdict.ok) {
      // Issue #7: a scanned PDF. No thumbnail (a PDF is not an <img>); the
      // page plan replaces it once 开始识别 runs. A new file always
      // releases the previous run's doc — page retries never outlive it.
      this.releasePdf();
      this.fileMeta.textContent = file.name + '（' +
        Math.max(1, Math.round(file.size / (1024 * 1024))) + ' MB）';
      this.thumb.hidden = true;
      this.thumb.removeAttribute('src');
      // Issue #81: a PDF replaces any photo source view — the page renders
      // will take the 本次材料 column once 开始识别 runs.
      if (this.sourceKind === 'photo') {
        this.sourceKind = '';
        this.sourceImg.removeAttribute('src');
        this.sourceView.hidden = true;
      }
      if (!(global.PdfPageRunner && global.PdfPageRunner.isAvailable())) {
        this.recognizeBtn.disabled = true;
        this.showFormError('PDF 支持未加载，请刷新页面或改用照片导入');
        this.syncWorkspace();
        return;
      }
      this.recognizeBtn.disabled = false;
      // Issue #81: the left column fills with page renders once 开始识别
      // runs; until then the status bar carries the file's story.
      this.setSourceCaption(file.name);
      this.syncWorkspace();
      return;
    }
    if (verdict.code !== 'not-pdf') {
      // pdf-too-large: refuse BEFORE loading, nothing is processed (AC-3).
      this.currentFile = null;
      this.clearPickUI();
      this.showFormError(verdict.message);
      return;
    }
    if (file.type !== 'image/png' && file.type !== 'image/jpeg') {
      this.currentFile = null;
      this.clearPickUI();
      this.showFormError('只支持 PNG / JPEG 照片或 PDF 扫描件，请换一个文件');
      return;
    }
    this.fileMeta.textContent = file.name + '（' + Math.max(1, Math.round(file.size / 1024)) + ' KB）';
    if (this.thumb.src) {
      URL.revokeObjectURL(this.thumb.src);
    }
    this.thumb.src = URL.createObjectURL(file);
    this.thumb.hidden = false;
    this.recognizeBtn.disabled = false;
    // Issue #81: the SAME object URL feeds the large 本次材料 view — the
    // parent reads the original photo beside the entries from the moment
    // the file is picked, before recognition even runs.
    this.clearPdfSource();
    this.sourceKind = 'photo';
    this.sourceImg.alt = '词表照片大图预览';
    this.sourceImg.src = this.thumb.src;
    this.sourceView.hidden = false;
    this.setSourceCaption(file.name);
    this.syncWorkspace();
  };

  /**
   * releasePdf — drop the current PDF run: destroy the pdf.js document
   * (frees the worker-side page data), clear the plan/bar. The ROWS stay
   * (a freshly picked file keeps the old preview on screen exactly like
   * the photo flow does — 开始识别 replaces it).
   */
  PhotoAddForm.prototype.releasePdf = function () {
    if (this.pdfState && this.pdfState.doc && global.PdfPageRunner) {
      global.PdfPageRunner.close(this.pdfState.doc);
    }
    this.pdfState = null;
    // Issue #81: the page render dies with the document — the left column
    // reverts to the photo (if one is still picked) or the empty note.
    this.clearPdfSource();
    this.renderPdfBar();
    this.syncWorkspace();
  };

  /**
   * Prepare the upload bytes: a PNG/JPEG already under the cap ships
   * verbatim (preserves PNGs); anything bigger walks the canvas ladder and
   * re-encodes as JPEG. Resolves null when even the last rung stays over
   * the cap (the honest too-large outcome — nothing is uploaded).
   */
  PhotoAddForm.prototype.prepareUpload = function (file) {
    if (file.size <= MAX_UPLOAD_BYTES) {
      return Promise.resolve(file);
    }
    return loadImageFromFile(file)
      .then(function (img) {
        var canvas = document.createElement('canvas');
        var ladderPromise = Promise.resolve(null);
        QUALITY_LADDER.forEach(function (step) {
          ladderPromise = ladderPromise.then(function (blob) {
            if (blob) {
              return blob;
            }
            var scale = Math.min(1, step.edge / Math.max(img.width, img.height));
            canvas.width = Math.max(1, Math.round(img.width * scale));
            canvas.height = Math.max(1, Math.round(img.height * scale));
            var ctx = canvas.getContext('2d');
            ctx.fillStyle = '#ffffff'; // paper photos are white; PNG alpha must not go black
            ctx.fillRect(0, 0, canvas.width, canvas.height);
            ctx.drawImage(img, 0, 0, canvas.width, canvas.height);
            return canvasToJpegBlob(canvas, step.quality).then(function (blob) {
              return blob && blob.size <= MAX_UPLOAD_BYTES ? blob : null;
            });
          });
        });
        return ladderPromise;
      });
  };

  PhotoAddForm.prototype.handleRecognize = function () {
    if (this.recognizeBtn.disabled || !this.currentFile) {
      return;
    }
    // Issue #10: the subject is REQUIRED before any vision cost — the
    // same 请选择分类 gate the manual add forms apply at submit.
    var category = this.categoryKit ? this.categoryKit.selected() : '';
    if (!category) {
      if (this.categoryKit) {
        this.categoryKit.setError('请选择分类');
      }
      return;
    }
    this.draftCategory = category;
    if (this.isPickedPdf()) {
      this.handlePdfRecognize();
      return;
    }
    var self = this;
    var kit = global.__parentFormKit || {};
    this.showFormError('');
    this.setBusy(true);

    var uploadBytes = null;
    this.prepareUpload(this.currentFile)
      .then(function (bytes) {
        if (!bytes) {
          throw new Error('照片压缩后仍然太大，请换一张更小的照片');
        }
        uploadBytes = bytes;
        return self.recognizePhoto(bytes, category);
      })
      .then(function (data) {
        // Issue #10 race guard: a radio switch mid-flight reset the draft
        // under the OTHER subject — a late landing would repopulate the
        // preview rows against a classification snapshot that no longer
        // matches the draft's subject. Drop it; the switch hint already
        // told the parent to 重新识别.
        if (self.draftCategory !== category) {
          self.setBusy(false);
          return;
        }
        var parsed = parseRecognized(data);
        var entries = parsed.entries;
        var meta = parsed.meta;
        // Issue #11/#13: a mixed 语文 page may carry ONLY poems or ONLY
        // passages (entries empty) — that is a legal recognition, not a
        // 没有识别到词表 dead end.
        if (!entries.length && !parsed.poems.length && !parsed.passages.length) {
          self.setBusy(false);
          self.renderPreviewHint('这张照片里没有识别到词表，请换一张更清晰的照片');
          self.resetEntries();
          return;
        }
        self.detectedMeta = meta;
        self.bookChoice = 'none';
        self.includePending = false;
        // Snapshot the REAL libraries for classification; load the textbook
        // list in parallel for the attribution panel. All ride the DRAFT's
        // subject (a Chinese draft classifies against the 语文 word library
        // AND the poem/passage libraries — Issue #10/#11/#13). A
        // textbook-list failure degrades the panel (with its reason),
        // never the preview; a poem/passage snapshot failure degrades that
        // classification to unchecked new drafts (the commit's per-item
        // outcome stays authoritative).
        var booksLoad = self.fetchTextbooks
          ? self.fetchTextbooks(category).catch(function () { return null; })
          : Promise.resolve(null);
        var poemsLoad = self.fetchPoems
          ? self.fetchPoems(category).catch(function () { return null; })
          : Promise.resolve(null);
        var passagesLoad = self.fetchPassages
          ? self.fetchPassages(category).catch(function () { return null; })
          : Promise.resolve(null);
        return Promise.all([
          self.fetchWords(category).catch(function () { return []; }),
          booksLoad,
          poemsLoad,
          passagesLoad,
        ]).then(function (loaded) {
          self.existingWords = loaded[0] || [];
          self.books = loaded[1] || [];
          self.existingPoems = loaded[2] || [];
          self.existingPassages = loaded[3] || [];
          self.rows = classifyPhotoEntries(entries, self.existingWords);
          self.poemRows = classifyPoemDrafts(parsed.poems, self.existingPoems);
          self.passageRows = classifyPassageDrafts(parsed.passages, self.existingPassages);
          self.otherSections = parsed.others.map(function (o) {
            return {
              kind: o && o.kind === 'exercise' ? 'exercise' : 'text',
              summary: metaField(o && o.summary),
              page: 0,
            };
          });
          self.renderTextbookPanel(loaded[1] === null ? '教材信息读取失败，可重试或直接确认添加' : '');
          self.renderPreview();
          self.discardBtn.hidden = false;
        });
      })
      .catch(function (err) {
        var message = (err && err.message) ? err.message : '未知错误';
        // Failure shows the reason and the file stays picked — 重新识别
        // is just clicking 开始识别 again (retry allowed).
        self.showFormError('识别失败：' + message + '。可以重试或换一张照片。');
        if (kit.showToast) {
          kit.showToast('识别失败：' + message, 'error');
        }
      })
      .then(function () {
        self.setBusy(false);
      });
  };

  // ------------------------------------------------------------------
  // Issue #7 — the scanned-PDF run: open → per-page plan → sequential
  // page loop (render → recognize → merge) → per-page status bar.
  // ------------------------------------------------------------------

  /** The picked file is a PDF (validatePdfFile's predicate, one home). */
  PhotoAddForm.prototype.isPickedPdf = function () {
    if (!this.currentFile || !global.validatePdfFile) {
      return false;
    }
    return global.validatePdfFile(this.currentFile).ok;
  };

  /**
   * handlePdfRecognize — open the PDF, plan the pages, run them
   * sequentially. Failures NEVER throw past this method's catch: a page
   * that fails lands on its plan entry and the run continues (AC-4); only
   * open/plan failures (unreadable file, over-cap pages) surface as form
   * errors with nothing processed.
   */
  PhotoAddForm.prototype.handlePdfRecognize = function () {
    var self = this;
    var kit = global.__parentFormKit || {};
    // The DRAFT's subject (locked by handleRecognize before dispatch):
    // every page's recognize call and the one library snapshot ride it.
    var category = this.draftCategory;
    this.showFormError('');
    this.setBusy(true);
    this.releasePdf();

    global.PdfPageRunner.open(this.currentFile)
      .then(function (opened) {
        var plan = global.createPagePlan(opened.pageCount);
        if (!plan.ok) {
          global.PdfPageRunner.close(opened.doc);
          throw new Error(plan.message);
        }
        self.pdfState = {
          doc: opened.doc,
          plan: plan,
          aborted: false,
          metaDone: false,
          panelRendered: false,
        };
        self.renderPdfBar();
        self.syncWorkspace();
        // ONE library snapshot classifies every page as it lands (the run
        // can take minutes; raced duplicates stay honest through the
        // server's UNIQUE + the post-commit reconciliation — the photo
        // flow's precedent, just stretched over pages). Subject-scoped
        // (Issue #10): a 语文 run classifies against the 语文 word library;
        // Issue #11 adds the poem library snapshot beside it, #13 the
        // passage library.
        var booksLoad = self.fetchTextbooks
          ? self.fetchTextbooks(category).catch(function () { return null; })
          : Promise.resolve(null);
        var poemsLoad = self.fetchPoems
          ? self.fetchPoems(category).catch(function () { return null; })
          : Promise.resolve(null);
        var passagesLoad = self.fetchPassages
          ? self.fetchPassages(category).catch(function () { return null; })
          : Promise.resolve(null);
        return Promise.all([
          self.fetchWords(category).catch(function () { return []; }),
          booksLoad,
          poemsLoad,
          passagesLoad,
        ]).then(function (loaded) {
          self.existingWords = loaded[0] || [];
          self.books = loaded[1] || [];
          self.existingPoems = loaded[2] || [];
          self.existingPassages = loaded[3] || [];
          return self.runPdfPages();
        });
      })
      .then(function (summary) {
        self.setBusy(false);
        self.finishPdfRun(summary);
      })
      .catch(function (err) {
        self.setBusy(false);
        var message = (err && err.message) ? err.message : '未知错误';
        self.showFormError('PDF 处理失败：' + message + '。可以重试或换一个文件。');
        if (kit.showToast) {
          kit.showToast('PDF 处理失败：' + message, 'error');
        }
      });
  };

  /**
   * runPdfPages — run every PENDING page of the plan sequentially
   * (onlyPageNo: the single-page retry path). The chain never rejects:
   * per-page failures are recorded on the plan; the loop keeps going.
   */
  PhotoAddForm.prototype.runPdfPages = function (onlyPageNo) {
    var self = this;
    var state = this.pdfState;
    var chain = Promise.resolve();
    state.plan.pages.forEach(function (page) {
      if (onlyPageNo && page.no !== onlyPageNo) {
        return;
      }
      chain = chain.then(function () {
        if (state.aborted || page.status !== 'pending') {
          return null;
        }
        return self.runOnePdfPage(page.no);
      });
    });
    return chain.then(function () {
      return global.planSummary(state.plan);
    });
  };

  /**
   * runOnePdfPage — render → recognize → merge ONE page. The page's
   * classified rows replace its segment in this.rows (mergePageRows), so
   * retries replace instead of duplicating and other pages' edits survive
   * (AC-2). The textbook meta is taken from the FIRST page that yields
   * one (AC-6: the book background is confirmed once, never re-entered
   * per page).
   */
  PhotoAddForm.prototype.runOnePdfPage = function (pageNo) {
    var self = this;
    var state = this.pdfState;
    state.plan = global.applyPageEvent(state.plan, pageNo, 'start');
    this.renderPdfBar();
    return global.PdfPageRunner.renderPage(state.doc, pageNo)
      .then(function (blob) {
        if (!blob) {
          throw new Error('这页压缩后仍然太大');
        }
        // Issue #81: the page being recognized shows large in the 本次材料
        // column while the run continues — the parent reviews against the
        // very render the model is reading.
        self.showPdfPageBlob(blob, pageNo);
        return self.recognizePhoto(blob, self.draftCategory);
      })
      .then(function (data) {
        var parsed = parseRecognized(data);
        if (!state.metaDone && parsed.meta) {
          self.detectedMeta = parsed.meta;
          state.metaDone = true;
        }
        var classified = global.classifyPhotoEntries(parsed.entries, self.existingWords);
        self.rows = global.mergePageRows(self.rows, pageNo, classified);
        // Issue #11: the page's poems and display-only others replace
        // their own page segments (a retry never duplicates either).
        // Issue #13: the passages replace their own segment the same way.
        self.poemRows = mergePagePoems(self.poemRows, pageNo, parsed.poems, self.existingPoems);
        self.passageRows = mergePagePassages(self.passageRows, pageNo, parsed.passages, self.existingPassages);
        self.otherSections = mergePageOthers(self.otherSections, pageNo, parsed.others);
        state.plan = global.applyPageEvent(state.plan, pageNo, 'done');
        // The textbook panel renders with the FIRST rows that land: its
        // preselect fixes bookChoice, so the reclassify below (and any
        // mid-run edit) sees the real textbook mode — the ordering the
        // photo flow gets for free by rendering the panel right after its
        // single recognition. A retry never rebuilds it.
        if (!state.panelRendered &&
            (self.rows.length || self.poemRows.length || self.passageRows.length)) {
          self.renderTextbookPanel('');
          state.panelRendered = true;
        }
        // Settle cross-page duplicates NOW — the 与第 N 行重复 verdicts
        // come from classifyPhotoEntries over the whole merged array, and
        // the preview should show them the moment the page lands.
        self.reclassify();
        if (self.rows.length || self.poemRows.length || self.passageRows.length) {
          self.renderPreview();
        }
        if (self.sourcePageNo === pageNo) {
          self.setSourceCaption('第 ' + pageNo + ' 页 · 已识别');
        }
        self.renderPdfBar();
      })
      .catch(function (err) {
        state.plan = global.applyPageEvent(state.plan, pageNo, 'fail',
          (err && err.message) || '识别失败');
        if (self.sourcePageNo === pageNo) {
          self.setSourceCaption('第 ' + pageNo + ' 页 · 识别失败');
        }
        self.renderPdfBar();
      });
  };

  /**
   * finishPdfRun — the after-run surface. A run with failed pages says so
   * (never a whole-file success); an all-failed run leaves the honest
   * empty preview; otherwise the textbook panel renders ONCE (a retry
   * must not rebuild it out from under the parent's choices).
   */
  PhotoAddForm.prototype.finishPdfRun = function (summary) {
    var kit = global.__parentFormKit || {};
    if (!this.pdfState) {
      return;
    }
    // Issue #11/#13: a poems-only or passages-only run is a real result
    // too — emptiness is judged over EVERY content kind.
    if (!this.rows.length && !this.poemRows.length && !this.passageRows.length) {
      this.resetEntries();
      this.renderPreviewHint(summary && summary.failed > 0
        ? '这一轮没有一页识别成功，请检查文件后重试'
        : '这次处理没有识别到词表内容');
      return;
    }
    if (!this.pdfState.panelRendered) {
      this.renderTextbookPanel('');
      this.pdfState.panelRendered = true;
    }
    this.renderPreview();
    this.discardBtn.hidden = false;
    if (summary && summary.failed > 0) {
      this.showFormError('有 ' + summary.failed + ' 页识别失败（在上方状态条可重试失败页）；已完成页的结果不受影响。');
      if (kit.showToast) {
        kit.showToast('有 ' + summary.failed + ' 页识别失败，可单独重试', 'error');
      }
    }
  };

  /**
   * handlePdfRetryPage — one failed page again: pending → run → finish.
   * Completed pages' rows are untouched (the merge replaces only the
   * retried page's segment).
   */
  PhotoAddForm.prototype.handlePdfRetryPage = function (pageNo) {
    var self = this;
    var state = this.pdfState;
    if (!state || this.recognizeBtn.disabled) {
      return;
    }
    state.aborted = false;
    state.plan = global.retryPage(state.plan, pageNo);
    this.showFormError('');
    this.setBusy(true);
    this.runPdfPages(pageNo).then(function (summary) {
      self.setBusy(false);
      self.finishPdfRun(summary);
    });
  };

  /**
   * handlePdfResume — the 继续处理 button's handler: after 停止, run the
   * still-pending pages only. Completed pages' rows are never re-merged
   * (their edits survive), and pending pages render fresh — the same
   * machinery the single-page retry rides.
   */
  PhotoAddForm.prototype.handlePdfResume = function () {
    var self = this;
    var state = this.pdfState;
    if (!state || this.recognizeBtn.disabled) {
      return;
    }
    state.aborted = false;
    this.showFormError('');
    this.setBusy(true);
    this.runPdfPages().then(function (summary) {
      self.setBusy(false);
      self.finishPdfRun(summary);
    });
  };

  /**
   * renderPdfBar — the per-page status surface: one chip per page (页码 +
   * 状态， title carries the failure reason), a 重试该页 button on failed
   * chips, the whole-file summary line, and a 停止 button while running
   * (AC-3 处理过程显示状态). textContent-only (Pitfall 10).
   */
  PhotoAddForm.prototype.renderPdfBar = function () {
    var self = this;
    var state = this.pdfState;
    if (!state) {
      this.pdfBar.hidden = true;
      clearEl(this.pdfBar);
      return;
    }
    this.pdfBar.hidden = false;
    clearEl(this.pdfBar);

    var summary = global.planSummary(state.plan);
    var line = document.createElement('p');
    line.className = 'pdf-summary-line';
    line.textContent = '共 ' + summary.total + ' 页：成功 ' + summary.done +
      '，失败 ' + summary.failed +
      (summary.pending ? '，待处理 ' + summary.pending : '') +
      (summary.running ? '，识别中 ' + summary.running : '');
    this.pdfBar.appendChild(line);

    var chips = document.createElement('div');
    chips.className = 'pdf-page-chips';
    state.plan.pages.forEach(function (page) {
      var chip = document.createElement('span');
      chip.className = 'pdf-page-chip pdf-chip-' + page.status;
      chip.setAttribute('data-page', String(page.no));
      chip.setAttribute('data-status', page.status);
      if (page.error) {
        chip.title = '第 ' + page.no + ' 页：' + page.error;
      }
      chip.textContent = '第 ' + page.no + ' 页';
      if (page.status === 'failed') {
        var retry = document.createElement('button');
        retry.type = 'button';
        retry.className = 'btn btn-secondary pdf-retry-btn';
        retry.textContent = '重试';
        retry.setAttribute('aria-label', '重试第 ' + page.no + ' 页');
        retry.addEventListener('click', function () {
          self.handlePdfRetryPage(page.no);
        });
        chip.appendChild(retry);
      }
      chips.appendChild(chip);
    });
    this.pdfBar.appendChild(chips);

    if (summary.processing) {
      var stop = document.createElement('button');
      stop.type = 'button';
      stop.className = 'btn btn-secondary pdf-stop-btn';
      stop.textContent = '停止（保留已完成页）';
      stop.addEventListener('click', function () {
        state.aborted = true;
      });
      this.pdfBar.appendChild(stop);
    } else if (summary.pending > 0) {
      // The 停止 counterpart: resume ONLY the still-pending pages — the
      // completed pages' rows (and the parent's edits to them) are never
      // re-run (AC-4's preservation, extended from failed pages to
      // stopped ones).
      var resume = document.createElement('button');
      resume.type = 'button';
      resume.className = 'btn btn-secondary pdf-resume-btn';
      resume.textContent = '继续处理（剩 ' + summary.pending + ' 页）';
      resume.addEventListener('click', function () {
        self.handlePdfResume();
      });
      this.pdfBar.appendChild(resume);
    }
  };

  PhotoAddForm.prototype.setBusy = function (busy) {
    var kit = global.__parentFormKit || {};
    if (kit.setLoading) {
      kit.setLoading(this.recognizeBtn, busy);
    } else {
      this.recognizeBtn.disabled = busy;
    }
    this.pickBtn.disabled = busy;
    if (busy) {
      this.commitBtn.disabled = true;
    } else {
      this.updateCommitButton();
    }
  };

  PhotoAddForm.prototype.resetEntries = function () {
    this.rows = [];
    this.poemRows = [];
    this.otherSections = [];
    this.existingPoems = [];
    this.passageRows = [];
    this.existingPassages = [];
    this.detectedMeta = null;
    this.bookChoice = 'none';
    this.includePending = false;
    this.renderTextbookPanelReset();
    this.renderPreview();
    this.discardBtn.hidden = true;
  };

  PhotoAddForm.prototype.renderPreviewHint = function (text) {
    clearEl(this.preview);
    var note = document.createElement('p');
    note.className = 'preview-empty';
    note.textContent = text;
    this.preview.appendChild(note);
  };

  // ------------------------------------------------------------------
  // Issue #5 — the textbook attribution panel + lesson column
  // ------------------------------------------------------------------

  /** The textbook deps are all-or-nothing (parent-app wires them as a set). */
  PhotoAddForm.prototype.textbookDepsReady = function () {
    return !!(this.fetchTextbooks && this.createTextbook &&
      this.updateTextbook && this.assignWords);
  };

  /** The chosen book object (null for 'none'/'new'/not found). */
  PhotoAddForm.prototype.chosenBook = function () {
    if (this.bookChoice === 'none' || this.bookChoice === 'new') {
      return null;
    }
    var id = this.bookChoice;
    var found = null;
    this.books.forEach(function (b) {
      if (b && b.id === id) {
        found = b;
      }
    });
    return found;
  };

  /**
   * The lesson options shared by the toolbar datalist and the rows:
   * 待确认 first, then the chosen book's lesson list (teaching order),
   * then any row lesson the list does not carry yet, in row order. For a
   * new book the prospective list is just the rows' distinct lessons.
   */
  PhotoAddForm.prototype.lessonOptions = function () {
    var options = [''];
    var seen = {};
    var book = this.chosenBook();
    (book && book.lessons ? book.lessons : []).forEach(function (name) {
      if (name && !seen[name]) {
        seen[name] = true;
        options.push(name);
      }
    });
    distinctRowLessons(this.rows).forEach(function (name) {
      if (!seen[name]) {
        seen[name] = true;
        options.push(name);
      }
    });
    return options;
  };

  /** Hide the panel (no recognition yet / reset). */
  PhotoAddForm.prototype.renderTextbookPanelReset = function () {
    this.textbookPanel.hidden = true;
    clearEl(this.textbookPanel);
  };

  /**
   * Build the attribution panel after a recognition. loadError carries the
   * textbook-list failure reason ('' when the list loaded fine). The
   * PRESELECT is honest: an existing book only when the meta actually
   * matches; a recognized-but-unknown header starts at 新建教材 (prefilled
   * from the meta); no header at all stays 不指定教材.
   */
  PhotoAddForm.prototype.renderTextbookPanel = function (loadError) {
    var self = this;
    this.textbookPanel.hidden = false;
    clearEl(this.textbookPanel);
    if (!this.textbookDepsReady()) {
      // Degraded mount (wiring incomplete): the plain #3 supplementary
      // flow stays fully usable; the panel says why it is absent.
      var offNote = document.createElement('p');
      offNote.className = 'photo-meta-line';
      offNote.textContent = '教材归属不可用（服务未配置），本次将作为补充词汇添加。';
      this.textbookPanel.appendChild(offNote);
      return;
    }

    var title = document.createElement('h4');
    title.className = 'photo-textbook-title';
    title.textContent = '教材归属';
    this.textbookPanel.appendChild(title);

    // The read-off-the-photo header, 待确认 for every unread field (AC2).
    var metaLine = document.createElement('p');
    metaLine.className = 'photo-meta-line';
    var metaParts = META_FIELDS.concat([{ key: 'lesson', label: '课次' }]).map(function (f) {
      var value = self.detectedMeta ? metaField(self.detectedMeta[f.key]) : '';
      return f.label + ' ' + (value || '待确认');
    });
    metaLine.textContent = (this.detectedMeta ? '识别到：' : '照片上未识别到教材信息（') +
      metaParts.join(' · ') + (this.detectedMeta ? '' : '）');
    this.textbookPanel.appendChild(metaLine);

    if (loadError) {
      var errNote = document.createElement('p');
      errNote.className = 'photo-meta-line photo-meta-warn';
      errNote.textContent = loadError;
      this.textbookPanel.appendChild(errNote);
    }

    // Preselect: honest match → that book; recognized header, no match →
    // 新建教材; nothing readable → 不指定 (the plain #3 flow).
    var preselect = matchTextbookForMeta(this.books, this.detectedMeta);
    if (preselect) {
      this.bookChoice = preselect.id;
    } else if (this.detectedMeta) {
      this.bookChoice = 'new';
    } else {
      this.bookChoice = 'none';
    }

    // 教材 select.
    var bookField = document.createElement('div');
    bookField.className = 'form-field photo-book-field';
    var bookLabel = document.createElement('span');
    bookLabel.className = 'form-label';
    bookLabel.textContent = '教材';
    var bookSelect = document.createElement('select');
    bookSelect.className = 'form-input photo-book-select';
    bookSelect.id = 'parent-photo-textbook';
    bookSelect.setAttribute('aria-label', '教材归属');
    [
      { value: 'none', text: '不指定教材（作为补充词汇）' },
      { value: 'new', text: '新建教材…' },
    ].concat(this.books.map(function (b) {
      var summary = global.textbookMetaSummary ? global.textbookMetaSummary(b) : '';
      return { value: String(b.id), text: b.name + (summary ? '（' + summary + '）' : '') };
    })).forEach(function (opt) {
      var o = document.createElement('option');
      o.value = opt.value;
      o.textContent = opt.text;
      bookSelect.appendChild(o);
    });
    bookSelect.value = String(this.bookChoice);
    bookField.appendChild(bookLabel);
    bookField.appendChild(bookSelect);
    this.textbookPanel.appendChild(bookField);

    var detailHost = document.createElement('div');
    detailHost.className = 'photo-book-detail';
    this.textbookPanel.appendChild(detailHost);
    this.bookDetailHost = detailHost;

    bookSelect.addEventListener('change', function () {
      self.bookChoice = bookSelect.value === 'new' ? 'new'
        : (bookSelect.value === 'none' ? 'none' : parseInt(bookSelect.value, 10));
      self.renderTextbookDetail(detailHost);
      self.renderPreview(); // lesson column visibility follows the choice
    });

    this.renderTextbookDetail(detailHost);
    this.updateStateLine();
  };

  /**
   * The panel's second row: an existing book's progress + the
   * lessons-that-will-append note, or the 新建教材 mini-form prefilled from
   * the meta (AC1/AC2: the missing background is confirmed ONCE, at
   * creation). Also hosts the batch lesson toolbar + the include checkbox
   * whenever a textbook is chosen.
   */
  PhotoAddForm.prototype.renderTextbookDetail = function (host) {
    var self = this;
    clearEl(host);
    if (this.bookChoice === 'none') {
      var noneNote = document.createElement('p');
      noneNote.className = 'photo-meta-line';
      noneNote.textContent = '这些词将作为补充词汇加入，默认加入默写范围（补充词汇不要求虚构课次）。';
      host.appendChild(noneNote);
      return;
    }

    if (this.bookChoice === 'new') {
      var newHint = document.createElement('p');
      newHint.className = 'photo-meta-line';
      newHint.textContent = '确认必要信息建立教材（只需这一次）；识别到的信息已预填，可修改。';
      host.appendChild(newHint);

      var nameField = document.createElement('div');
      nameField.className = 'form-field';
      var nameLabel = document.createElement('span');
      nameLabel.className = 'form-label';
      nameLabel.textContent = '教材名称（必填）';
      var nameInput = document.createElement('input');
      nameInput.type = 'text';
      nameInput.className = 'form-input';
      nameInput.id = 'parent-photo-tb-name';
      // parent-textbook.js's summary join IS the prefill (one encoder for
      // 版本·学段·年级·上下册·模块; a missing script degrades to no prefill).
      nameInput.value = global.textbookMetaSummary ? global.textbookMetaSummary(this.detectedMeta) : '';
      nameInput.setAttribute('aria-label', '教材名称');
      nameField.appendChild(nameLabel);
      nameField.appendChild(nameInput);
      host.appendChild(nameField);

      META_FIELDS.forEach(function (meta) {
        var field = document.createElement('div');
        field.className = 'form-field';
        var label = document.createElement('span');
        label.className = 'form-label';
        label.textContent = meta.label + '（选填）';
        var input = document.createElement('input');
        input.type = 'text';
        input.className = 'form-input';
        input.id = 'parent-photo-tb-' + meta.key;
        input.placeholder = meta.ph;
        input.value = self.detectedMeta ? metaField(self.detectedMeta[meta.key]) : '';
        input.setAttribute('aria-label', meta.label);
        field.appendChild(label);
        field.appendChild(input);
        host.appendChild(field);
      });

      var lessonsNote = document.createElement('p');
      lessonsNote.className = 'photo-meta-line';
      lessonsNote.textContent = '课次表将按本次确认的课次生成（可在「教材管理」中补全整本顺序）。';
      host.appendChild(lessonsNote);
    } else {
      var book = this.chosenBook();
      if (!book) {
        return;
      }
      var bookNote = document.createElement('p');
      bookNote.className = 'photo-meta-line';
      var progress = global.progressLabel ? global.progressLabel(book) : '';
      bookNote.textContent = progress + '；已学完第 N 课包含第 N 课，之后的课次不会自动选入。';
      host.appendChild(bookNote);

      // Which confirmed lessons are NOT in the book's list yet (they will
      // append at the end — order-correctable in 教材管理 afterwards).
      var inList = {};
      (book.lessons || []).forEach(function (name) {
        inList[name] = true;
      });
      var missing = distinctRowLessons(this.rows).filter(function (name) {
        return !inList[name];
      });
      if (missing.length) {
        var appendNote = document.createElement('p');
        appendNote.className = 'photo-meta-line';
        appendNote.textContent = '课次「' + missing.join('」「') + '」不在教材课次表里，确认后将加到课次表末尾。';
        host.appendChild(appendNote);
      }
    }

    // Batch lesson toolbar (AC3): one lesson onto every CHECKED row. Free
    // text with datalist suggestions — correcting a misread lesson to a
    // name the list does not carry yet is the normal case, so the input
    // never traps the parent inside the current options.
    var toolbar = document.createElement('div');
    toolbar.className = 'photo-lesson-toolbar';
    var toolLabel = document.createElement('span');
    toolLabel.className = 'form-label';
    toolLabel.textContent = '批量指定课次';
    var lessonInput = document.createElement('input');
    lessonInput.type = 'text';
    lessonInput.className = 'form-input photo-lesson-select';
    lessonInput.id = 'parent-photo-lesson';
    lessonInput.setAttribute('list', 'parent-photo-lesson-options');
    lessonInput.setAttribute('aria-label', '批量指定课次');
    lessonInput.placeholder = '如 第5课（留空＝待确认）';
    var applyBtn = document.createElement('button');
    applyBtn.type = 'button';
    applyBtn.className = 'btn btn-secondary';
    applyBtn.textContent = '应用到勾选行';
    applyBtn.addEventListener('click', function () {
      var lesson = lessonInput.value.trim();
      self.rows.forEach(function (row) {
        if (isCommittable(row, true) && row.checked !== false) {
          row.lesson = lesson;
        }
      });
      // Re-render rows so the per-row inputs reflect the batch value, and
      // the detail host so the append-note matches the corrected lessons.
      self.renderPreview();
      self.renderTextbookDetail(host);
    });
    toolbar.appendChild(toolLabel);
    toolbar.appendChild(lessonInput);
    toolbar.appendChild(applyBtn);
    host.appendChild(toolbar);

    // AC5: the explicit include-in-scope handling for lesson-less rows.
    var includeLabel = document.createElement('label');
    includeLabel.className = 'photo-include-toggle';
    var include = document.createElement('input');
    include.type = 'checkbox';
    include.id = 'parent-photo-include';
    include.checked = this.includePending;
    include.addEventListener('change', function () {
      self.includePending = include.checked;
      // Issue #81: the checkbox changes WHAT WILL BE SCHEDULED — the state
      // line and the save-note follow immediately (no table rebuild).
      self.updateStateLine();
      self.updateCommitHint();
    });
    var includeText = document.createElement('span');
    includeText.textContent = '待确认课次的词直接加入默写范围（不受进度限制）';
    includeLabel.appendChild(include);
    includeLabel.appendChild(includeText);
    host.appendChild(includeLabel);

    this.updateStateLine();
  };

  /** Read the 新建教材 mini-form's values back (name + five meta fields). */
  PhotoAddForm.prototype.readCreateForm = function () {
    var values = { name: '' };
    var nameInput = document.getElementById('parent-photo-tb-name');
    if (nameInput) {
      values.name = nameInput.value.trim();
    }
    META_FIELDS.forEach(function (meta) {
      var input = document.getElementById('parent-photo-tb-' + meta.key);
      values[meta.key] = input ? input.value.trim() : '';
    });
    return values;
  };

  /** Re-run classification over the CURRENT (edited) row values. */
  PhotoAddForm.prototype.reclassify = function () {
    var textbookMode = this.bookChoice !== 'none';
    var entries = this.rows.map(function (r) {
      return { word: r.word, meaning: r.meaning, lesson: r.lesson };
    });
    var next = classifyPhotoEntries(entries, this.existingWords);
    // Carry over the checked flag and the lesson the parent set (new rows
    // default on; in textbook mode exists rows are committable too). The
    // resolution carry MUST precede the checked computation — isCommittable
    // reads it (a fresh conflict row would otherwise drop its parent-
    // confirmed state and uncheck itself).
    for (var i = 0; i < next.length; i++) {
      if (this.rows[i].status === next[i].status) {
        next[i].existingMeaning = next[i].existingMeaning || this.rows[i].existingMeaning;
        // A resolution survives edits that keep the row a conflict (adopt
        // then means "use what's in the box"); a status change drops it.
        next[i].resolution = this.rows[i].resolution || '';
      }
      next[i].checked = this.rows[i].checked !== false && isCommittable(next[i], textbookMode);
      next[i].lesson = this.rows[i].lesson || '';
      // Issue #7: the page tag rides every re-classification — the
      // preview's page grouping and mergePageRows' segment replace read it.
      next[i].page = this.rows[i].page || 0;
      // Issue #8: provenance and the unanswered-attempt flag ride too —
      // editing a word never silently drops the 系统补充 marker or the
      // 生成失败 note (ADR-0005: edits do not change the source).
      next[i].meaningSource = this.rows[i].meaningSource || '';
      next[i].genFailed = !!this.rows[i].genFailed;
    }
    this.rows = next;
  };

  /** Render the editable review table from this.rows. */
  PhotoAddForm.prototype.renderPreview = function () {
    var self = this;
    clearEl(this.preview);
    // Issue #81: every structural preview change re-derives the workspace
    // visibility and the state line (the line is pure text — the caret-
    // preserving edit path onRowEdited refreshes it in place too).
    this.syncWorkspace();
    this.updateStateLine();
    // Issue #11/#13: a poems-only or passages-only draft is a real
    // preview — the hint shows only when EVERY content kind is empty.
    if (!this.rows.length && !this.poemRows.length && !this.passageRows.length) {
      this.renderPreviewHint('选择照片或 PDF 后点「开始识别」');
      this.updateCommitButton();
      this.renderMeaningBar();
      this.renderPoemPreview();
      this.renderPassagePreview();
      this.renderOthersPreview();
      return;
    }
    if (!this.rows.length) {
      // Poems/passages only: the 词语 table renders its honest header
      // instead of the pick-a-photo hint (the cards below ARE the
      // recognition).
      var wordsNote = document.createElement('p');
      wordsNote.className = 'preview-empty';
      wordsNote.textContent = '这张照片没有识别到词语表条目';
      this.preview.appendChild(wordsNote);
      this.updateCommitButton();
      this.renderMeaningBar();
      this.renderPoemPreview();
      this.renderPassagePreview();
      this.renderOthersPreview();
      return;
    }
    var textbookMode = this.bookChoice !== 'none';

    var table = document.createElement('table');
    table.className = 'preview-table photo-preview-table';

    var thead = document.createElement('thead');
    var headRow = document.createElement('tr');
    // Issue #81: 单词/释义 carry the visual weight (photo-th-primary);
    // 课次/状态 are deliberately secondary (photo-th-secondary) — the
    // teaching info and the verdict read beside the content, not in it.
    var headers = textbookMode
      ? [
        { text: '加入' },
        { text: '单词', cls: 'photo-th-primary' },
        { text: '释义（原文）', cls: 'photo-th-primary' },
        { text: '课次', cls: 'photo-th-secondary' },
        { text: '状态', cls: 'photo-th-secondary' },
      ]
      : [
        { text: '加入' },
        { text: '单词', cls: 'photo-th-primary' },
        { text: '释义（原文）', cls: 'photo-th-primary' },
        { text: '状态', cls: 'photo-th-secondary' },
      ];
    headers.forEach(function (def) {
      var th = document.createElement('th');
      th.setAttribute('scope', 'col');
      th.textContent = def.text;
      if (def.cls) {
        th.className = def.cls;
      }
      headRow.appendChild(th);
    });
    thead.appendChild(headRow);
    table.appendChild(thead);

    var tbody = document.createElement('tbody');
    // Issue #7: PDF rows group under per-page header rows (页码 + that
    // page's status + a 本页批量课次 mini-toolbar). Photo rows (no page
    // tag) render flat, exactly as before.
    var hasPages = this.pdfState && this.rows.some(function (r) { return r.page; });
    if (hasPages) {
      global.rowsByPage(this.rows).forEach(function (group) {
        if (group.no) {
          tbody.appendChild(self.buildPageHeadRow(group, headers.length));
        }
        group.rows.forEach(function (row) {
          tbody.appendChild(self.buildReviewRow(row, row.index, textbookMode));
        });
      });
    } else {
      this.rows.forEach(function (row, i) {
        tbody.appendChild(self.buildReviewRow(row, i, textbookMode));
      });
    }
    table.appendChild(tbody);
    this.preview.appendChild(table);

    if (textbookMode) {
      // The shared lesson datalist: book lessons + the rows' current
      // lessons — free typing still allowed (a corrected lesson the list
      // does not carry yet is the NORMAL case after a misread header).
      var datalist = document.createElement('datalist');
      datalist.id = 'parent-photo-lesson-options';
      this.lessonOptions().forEach(function (name) {
        if (name) {
          var o = document.createElement('option');
          o.value = name;
          datalist.appendChild(o);
        }
      });
      this.preview.appendChild(datalist);
    }
    this.updateCommitButton();
    this.renderMeaningBar();
    this.renderPoemPreview();
    this.renderPassagePreview();
    this.renderOthersPreview();
  };

  /**
   * renderPoemPreview (Issue #11) — the mixed page's 古诗 section: one
   * editable CARD per whole poem (checkbox, title, author, the ordered
   * verse lines in one textarea — one line per row, a poem is edited as a
   * work, never as unrelated rows — and the per-poem lesson in textbook
   * mode). The page's own 背诵/默写 mark rides a badge; an unmarked poem
   * is selectable but NOT auto-checked (明确背诵/默写要求影响默认选择).
   * textContent/createElement only (Pitfall 10).
   */
  PhotoAddForm.prototype.renderPoemPreview = function () {
    var self = this;
    this.poemHost.hidden = true;
    clearEl(this.poemHost);
    if (!this.poemRows.length) {
      return;
    }
    var textbookMode = this.bookChoice !== 'none';

    var head = document.createElement('h4');
    head.className = 'photo-section-title';
    head.textContent = '古诗（' + this.poemRows.length + ' 首，整首保存）';
    this.poemHost.appendChild(head);
    this.poemHost.hidden = false;

    // Degraded wiring (no submitPoems handler): the poems stay visible
    // for 核对 but cannot join the commit — the reason says so, the 词语
    // flow is untouched (poems are an additive surface).
    if (!this.submitPoems) {
      var degraded = document.createElement('p');
      degraded.className = 'photo-poem-note';
      degraded.textContent = '本页暂不支持保存古诗（古诗接口未接入）；词语导入不受影响。';
      this.poemHost.appendChild(degraded);
    }

    this.poemRows.forEach(function (row, i) {
      self.poemHost.appendChild(self.buildPoemCard(row, i, textbookMode));
    });

    if (textbookMode) {
      // The poem lesson inputs share the 词语 table's datalist (the same
      // book's lessons); free typing stays allowed.
      var datalist = document.createElement('datalist');
      datalist.id = 'parent-photo-poem-lesson-options';
      this.lessonOptions().forEach(function (name) {
        if (name) {
          var o = document.createElement('option');
          o.value = name;
          datalist.appendChild(o);
        }
      });
      this.poemHost.appendChild(datalist);
    }
  };

  /** One editable poem card (the buildReviewRow kin for whole works). */
  PhotoAddForm.prototype.buildPoemCard = function (row, i, textbookMode) {
    var self = this;
    var card = document.createElement('div');
    card.className = 'photo-poem-card photo-poem-' + row.status;
    card.setAttribute('data-poem-index', String(i));

    var top = document.createElement('div');
    top.className = 'photo-poem-top';

    // A poem joins the commit ONLY when the parent checks it (exists rows
    // stay checkable too — the server reports existing and never copies).
    // Degraded wiring (no submitPoems) keeps the check visible but inert.
    var checkHit = document.createElement('label');
    checkHit.className = 'inline-check-hit';
    var check = document.createElement('input');
    check.type = 'checkbox';
    check.className = 'photo-poem-check';
    check.id = 'parent-photo-poem-check-' + i;
    check.checked = row.status !== 'invalid' && row.checked !== false;
    check.disabled = row.status === 'invalid' || !self.submitPoems;
    check.setAttribute('aria-label', '加入 ' + (row.title || ('第 ' + (i + 1) + ' 首')));
    check.addEventListener('change', function () {
      self.poemRows[i].checked = check.checked;
      self.updateCommitButton();
    });
    checkHit.appendChild(check);
    top.appendChild(checkHit);

    var titleInput = document.createElement('input');
    titleInput.type = 'text';
    titleInput.className = 'form-input photo-poem-title';
    titleInput.value = row.title;
    titleInput.setAttribute('aria-label', '诗题 第 ' + (i + 1) + ' 首');
    titleInput.addEventListener('input', function () {
      self.poemRows[i].title = titleInput.value;
      self.onPoemEdited(i);
    });
    top.appendChild(titleInput);

    var authorInput = document.createElement('input');
    authorInput.type = 'text';
    authorInput.className = 'form-input photo-poem-author';
    authorInput.value = row.author;
    authorInput.placeholder = '作者（可空）';
    authorInput.setAttribute('aria-label', '作者 第 ' + (i + 1) + ' 首');
    authorInput.addEventListener('input', function () {
      self.poemRows[i].author = authorInput.value;
      self.onPoemEdited(i);
    });
    top.appendChild(authorInput);

    var required = poemRequiredLabel(row.required);
    if (required) {
      var reqBadge = document.createElement('span');
      reqBadge.className = 'photo-poem-required';
      reqBadge.textContent = required;
      reqBadge.title = '照片上明确印出的要求，默认勾选加入';
      top.appendChild(reqBadge);
    }

    var statusText = document.createElement('span');
    statusText.className = 'photo-poem-status photo-poem-status-' + row.status;
    statusText.textContent = row.status === 'new'
      ? '新古诗'
      : (row.status === 'exists' ? '已有（不会重复建立）' : '无法添加');
    top.appendChild(statusText);
    card.appendChild(top);

    // The ordered verse lines: one textarea, one line per row — order and
    // line breaks are the work's own structure (分行和顺序保留).
    var linesInput = document.createElement('textarea');
    linesInput.className = 'form-textarea photo-poem-lines';
    linesInput.rows = Math.max(2, row.lines.length);
    linesInput.value = row.lines.join('\n');
    linesInput.setAttribute('aria-label', '诗句 第 ' + (i + 1) + ' 首（每行一句）');
    linesInput.addEventListener('input', function () {
      self.poemRows[i].lines = linesInput.value.split('\n');
      self.onPoemEdited(i);
    });
    card.appendChild(linesInput);

    var bottom = document.createElement('div');
    bottom.className = 'photo-poem-bottom';
    if (textbookMode) {
      var lessonInput = document.createElement('input');
      lessonInput.type = 'text';
      lessonInput.className = 'form-input photo-poem-lesson';
      lessonInput.setAttribute('list', 'parent-photo-poem-lesson-options');
      lessonInput.setAttribute('aria-label', '课次 第 ' + (i + 1) + ' 首');
      lessonInput.placeholder = '待确认';
      lessonInput.value = row.lesson || '';
      lessonInput.disabled = row.status === 'invalid';
      lessonInput.addEventListener('input', function () {
        self.poemRows[i].lesson = lessonInput.value;
        self.updateStateLine();
        self.updateCommitHint();
      });
      bottom.appendChild(lessonInput);
    }
    if (row.status === 'exists') {
      var note = document.createElement('span');
      note.className = 'photo-poem-note';
      note.textContent = '内容库已有这首诗（整首相同）；勾选后仅补指定课次';
      bottom.appendChild(note);
    }
    if (row.status === 'invalid') {
      var why = document.createElement('span');
      why.className = 'photo-poem-note';
      why.textContent = describePoemInvalidReason(row.reason);
      bottom.appendChild(why);
    }
    card.appendChild(bottom);
    return card;
  };

  /**
   * onPoemEdited — re-classify ONE poem row's derived surfaces after an
   * edit WITHOUT re-rendering the card (a full re-render would steal the
   * caret mid-word, the onRowEdited reason). Status/checkbox sync only;
   * the caller keeps the row model current.
   */
  PhotoAddForm.prototype.onPoemEdited = function (i) {
    var next = classifyPoemDrafts(this.poemRows, this.existingPoems)[i];
    var row = this.poemRows[i];
    if (!row || !next) {
      return;
    }
    // Preserve the parent's explicit choices across the re-classification;
    // the status/verdict follow the edited text.
    next.checked = row.checked;
    next.page = row.page;
    this.poemRows[i] = next;

    var card = this.poemHost.querySelector('[data-poem-index="' + i + '"]');
    if (!card) {
      return;
    }
    card.className = 'photo-poem-card photo-poem-' + next.status;
    var check = card.querySelector('.photo-poem-check');
    if (check) {
      check.disabled = next.status === 'invalid';
      check.checked = next.status !== 'invalid' && next.checked !== false;
    }
    var statusEl = card.querySelector('.photo-poem-status');
    if (statusEl) {
      statusEl.textContent = next.status === 'new'
        ? '新古诗'
        : (next.status === 'exists' ? '已有（不会重复建立）' : '无法添加');
    }
    this.updateCommitButton();
  };

  /**
   * renderPassagePreview (Issue #13) — the mixed page's 课文 section: one
   * editable CARD per whole passage. The card carries the ordered 自然段
   * (one textarea per paragraph — boundaries and order are the work's own
   * structure), the practice-range picker (整篇 checkbox + one 选择该段
   * checkbox per paragraph — 选择整篇或指定自然段), the page's own marks as
   * badges, and the per-passage lesson in textbook mode. A checked card
   * with NEITHER whole nor any paragraph commits the TEXT only (原文和练
   * 习范围分别表达 — the range is picked later on the content page). An
   * unmarked passage is selectable but NOT auto-checked. textContent/
   * createElement only (Pitfall 10).
   */
  PhotoAddForm.prototype.renderPassagePreview = function () {
    var self = this;
    this.passageHost.hidden = true;
    clearEl(this.passageHost);
    if (!this.passageRows.length) {
      return;
    }
    var textbookMode = this.bookChoice !== 'none';

    var head = document.createElement('h4');
    head.className = 'photo-section-title';
    head.textContent = '课文（' + this.passageRows.length + ' 篇，整篇保存）';
    this.passageHost.appendChild(head);
    this.passageHost.hidden = false;

    // Degraded wiring (no submitPassages handler): the passages stay
    // visible for 核对 but cannot join the commit — the reason says so,
    // the 词语/古诗 flows are untouched (passages are an additive surface).
    if (!this.submitPassages) {
      var degraded = document.createElement('p');
      degraded.className = 'photo-passage-note';
      degraded.textContent = '本页暂不支持保存课文（课文接口未接入）；词语和古诗导入不受影响。';
      this.passageHost.appendChild(degraded);
    }

    this.passageRows.forEach(function (row, i) {
      self.passageHost.appendChild(self.buildPassageCard(row, i, textbookMode));
    });

    if (textbookMode) {
      // The passage lesson inputs share the 词语 table's datalist (the
      // same book's lessons); free typing stays allowed.
      var datalist = document.createElement('datalist');
      datalist.id = 'parent-photo-passage-lesson-options';
      this.lessonOptions().forEach(function (name) {
        if (name) {
          var o = document.createElement('option');
          o.value = name;
          datalist.appendChild(o);
        }
      });
      this.passageHost.appendChild(datalist);
    }
  };

  /** One editable passage card — the buildPoemCard kin for whole texts. */
  PhotoAddForm.prototype.buildPassageCard = function (row, i, textbookMode) {
    var self = this;
    var card = document.createElement('div');
    card.className = 'photo-passage-card photo-passage-' + row.status;
    card.setAttribute('data-passage-index', String(i));

    var top = document.createElement('div');
    top.className = 'photo-passage-top';

    // A passage joins the commit ONLY when the parent checks it (exists
    // rows stay checkable too — the server reports existing and never
    // copies). Degraded wiring keeps the check visible but inert.
    var checkHit = document.createElement('label');
    checkHit.className = 'inline-check-hit';
    var check = document.createElement('input');
    check.type = 'checkbox';
    check.className = 'photo-passage-check';
    check.id = 'parent-photo-passage-check-' + i;
    check.checked = row.status !== 'invalid' && row.checked !== false;
    check.disabled = row.status === 'invalid' || !self.submitPassages;
    check.setAttribute('aria-label', '加入 ' + (row.title || ('第 ' + (i + 1) + ' 篇')));
    check.addEventListener('change', function () {
      self.passageRows[i].checked = check.checked;
      self.syncPassageCard(i);
      self.updateCommitButton();
    });
    checkHit.appendChild(check);
    top.appendChild(checkHit);

    var titleInput = document.createElement('input');
    titleInput.type = 'text';
    titleInput.className = 'form-input photo-passage-title';
    titleInput.value = row.title;
    titleInput.setAttribute('aria-label', '课文标题 第 ' + (i + 1) + ' 篇');
    titleInput.addEventListener('input', function () {
      self.passageRows[i].title = titleInput.value;
      self.onPassageEdited(i);
    });
    top.appendChild(titleInput);

    var authorInput = document.createElement('input');
    authorInput.type = 'text';
    authorInput.className = 'form-input photo-passage-author';
    authorInput.value = row.author;
    authorInput.placeholder = '作者（可空）';
    authorInput.setAttribute('aria-label', '作者 第 ' + (i + 1) + ' 篇');
    authorInput.addEventListener('input', function () {
      self.passageRows[i].author = authorInput.value;
      self.onPassageEdited(i);
    });
    top.appendChild(authorInput);

    var required = passageRequiredLabel(row.required);
    if (required) {
      var reqBadge = document.createElement('span');
      reqBadge.className = 'photo-passage-required';
      reqBadge.textContent = required;
      reqBadge.title = '照片上明确印出的要求，默认勾选加入';
      top.appendChild(reqBadge);
    }

    var statusText = document.createElement('span');
    statusText.className = 'photo-passage-status photo-passage-status-' + row.status;
    statusText.textContent = row.status === 'new'
      ? '新课文'
      : (row.status === 'exists' ? '已有（不会重复建立）' : '无法添加');
    top.appendChild(statusText);
    card.appendChild(top);

    // The scope toolbar: 整篇 checkbox + the live range summary. Neither
    // whole nor paragraphs = text-only commit (the range is chosen later).
    var scopeBar = document.createElement('div');
    scopeBar.className = 'photo-passage-scope-bar';
    var wholeHit = document.createElement('label');
    wholeHit.className = 'inline-check-hit';
    var whole = document.createElement('input');
    whole.type = 'checkbox';
    whole.className = 'photo-passage-whole';
    whole.checked = row.scopeWhole;
    whole.disabled = row.status === 'invalid';
    whole.setAttribute('aria-label', '整篇练习 第 ' + (i + 1) + ' 篇');
    whole.addEventListener('change', function () {
      var r = self.passageRows[i];
      r.scopeWhole = whole.checked;
      if (whole.checked) {
        r.scopeParagraphs = [];
        card.querySelectorAll('.photo-passage-paragraph-check').forEach(function (el) {
          el.checked = false;
        });
      }
      self.syncPassageCard(i);
      self.updateCommitButton();
    });
    wholeHit.appendChild(whole);
    var wholeText = document.createElement('span');
    wholeText.textContent = '整篇练习';
    wholeHit.appendChild(wholeText);
    scopeBar.appendChild(wholeHit);
    var scopeNote = document.createElement('span');
    scopeNote.className = 'photo-passage-scope-note';
    scopeBar.appendChild(scopeNote);
    card.appendChild(scopeBar);

    // The ordered 自然段: one row per paragraph — scope checkbox + 段号 +
    // the page's own per-paragraph mark + the paragraph's own textarea
    // (段落边界和顺序保留; paragraphs are edited in place, never merged).
    var list = document.createElement('div');
    list.className = 'photo-passage-paragraphs';
    row.paragraphs.forEach(function (text, m) {
      var no = m + 1;
      var line = document.createElement('div');
      line.className = 'photo-passage-paragraph';
      line.setAttribute('data-paragraph-no', String(no));

      var paraCheck = document.createElement('input');
      paraCheck.type = 'checkbox';
      paraCheck.className = 'photo-passage-paragraph-check';
      paraCheck.checked = row.scopeParagraphs.indexOf(no) >= 0;
      paraCheck.disabled = row.status === 'invalid';
      paraCheck.setAttribute('aria-label', '练习第 ' + (i + 1) + ' 篇第 ' + no + ' 段');
      paraCheck.addEventListener('change', function () {
        var r = self.passageRows[i];
        var idx = r.scopeParagraphs.indexOf(no);
        if (paraCheck.checked && idx < 0) {
          r.scopeParagraphs.push(no);
          r.scopeParagraphs.sort(function (a, b) { return a - b; });
        } else if (!paraCheck.checked && idx >= 0) {
          r.scopeParagraphs.splice(idx, 1);
        }
        if (paraCheck.checked) {
          // 指定段落 and 整篇 are different ranges — picking paragraphs
          // leaves the whole box.
          r.scopeWhole = false;
        }
        self.syncPassageCard(i);
        self.updateCommitButton();
      });
      line.appendChild(paraCheck);

      var noLabel = document.createElement('span');
      noLabel.className = 'photo-passage-paragraph-no';
      noLabel.textContent = '第 ' + no + ' 段';
      line.appendChild(noLabel);

      if (row.requiredParagraphs.indexOf(no) >= 0) {
        var mark = document.createElement('span');
        mark.className = 'photo-passage-paragraph-required';
        mark.textContent = required ? required : '要求背诵';
        mark.title = '照片上明确印出的该段要求，默认勾选该段';
        line.appendChild(mark);
      }

      var textInput = document.createElement('textarea');
      textInput.className = 'form-textarea photo-passage-paragraph-text';
      textInput.rows = 2;
      textInput.value = text;
      textInput.setAttribute('aria-label', '第 ' + (i + 1) + ' 篇第 ' + no + ' 段');
      textInput.addEventListener('input', function () {
        var r = self.passageRows[i];
        var before = r.paragraphs.filter(function (t) { return t.trim() !== ''; }).length;
        r.paragraphs[m] = textInput.value;
        var after = r.paragraphs.filter(function (t) { return t.trim() !== ''; }).length;
        if (before !== after) {
          // A paragraph appeared or vanished — the 段号 numbering over the
          // KEPT paragraphs shifts, so the card rebuilds (a structural
          // edit; the caret survives the ordinary same-count edits).
          self.onPassageEdited(i);
          self.renderPassagePreview();
        } else {
          self.onPassageEdited(i);
        }
      });
      line.appendChild(textInput);
      list.appendChild(line);
    });
    card.appendChild(list);

    var bottom = document.createElement('div');
    bottom.className = 'photo-passage-bottom';
    if (textbookMode) {
      var lessonInput = document.createElement('input');
      lessonInput.type = 'text';
      lessonInput.className = 'form-input photo-passage-lesson';
      lessonInput.setAttribute('list', 'parent-photo-passage-lesson-options');
      lessonInput.setAttribute('aria-label', '课次 第 ' + (i + 1) + ' 篇');
      lessonInput.placeholder = '待确认';
      lessonInput.value = row.lesson || '';
      lessonInput.disabled = row.status === 'invalid';
      lessonInput.addEventListener('input', function () {
        self.passageRows[i].lesson = lessonInput.value;
        self.updateStateLine();
        self.updateCommitHint();
      });
      bottom.appendChild(lessonInput);
    }
    if (row.status === 'exists') {
      var note = document.createElement('span');
      note.className = 'photo-passage-note';
      note.textContent = '内容库已有这篇课文（整篇相同）；勾选后仅补指定课次和练习范围';
      bottom.appendChild(note);
    }
    if (row.status === 'invalid') {
      var why = document.createElement('span');
      why.className = 'photo-passage-note';
      why.textContent = describePassageInvalidReason(row.reason);
      bottom.appendChild(why);
    }
    card.appendChild(bottom);
    this.syncPassageCardNote(card, row);
    return card;
  };

  /**
   * syncPassageCard — refresh ONE card's derived surfaces (status class
   * AND text, check enablement, 整篇 box, the range summary note) without
   * re-rendering the card. The row model is the caller's to keep current.
   */
  PhotoAddForm.prototype.syncPassageCard = function (i) {
    var row = this.passageRows[i];
    var card = this.passageHost.querySelector('[data-passage-index="' + i + '"]');
    if (!row || !card) {
      return;
    }
    card.className = 'photo-passage-card photo-passage-' + row.status;
    var check = card.querySelector('.photo-passage-check');
    if (check) {
      check.disabled = row.status === 'invalid' || !this.submitPassages;
      check.checked = row.status !== 'invalid' && row.checked !== false;
    }
    var whole = card.querySelector('.photo-passage-whole');
    if (whole) {
      whole.disabled = row.status === 'invalid';
      whole.checked = row.scopeWhole;
    }
    var statusEl = card.querySelector('.photo-passage-status');
    if (statusEl) {
      statusEl.textContent = row.status === 'new'
        ? '新课文'
        : (row.status === 'exists' ? '已有（不会重复建立）' : '无法添加');
    }
    this.syncPassageCardNote(card, row);
  };

  /**
   * syncPassageCardNote — the live range summary: 整篇 / the explicit
   * paragraph numbers / the honest text-only note when nothing is chosen
   * (原文和练习范围分别表达 — a text-only save is legal, the range is
   * picked later on the content page).
   */
  PhotoAddForm.prototype.syncPassageCardNote = function (card, row) {
    var note = card.querySelector('.photo-passage-scope-note');
    if (!note) {
      return;
    }
    if (row.scopeWhole) {
      note.textContent = '练习范围：整篇';
    } else if (row.scopeParagraphs.length) {
      note.textContent = '练习范围：第 ' + row.scopeParagraphs.join('、') + ' 段';
    } else {
      note.textContent = row.checked && row.status !== 'invalid'
        ? '未选练习范围：将仅保存原文，之后可在内容页选择'
        : '未选练习范围';
    }
  };

  /**
   * onPassageEdited — re-classify ONE passage row's derived surfaces
   * after an edit WITHOUT re-rendering the card (the onPoemEdited
   * reason: a full re-render would steal the caret mid-word). The
   * parent's explicit choices survive the re-classification; the range
   * clamps to the paragraphs that still exist (an emptied paragraph can
   * never leave a range pointing past the end).
   */
  PhotoAddForm.prototype.onPassageEdited = function (i) {
    var next = classifyPassageDrafts(this.passageRows, this.existingPassages)[i];
    var row = this.passageRows[i];
    if (!row || !next) {
      return;
    }
    next.checked = row.checked;
    next.page = row.page;
    next.scopeWhole = row.scopeWhole;
    next.scopeParagraphs = row.scopeParagraphs.filter(function (no) {
      return no >= 1 && no <= next.paragraphs.length;
    });
    if (!next.scopeWhole && !next.scopeParagraphs.length && row.scopeParagraphs.length) {
      // An edit shrank the text below every chosen paragraph — fall back
      // to the page's own marks (or an honestly empty range) rather than
      // a range pointing past the end.
      next.scopeParagraphs = next.requiredParagraphs.filter(function (no) {
        return no >= 1 && no <= next.paragraphs.length;
      });
    }
    this.passageRows[i] = next;
    this.syncPassageCard(i);
    this.updateCommitButton();
  };

  /**
   * renderOthersPreview (Issue #11) — the mixed page's display-only
   * 其他内容 section: 课文正文 and 练习题 blocks are SHOWN (区分展示) with
   * their own kind badges and a plain 不会导入 note — they are not
   * selectable, never join the commit, and never become dictation words
   * (练习题不自动选入; 课文原文导入是后续任务).
   */
  PhotoAddForm.prototype.renderOthersPreview = function () {
    this.othersHost.hidden = true;
    clearEl(this.othersHost);
    if (!this.otherSections.length) {
      return;
    }
    var head = document.createElement('h4');
    head.className = 'photo-section-title';
    head.textContent = '其他内容（仅展示，不导入）';
    this.othersHost.appendChild(head);

    this.otherSections.forEach(function (o, i) {
      var line = document.createElement('div');
      line.className = 'photo-other-row';
      line.setAttribute('data-other-index', String(i));
      var badge = document.createElement('span');
      badge.className = 'photo-other-kind photo-other-' + o.kind;
      badge.textContent = o.kind === 'exercise' ? '练习题' : '课文正文';
      var summary = document.createElement('span');
      summary.className = 'photo-other-summary';
      summary.textContent = o.summary;
      var note = document.createElement('span');
      note.className = 'photo-other-note';
      note.textContent = '不导入';
      line.appendChild(badge);
      line.appendChild(summary);
      line.appendChild(note);
      this.othersHost.appendChild(line);
    }, this);
    this.othersHost.hidden = false;
  };

  /**
   * One page's header row (Issue #7): 页码 + the page's row count + the
   * 本页批量课次 mini-toolbar (US-13/AC-6 — setting one lesson onto a
   * whole page without touching other pages; the global toolbar stays
   * available for cross-page selections). The lesson input shares the
   * page-level datalist rendered right after the table. colCount is the
   * table's live header count (the thead is the one owner of the shape).
   */
  PhotoAddForm.prototype.buildPageHeadRow = function (group, colCount) {
    var self = this;
    var tr = document.createElement('tr');
    tr.className = 'pdf-page-head';
    var td = document.createElement('td');
    td.colSpan = colCount;

    var label = document.createElement('span');
    label.className = 'pdf-page-label';
    label.textContent = '第 ' + group.no + ' 页（' + group.rows.length + ' 条）';
    td.appendChild(label);

    var lessonInput = document.createElement('input');
    lessonInput.type = 'text';
    lessonInput.className = 'form-input pdf-page-lesson';
    lessonInput.setAttribute('list', 'parent-photo-lesson-options');
    lessonInput.setAttribute('aria-label', '第 ' + group.no + ' 页课次');
    lessonInput.placeholder = '本页课次（如 第5课）';
    var apply = document.createElement('button');
    apply.type = 'button';
    apply.className = 'btn btn-secondary pdf-page-lesson-apply';
    apply.textContent = '应用到本页';
    apply.addEventListener('click', function () {
      self.rows = global.applyLessonToPage(self.rows, group.no, lessonInput.value.trim());
      self.renderPreview();
      if (self.bookDetailHost) {
        self.renderTextbookDetail(self.bookDetailHost);
      }
    });
    td.appendChild(lessonInput);
    td.appendChild(apply);

    tr.appendChild(td);
    return tr;
  };

  /** One editable review row. textContent-only rendering (Pitfall 10). */
  PhotoAddForm.prototype.buildReviewRow = function (row, i, textbookMode) {
    var self = this;
    var tr = document.createElement('tr');
    tr.className = 'photo-row photo-row-' + row.status;
    tr.setAttribute('data-row-index', String(i));

    // In textbook mode an 已有 row is committable too — checking it does
    // NOT re-add the word (the server skips it anyway); it assigns the
    // card to the chosen textbook's lesson (US-32's multi-source).
    var committable = isCommittable(row, textbookMode);

    var checkCell = document.createElement('td');
    checkCell.className = 'preview-index';
    // Issue #18: the 44px touch floor rides a wrapping label
    // (.inline-check-hit) — the input itself stays 18px (Chrome zeroes
    // used padding on native checkboxes, so the input cannot carry the
    // hit area itself).
    var checkHit = document.createElement('label');
    checkHit.className = 'inline-check-hit';
    var check = document.createElement('input');
    check.type = 'checkbox';
    check.className = 'photo-row-check';
    check.id = 'parent-photo-check-' + i;
    check.checked = committable && row.checked !== false;
    check.disabled = !committable;
    check.setAttribute('aria-label', '加入 ' + (row.word || ('第 ' + (i + 1) + ' 行')));
    check.addEventListener('change', function () {
      self.rows[i].checked = check.checked;
      self.updateCommitButton();
      // Issue #81: the state line's schedulable counts ride the checked
      // set — keep it honest when the parent unchecks a row.
      self.updateStateLine();
    });
    checkHit.appendChild(check);
    checkCell.appendChild(checkHit);
    tr.appendChild(checkCell);

    var wordCell = document.createElement('td');
    var wordInput = document.createElement('input');
    wordInput.type = 'text';
    // Issue #81: the word face reads as the row's headline (photo-word-
    // input carries the weight); the cell stays td#2 for the e2e contract.
    wordInput.className = 'form-input photo-cell-input photo-word-input';
    wordInput.value = row.word;
    wordInput.setAttribute('aria-label', '单词 第 ' + (i + 1) + ' 行');
    wordInput.addEventListener('input', function () {
      self.rows[i].word = wordInput.value;
      self.onRowEdited();
    });
    wordCell.appendChild(wordInput);
    tr.appendChild(wordCell);

    var meaningCell = document.createElement('td');
    var meaningWrap = document.createElement('div');
    meaningWrap.className = 'photo-meaning-cell';
    var meaningInput = document.createElement('input');
    meaningInput.type = 'text';
    meaningInput.className = 'form-input photo-cell-input';
    meaningInput.value = row.meaning;
    meaningInput.setAttribute('aria-label', '释义 第 ' + (i + 1) + ' 行');
    // Issue #8: the 系统补充 marker rides the meaning cell (AC-2). The
    // display rule is meaningMarkerVisible's alone — a parent clearing
    // the text by keyboard hides the badge live (the source resets at
    // submit anyway: an empty gloss never stores 'generated').
    var meaningBadge = document.createElement('span');
    meaningBadge.className = 'meaning-source-badge';
    meaningBadge.textContent = '系统补充';
    meaningBadge.title = '释义由系统按教材语境补充，可编辑或取消；不会当作教材原文';
    meaningBadge.hidden = !global.meaningMarkerVisible(row);
    // AC-4 取消: clear the supplement, back to missing (retryable).
    var meaningCancel = document.createElement('button');
    meaningCancel.type = 'button';
    meaningCancel.className = 'meaning-cancel-btn';
    meaningCancel.textContent = '取消补充';
    meaningCancel.setAttribute('aria-label', '取消第 ' + (i + 1) + ' 行的系统补充释义');
    meaningCancel.title = '清空这条系统补充的释义，保留缺失（可再次补充）';
    meaningCancel.hidden = !global.meaningMarkerVisible(row);
    meaningCancel.addEventListener('click', function () {
      self.rows = global.cancelGeneratedMeaning(self.rows, i);
      meaningInput.value = '';
      meaningBadge.hidden = true;
      meaningCancel.hidden = true;
      self.onRowEdited();
    });
    meaningInput.addEventListener('input', function () {
      self.rows[i].meaning = meaningInput.value;
      meaningBadge.hidden = !global.meaningMarkerVisible(self.rows[i]);
      meaningCancel.hidden = meaningBadge.hidden;
      self.onRowEdited();
    });
    meaningWrap.appendChild(meaningInput);
    meaningWrap.appendChild(meaningBadge);
    meaningWrap.appendChild(meaningCancel);
    meaningCell.appendChild(meaningWrap);
    tr.appendChild(meaningCell);

    if (textbookMode) {
      // Per-row lesson (AC3: 逐条编辑仍然可用) — free text with the shared
      // datalist suggestions; empty means 待确认. The commit appends any
      // confirmed lesson the book's list lacks.
      var lessonCell = document.createElement('td');
      lessonCell.className = 'photo-lesson-cell';
      var lessonInput = document.createElement('input');
      lessonInput.type = 'text';
      lessonInput.className = 'form-input photo-cell-input photo-lesson-input';
      lessonInput.setAttribute('list', 'parent-photo-lesson-options');
      lessonInput.setAttribute('aria-label', '课次 第 ' + (i + 1) + ' 行');
      lessonInput.placeholder = '待确认';
      lessonInput.value = row.lesson || '';
      lessonInput.disabled = !committable;
      lessonInput.addEventListener('input', function () {
        self.rows[i].lesson = lessonInput.value;
        // Issue #81: the lesson feeds the state line's 本批课次 + progress
        // verdict — the line is pure text, so it syncs live without ever
        // rebuilding the table (the caret stays in this input).
        self.updateStateLine();
        self.updateCommitHint();
      });
      lessonCell.appendChild(lessonInput);
      tr.appendChild(lessonCell);
    }

    var statusCell = document.createElement('td');
    statusCell.className = 'preview-status photo-status photo-status-' + row.status;
    renderStatusCell(statusCell, row, textbookMode, this);
    tr.appendChild(statusCell);

    return tr;
  };

  /**
   * (Re)build one row's status cell content — the single writer for the
   * chip + note markup, used by both the initial render (buildReviewRow)
   * and the caret-preserving edit sync (onRowEdited). textContent only
   * (Pitfall 10) — except the Issue #6 conflict radios, the one control
   * that lives here (form is passed in so the change handler always
   * addresses the CURRENT row object, never a reclassify-stale one).
   */
  function renderStatusCell(statusCell, row, textbookMode, form) {
    while (statusCell.firstChild) {
      statusCell.removeChild(statusCell.firstChild);
    }
    var statusText = document.createElement('span');
    statusText.textContent = statusLabel(row);
    statusCell.appendChild(statusText);
    if (row.status === 'conflict' || row.status === 'exists') {
      var note = document.createElement('p');
      note.className = 'photo-status-note';
      note.textContent = row.status === 'conflict'
        ? '词库释义：' + (row.existingMeaning || '（空）') + (row.resolution ? '' : '，未确认前不提交')
        : (textbookMode ? '词库已有相同词条；勾选后仅指定课次，不重复添加' : '词库已有相同词条');
      statusCell.appendChild(note);
    }
    if (row.status === 'invalid') {
      var why = document.createElement('p');
      why.className = 'photo-status-note';
      why.textContent = describeInvalidReason(row.reason);
      statusCell.appendChild(why);
    }
    // Issue #8 AC-5: an unanswered supplement attempt is shown honestly —
    // the row stays submittable with its missing meaning (保留缺失), and
    // the affordance offers 重试. Manual text resolves the note.
    if (global.genFailedVisible && global.genFailedVisible(row)) {
      var genNote = document.createElement('p');
      genNote.className = 'photo-status-note photo-gen-failed-note';
      genNote.textContent = '释义生成失败，可重试或保留缺失';
      statusCell.appendChild(genNote);
    }
    if (row.status === 'conflict' && textbookMode && form) {
      // Issue #6 AC-3: the difference is already on display; the parent
      // confirms ONE reading, and only then does the row join the commit.
      var wrap = document.createElement('div');
      wrap.className = 'photo-conflict-choice';
      [
        { value: 'keep', label: '保留词库释义' },
        { value: 'adopt', label: '采用照片释义' },
      ].forEach(function (opt) {
        var lab = document.createElement('label');
        lab.className = 'photo-conflict-option';
        var input = document.createElement('input');
        input.type = 'radio';
        input.name = 'photo-conflict-' + row.index;
        input.value = opt.value;
        input.checked = row.resolution === opt.value;
        input.addEventListener('change', function () {
          if (!input.checked || !form || !form.rows) {
            return;
          }
          var current = form.rows[row.index];
          if (!current || current.status !== 'conflict') {
            return;
          }
          current.resolution = opt.value;
          current.checked = true;
          form.onRowEdited();
        });
        var span = document.createElement('span');
        span.textContent = opt.label;
        lab.appendChild(input);
        lab.appendChild(span);
        wrap.appendChild(lab);
      });
      statusCell.appendChild(wrap);
    }
  }

  /** Status chip label for one row. */
  function statusLabel(row) {
    switch (row.status) {
      case 'new':
        return '新词';
      case 'exists':
        return '已有';
      case 'conflict':
        return '释义不同';
      default:
        return '无法添加';
    }
  }

  /**
   * Edit follow-up WITHOUT a full re-render: a full re-render would rebuild
   * the inputs and steal the caret mid-word. The row models are already
   * updated by the caller; this syncs only the derived surfaces (status
   * cell, checkbox enablement, commit count).
   */
  PhotoAddForm.prototype.onRowEdited = function () {
    this.reclassify();
    var textbookMode = this.bookChoice !== 'none';
    var rowEls = this.preview.querySelectorAll('.photo-row');
    for (var i = 0; i < rowEls.length && i < this.rows.length; i++) {
      var row = this.rows[i];
      var tr = rowEls[i];
      tr.className = 'photo-row photo-row-' + row.status;

      var committable = isCommittable(row, textbookMode);
      var check = tr.querySelector('.photo-row-check');
      if (check) {
        check.disabled = !committable;
        check.checked = committable && row.checked !== false;
      }
      // The lesson input's enablement must follow the SAME committable
      // verdict — a resolved conflict becomes lesson-editable without a
      // full re-render (the caret-preserving reason this sync exists).
      var lessonEl = tr.querySelector('.photo-lesson-cell input');
      if (lessonEl) {
        lessonEl.disabled = !committable;
      }

      var statusCell = tr.querySelector('.photo-status');
      if (statusCell) {
        renderStatusCell(statusCell, row, textbookMode, this);
      }
    }
    this.updateCommitButton();
    // Issue #81: the state line's counts/verdicts follow the edit — text
    // only, the inputs keep the caret (same reason this sync exists).
    this.updateStateLine();
    // Issue #8: edits change the eligible/failed counts — the affordance
    // label follows without a full re-render (same caret-preserving reason
    // this sync exists).
    this.updateMeaningBar();
  };

  PhotoAddForm.prototype.updateCommitButton = function () {
    var label = this.commitBtn.querySelector('.btn-label');
    var n = this.bookChoice !== 'none' ? this.committedRows().length : this.submittableRows().length;
    // Issue #11/#13: checked poem and passage drafts ride the SAME
    // commit; the label names every kind honestly. Word-only drafts keep
    // the established label verbatim (确认添加（N 个） — specs pin it).
    var m = this.submittablePoems().length;
    var k = this.submittablePassages().length;
    if (label) {
      var parts = [];
      if (n > 0) {
        parts.push(n + ' 个');
      }
      if (m > 0) {
        parts.push(m + ' 首古诗');
      }
      if (k > 0) {
        parts.push(k + ' 篇课文');
      }
      label.textContent = parts.length
        ? '确认添加（' + parts.join(' · ') + '）'
        : '确认添加（0 个）';
    }
    this.commitBtn.disabled = n === 0 && m === 0 && k === 0;
    // Issue #81: the footer save-note counts exactly what this label is
    // about to commit — refresh them together.
    this.updateCommitHint();
  };

  PhotoAddForm.prototype.submittableRows = function () {
    return this.rows.filter(function (r) {
      return r.status === 'new' && r.checked !== false;
    });
  };

  /**
   * submittablePoems (Issue #11) — the poem drafts joining the commit:
   * checked, not invalid. An 'exists' poem is included too — the server
   * reports existing and never copies, and its attribution still applies
   * (the buildReviewRow exists-rows kin).
   */
  PhotoAddForm.prototype.submittablePoems = function () {
    return this.poemRows.filter(function (r) {
      return r.status !== 'invalid' && r.checked !== false;
    });
  };

  /**
   * submittablePassages (Issue #13) — the passage drafts joining the
   * commit: checked, not invalid. An 'exists' passage is included too —
   * the server reports existing and never copies, and its range and
   * attribution still apply. The range itself may be empty (text-only
   * save — the scope note on the card says so).
   */
  PhotoAddForm.prototype.submittablePassages = function () {
    return this.passageRows.filter(function (r) {
      return r.status !== 'invalid' && r.checked !== false;
    });
  };

  /**
   * runPassageCommit (Issue #13) — the checked passage drafts ride the
   * SAME confirm as the words and poems: POST /api/v1/passages in
   * maxPassageBatch-sized slices, per-passage outcomes aggregated, the
   * chosen practice range riding each draft (whole / paragraphs / absent
   * = text-only). NEVER throws: the receipt piece carries error instead,
   * so a passage failure can coexist honestly with landed words and
   * poems in one banner (independent surfaces).
   */
  PhotoAddForm.prototype.runPassageCommit = function (textbookId) {
    var passages = this.submittablePassages();
    if (!passages.length) {
      return Promise.resolve({ skipped: true, added: 0, existing: 0, results: [], submitted: [], error: null });
    }
    var self = this;
    var outcome = { skipped: false, added: 0, existing: 0, results: [], submitted: passages, error: null };
    var slices = global.chunkBatch
      ? global.chunkBatch(passages, MAX_PASSAGE_BATCH)
      : [passages];
    var chain = Promise.resolve();
    slices.forEach(function (part) {
      chain = chain.then(function () {
        return self.submitPassages({
          category: self.draftCategory,
          textbook_id: textbookId || 0,
          passages: part.map(function (r) {
            return {
              title: r.title, author: r.author, paragraphs: r.paragraphs,
              lesson: metaField(r.lesson),
              scope: r.scopeWhole
                ? { whole: true }
                : (r.scopeParagraphs.length ? { paragraphs: r.scopeParagraphs } : null),
            };
          }),
        }).then(function (result) {
          outcome.added += (result && result.added) || 0;
          outcome.existing += (result && result.existing) || 0;
          (result && Array.isArray(result.results) ? result.results : [])
            .forEach(function (row) { outcome.results.push(row); });
        });
      });
    });
    return chain.catch(function (err) {
      // A mid-run slice failure keeps what landed: the per-passage results
      // stay partial but the counts are the server's own, and the
      // un-attempted passages surface through the receipt's 未保存 note.
      outcome.error = (err && err.message) || '未知错误';
      return outcome;
    }).then(function () {
      return outcome;
    });
  };

  /**
   * runPoemCommit (Issue #11) — the checked poem drafts ride the SAME
   * confirm as the words: POST /api/v1/poems in maxPoemBatch-sized slices
   * (the submitChunked kin; a multi-page PDF run can exceed one call's
   * cap), per-poem outcomes aggregated. NEVER throws: the receipt piece
   * carries error instead, so a poem failure can coexist honestly with
   * landed words in one banner (independent surfaces — a failed poem
   * commit never rolls back, and never masks, the word outcome).
   * textbookId 0 = the supplementary path (no attribution).
   */
  PhotoAddForm.prototype.runPoemCommit = function (textbookId) {
    var poems = this.submittablePoems();
    if (!poems.length) {
      return Promise.resolve({ skipped: true, added: 0, existing: 0, results: [], submitted: [], error: null });
    }
    var self = this;
    var outcome = { skipped: false, added: 0, existing: 0, results: [], submitted: poems, error: null };
    var slices = global.chunkBatch
      ? global.chunkBatch(poems, MAX_POEM_BATCH)
      : [poems];
    var chain = Promise.resolve();
    slices.forEach(function (part) {
      chain = chain.then(function () {
        return self.submitPoems({
          category: self.draftCategory,
          textbook_id: textbookId || 0,
          poems: part.map(function (r) {
            return {
              title: r.title, author: r.author, lines: r.lines,
              lesson: metaField(r.lesson),
            };
          }),
        }).then(function (result) {
          outcome.added += (result && result.added) || 0;
          outcome.existing += (result && result.existing) || 0;
          (result && Array.isArray(result.results) ? result.results : [])
            .forEach(function (row) { outcome.results.push(row); });
        });
      });
    });
    return chain.catch(function (err) {
      // A mid-run slice failure keeps what landed: the per-poem results
      // stay partial but the counts are the server's own, and the
      // un-attempted poems surface through the receipt's 未保存 note.
      outcome.error = (err && err.message) || '未知错误';
      return outcome;
    }).then(function () {
      return outcome;
    });
  };

  /** Rows that take part in a textbook-mode commit: added (new),
   *  lesson-assigned (exists) and parent-RESOLVED conflicts (Issue #6);
   *  unresolved conflicts, invalid and unchecked rows never do. */
  PhotoAddForm.prototype.committedRows = function () {
    var textbookMode = this.bookChoice !== 'none';
    return this.rows.filter(function (r) {
      return isCommittable(r, textbookMode) && r.checked !== false;
    });
  };

  /**
   * Commit: re-classify (edits may have changed statuses), then either the
   * plain Issue #3 supplementary flow (no textbook chosen) or the Issue #5
   * textbook flow — resolve/create the textbook FIRST (nothing is written
   * if the attribution target cannot be established), add the checked new
   * rows through the ordinary add path, resolve the committed rows' card
   * ids from a REAL post-commit snapshot, and assign each (lesson,
   * includeInScope) group through the Issue #4 assign endpoint. Every
   * step's outcome lands in the receipt honestly.
   */
  PhotoAddForm.prototype.handleCommit = function () {
    if (this.commitBtn.disabled) {
      return;
    }
    var self = this;
    var kit = global.__parentFormKit || {};
    this.showFormError('');
    this.reclassify();

    if (this.bookChoice !== 'none' && this.textbookDepsReady()) {
      this.handleTextbookCommit();
      return;
    }

    var rows = this.submittableRows();
    // Issue #11/#13: the checked poems and passages ride the SAME
    // confirm; a poems-only or passages-only draft is a legal commit, so
    // emptiness is judged over every kind.
    var poems = this.submittablePoems();
    var passages = this.submittablePassages();
    if (!rows.length && !poems.length && !passages.length) {
      this.showFormError('没有可添加的新内容，请勾选或编辑后再确认');
      this.updateCommitButton();
      return;
    }

    if (rows.length && !global.chunkBatch) {
      this.showFormError('页面脚本未完整加载，请刷新后重试');
      return;
    }

    this.setCommitBusy(true);
    // Issue #7: a whole textbook can far exceed one add call's entry cap —
    // submitChunked slices sequentially and aggregates. A mid-run slice
    // failure keeps the already-added slices: the receipt reconciles per
    // word from the real snapshot, and the rows stay for a plain
    // re-confirm (the server skips what a retry finds already present).
    var failedWith = null;
    var wordsRun = rows.length
      ? submitChunked(this, rows)
        .catch(function (err) {
          failedWith = err;
          return err.partialOutcome || { added: 0, skipped: 0 };
        })
      : Promise.resolve({ added: 0, skipped: 0 });
    wordsRun
      .then(function (outcome) {
        // Issue #11/#13: the poem and passage commits ride AFTER the
        // words settle — independent surfaces, one receipt (a failure on
        // either keeps the draft for a retry exactly like a word slice
        // failure does).
        return self.runPoemCommit(0).then(function (poemReceipt) {
          return self.runPassageCommit(0).then(function (passageReceipt) {
            return { words: outcome, poem: poemReceipt, passage: passageReceipt };
          });
        });
      })
      .then(function (run) {
        var outcome = run.words;
        // Reconcile on skips (a raced duplicate) OR a mid-run slice
        // failure (what actually landed matters). A failed snapshot fetch
        // resolves null — "unavailable", NOT an empty library — so a
        // network blip can never reclassify server-confirmed adds as
        // failures (honesty cuts both ways).
        var reconcile = (outcome.skipped || 0) > 0 || failedWith
          ? self.fetchWords(self.draftCategory).catch(function () { return null; })
          : Promise.resolve(null);
        return reconcile.then(function (fresh) {
          if (fresh) {
            self.existingWords = fresh;
          }
          self.renderCommitResult(outcome, rows, fresh, run.poem, run.passage);
          // Any surface failed (word slices, poems or passages) keeps the
          // whole draft — landed parts are never lost, the re-confirm
          // retries what remains (the server skips what it already has).
          var poemFailed = run.poem && run.poem.error;
          var passageFailed = run.passage && run.passage.error;
          if (failedWith || poemFailed || passageFailed) {
            var message = failedWith && failedWith.message
              ? failedWith.message : (poemFailed || passageFailed || '未知错误');
            if (kit.showToast) {
              kit.showToast('部分添加失败：' + message + '。已添加的部分不会丢失，请重试未完成的部分。', 'error');
            } else {
              self.showFormError('部分添加失败：' + message + '。已添加的部分不会丢失，请重试未完成的部分。');
            }
            return null;
          }
          self.onCommitted(outcome);
          // Reset for the next photo; category note and unit retained.
          self.resetAfterCommit();
        });
      })
      .catch(function (err) {
        // The unexpected (render/parse) path — the submit failures were
        // already handled above; this never masks them.
        var message = (err && err.message) ? err.message : '未知错误';
        self.showFormError('保存失败：' + message + '。请检查后重试。');
      })
      .then(function () {
        self.setCommitBusy(false);
      });
  };

  /** Shared post-success reset (both commit paths land here). */
  PhotoAddForm.prototype.resetAfterCommit = function () {
    this.currentFile = null;
    this.fileInput.value = '';
    this.releasePdf();
    this.clearPickUI();
    this.resetEntries();
  };

  /**
   * The Issue #5 commit. Sequential steps, each able to fail without
   * pretending the later ones ran:
   *   1. resolveTargetBook — create the new textbook (with the CONFIRMED
   *      lessons) or append the missing lessons to the chosen one. A
   *      failure here writes NOTHING anywhere (fail before any word add —
   *      a supplementary add would silently change selection behavior).
   *   2. submitWords — the checked NEW rows only (the ordinary #3 path;
   *      exists rows are already in the library).
   *   3. fetchWords — the REAL post-commit snapshot: raced-duplicate
   *      reconciliation AND the card ids the assignments need. null ⇒
   *      assignments are skipped with their reason, never guessed.
   *   4. assignWords per (lesson, includeInScope) group — a group failure
   *      does not stop the others; the receipt names every word and the
   *      词库批量指定 modal remains the retry path.
   */
  PhotoAddForm.prototype.handleTextbookCommit = function () {
    var self = this;

    var committed = this.committedRows();
    // Issue #11/#13: the checked poems and passages ride the same
    // textbook commit (their own lesson inputs); a poems-only or
    // passages-only commit is legal.
    var poems = this.submittablePoems();
    var passages = this.submittablePassages();
    if (!committed.length && !poems.length && !passages.length) {
      this.showFormError('没有勾选任何内容，请勾选后再确认');
      this.updateCommitButton();
      return;
    }
    if (poems.length && !this.submitPoems) {
      this.showFormError('本页暂不支持保存古诗（古诗接口未接入），请取消勾选古诗后再确认');
      return;
    }
    if (passages.length && !this.submitPassages) {
      this.showFormError('本页暂不支持保存课文（课文接口未接入），请取消勾选课文后再确认');
      return;
    }
    if (committed.length && !global.chunkBatch) {
      this.showFormError('页面脚本未完整加载，请刷新后重试');
      return;
    }
    // Issue #7: a whole textbook exceeds one call's caps — the add and
    // assign steps slice themselves (ADR-0004 #6); the parent confirms
    // ONCE, the slices run sequentially inside the commit.
    var createValues = null;
    if (this.bookChoice === 'new') {
      createValues = this.readCreateForm();
      if (!createValues.name) {
        this.showFormError('请先填写教材名称（缺失的教材信息只需这一次确认）');
        return;
      }
    }

    this.setCommitBusy(true);
    // established tracks the step-1 result so a LATER step's failure can
    // still name what already persisted (per-step honesty: a partial
    // commit never renders as a plain failure with unreported writes).
    var established = null;
    this.resolveTargetBook(committed, createValues, poems, passages)
      .then(function (book) {
        established = book;
        return self.addAndAssign(book, committed);
      })
      .then(function (receipt) {
        // Issue #11/#13: the poems and passages commit AFTER the words
        // land (their attribution needs the established book id). A
        // failure throws with its flag — the catch keeps the draft and
        // re-points the panel at the established book, exactly like a
        // word-step failure (landed words are never lost; the retry
        // re-commits them as 已有).
        return self.runPoemCommit(receipt.book.id).then(function (poemReceipt) {
          if (poemReceipt.error) {
            var err = new Error('古诗保存失败：' + poemReceipt.error);
            err.poemFailure = true;
            err.partialOutcome = receipt.addOutcome || { added: 0, skipped: 0 };
            throw err;
          }
          receipt.poemReceipt = poemReceipt;
          return self.runPassageCommit(receipt.book.id).then(function (passageReceipt) {
            if (passageReceipt.error) {
              var perr = new Error('课文保存失败：' + passageReceipt.error);
              perr.passageFailure = true;
              perr.partialOutcome = receipt.addOutcome || { added: 0, skipped: 0 };
              throw perr;
            }
            receipt.passageReceipt = passageReceipt;
            return receipt;
          });
        });
      })
      .then(function (receipt) {
        self.renderTextbookCommitResult(receipt);
        self.onCommitted(receipt.addOutcome || {});
        // The textbook list changed (created/appended lessons/backfilled
        // meta) — reload it for the next recognition, then reset the flow.
        if (self.fetchTextbooks) {
          self.fetchTextbooks(self.draftCategory).catch(function () { return null; }).then(function (books) {
            if (books) {
              self.books = books;
            }
            self.resetAfterCommit();
          });
        } else {
          self.resetAfterCommit();
        }
      })
      .catch(function (err) {
        var message = (err && err.message) ? err.message : '未知错误';
        // Issue #7: a mid-slice add failure leaves earlier slices added —
        // say so instead of a flat 保存失败.
        var partial = err && err.partialOutcome && err.partialOutcome.added
          ? '（已添加 ' + err.partialOutcome.added + ' 个，不会丢失）' : '';
        if (!established) {
          self.reportCommitError('保存失败：' + message + partial + '。请检查后重试。');
          return;
        }
        // The book (and possibly its lesson list) DID land; the retry must
        // REUSE it, not create a same-named duplicate — re-point the panel
        // at the established book and say what persisted.
        var reloaded = self.fetchTextbooks
          ? self.fetchTextbooks(self.draftCategory).catch(function () { return null; })
          : Promise.resolve(null);
        reloaded.then(function (books) {
          if (books) {
            self.books = books;
            self.renderTextbookPanel('');
            self.bookChoice = established.id;
            if (self.bookDetailHost) {
              self.renderTextbookDetail(self.bookDetailHost);
            }
            self.renderPreview();
          }
          self.reportCommitError('教材「' + established.name +
            '」已建立（课次表已更新），但 ' + message + partial +
            '。已保存的部分不会丢失；请直接重试。');
        });
      })
      .then(function () {
        self.setCommitBusy(false);
      });
  };

  /** The commit-failure surface: toast when the kit has one, else the
   *  inline form error. */
  PhotoAddForm.prototype.reportCommitError = function (message) {
    var kit = global.__parentFormKit || {};
    if (kit.showToast) {
      kit.showToast(message, 'error');
    } else {
      this.showFormError(message);
    }
  };

  /**
   * Step 1 — establish the attribution target BEFORE any word is written.
   * 'new' creates the textbook with the CONFIRMED lesson set (distinct
   * non-empty row lessons, row order). An existing book gets two kinds of
   * recognized-but-missing detail written back in ONE update: its missing
   * confirmed lessons append (order fixable in 教材管理 later), and header
   * fields the photo DID read but the book never had backfill (AC2's
   * 自动填写 — existing values are never overwritten). Both paths fail the
   * whole commit on error — a supplementary add would silently change
   * what selection can reach.
   */
  PhotoAddForm.prototype.resolveTargetBook = function (committed, createValues, poemRows, passageRows) {
    var self = this;
    var finalLessons = distinctRowLessons(committed);
    // Issue #11/#13: the poems' and passages' own confirmed lessons join
    // the book's lesson set (a poems-only or passages-only page still
    // establishes its 课次 table).
    (poemRows || []).forEach(function (r) {
      var lesson = metaField(r && r.lesson);
      if (lesson && finalLessons.indexOf(lesson) === -1) {
        finalLessons.push(lesson);
      }
    });
    (passageRows || []).forEach(function (r) {
      var lesson = metaField(r && r.lesson);
      if (lesson && finalLessons.indexOf(lesson) === -1) {
        finalLessons.push(lesson);
      }
    });

    if (this.bookChoice === 'new') {
      var payload = {
        name: createValues.name,
        // Issue #10: the new book is filed under the DRAFT's subject — a
        // 语文 import creates a 语文 textbook, never an English one.
        category: this.draftCategory,
        lessons: finalLessons,
      };
      META_FIELDS.forEach(function (meta) {
        if (createValues[meta.key]) {
          payload[meta.key] = createValues[meta.key];
        }
      });
      return this.createTextbook(payload).catch(function (err) {
        err.message = '教材创建失败：' + (err.message || '未知错误');
        throw err;
      });
    }

    var book = this.chosenBook();
    if (!book) {
      return Promise.reject(new Error('所选教材不存在，请重新选择'));
    }
    var inList = {};
    (book.lessons || []).forEach(function (name) {
      inList[name] = true;
    });
    var missing = finalLessons.filter(function (name) {
      return !inList[name];
    });
    // Backfill ONLY the fields the photo read and the book lacks.
    var patch = {};
    META_FIELDS.forEach(function (meta) {
      var recognized = self.detectedMeta ? metaField(self.detectedMeta[meta.key]) : '';
      if (recognized && !metaField(book[meta.key])) {
        patch[meta.key] = recognized;
      }
    });
    if (!missing.length && !META_KEYS.some(function (key) {
      return Object.prototype.hasOwnProperty.call(patch, key);
    })) {
      return Promise.resolve(book);
    }
    if (missing.length) {
      patch.lessons = distinctLessons((book.lessons || []).concat(missing));
    }
    return this.updateTextbook(book.id, patch).then(function (updated) {
      // Keep the in-memory book current for the receipt's progress hint.
      if (updated && updated.lessons) {
        book.lessons = updated.lessons;
        book.learnedLessons = updated.learnedLessons;
      } else if (patch.lessons) {
        book.lessons = patch.lessons;
      }
      META_KEYS.forEach(function (key) {
        if (patch[key]) {
          book[key] = patch[key];
        }
      });
      return book;
    }).catch(function (err) {
      err.message = '教材信息更新失败：' + (err.message || '未知错误');
      throw err;
    });
  };

  /**
   * Steps 2–5 — add the new rows, snapshot the truth, apply the parent's
   * 采用照片释义 edits, assign the groups. Returns the receipt pieces for
   * renderTextbookCommitResult.
   */
  PhotoAddForm.prototype.addAndAssign = function (book, committed) {
    var self = this;
    var addRows = committed.filter(function (r) {
      return r.status === 'new';
    });
    var adoptRows = committed.filter(function (r) {
      return r.status === 'conflict' && r.resolution === 'adopt';
    });

    // Issue #7: the new rows go out sliced and aggregated (a whole
    // textbook in one confirm); a mid-run failure rejects with
    // partialOutcome (submitChunked's contract).
    var addPromise = addRows.length
      ? submitChunked(self, addRows).catch(function (err) {
        err.message = '词汇添加失败：' + (err.message || '未知错误');
        throw err;
      })
      : Promise.resolve({ added: 0, skipped: 0 });

    return addPromise.then(function (outcome) {
      outcome = outcome || { added: 0, skipped: 0 };
      // The snapshot serves reconciliation (raced duplicates) AND card-id
      // resolution for the edits and assignments — one fetch, three
      // honest uses (and the sticky-include lookup rides it too).
      return self.fetchWords(self.draftCategory).catch(function () { return null; })
        .then(function (fresh) {
          if (fresh) {
            self.existingWords = fresh;
          }
          var idByWord = {};
          (fresh || []).forEach(function (w) {
            if (w && w.word) {
              idByWord[String(w.word).trim()] = w.id;
            }
          });
          return self.runAdoptEdits(adoptRows, idByWord).then(function (editResults) {
            var groups = planAssignments(committed, fresh || [], book.id, self.includePending);
            var unassigned = committed.filter(function (r) {
              return !Object.prototype.hasOwnProperty.call(idByWord, r.word);
            }).map(function (r) { return r.word; });

            return self.runAssignments(book, groups).then(function (results) {
              // Issue #81 P2: the receipt's 默写安排 verdict must come
              // from the POST-SAVE truth — the assigns just rewrote the
              // entries, and the books' progress may have moved since the
              // client cached it. Re-read both (null on failure ⇒ the
              // receipt degrades to the assign payloads, never a guess).
              var postWords = self.fetchWords(self.draftCategory)
                .catch(function () { return null; });
              var postBooks = self.fetchTextbooks
                ? self.fetchTextbooks(self.draftCategory)
                  .catch(function () { return null; })
                : Promise.resolve(null);
              return Promise.all([postWords, postBooks]).then(function (after) {
                if (after[0]) {
                  self.existingWords = after[0];
                }
                return {
                  book: book,
                  addOutcome: outcome,
                  submittedRows: addRows,
                  adoptRows: adoptRows,
                  editResults: editResults,
                  fresh: fresh,
                  groups: groups,
                  groupResults: results,
                  unassigned: unassigned,
                  postSave: after[0],
                  booksAfter: after[1],
                };
              });
            });
          });
        });
    });
  };

  /**
   * 采用照片释义 edits — sequential meaning-only PUT /words/{id} calls
   * (the D-07 edit path: text changes never touch FSRS state, so the
   * confirmed reading replaces the meaning while the learning history
   * stays). One row's failure fails only itself — the receipt says so.
   */
  PhotoAddForm.prototype.runAdoptEdits = function (adoptRows, idByWord) {
    var self = this;
    var results = [];
    var chain = Promise.resolve();
    adoptRows.forEach(function (row) {
      chain = chain.then(function () {
        var id = Object.prototype.hasOwnProperty.call(idByWord, row.word)
          ? idByWord[row.word] : null;
        if (!id) {
          results.push({ word: row.word, ok: false, error: '词库读取失败，未能更新释义' });
          return null;
        }
        if (!self.editWord) {
          results.push({ word: row.word, ok: false, error: '编辑接口不可用' });
          return null;
        }
        return self.editWord(id, { word: row.word, meaning: row.meaning }).then(function () {
          results.push({ word: row.word, ok: true });
        }).catch(function (err) {
          results.push({ word: row.word, ok: false, error: (err && err.message) || '未知错误' });
        });
      });
    });
    return chain.then(function () { return results; });
  };

  // ------------------------------------------------------------------
  // Issue #8 — 缺失英语释义按教材语境补充（ADR-0005）
  // ------------------------------------------------------------------

  /**
   * The CONFIRMED context one supplement call carries (AC-3): an existing
   * book's stored meta + its 已学进度 gauge; the 新建教材 mini-form's
   * confirmed values with NO progress (尚未登记 — never faked as 0).
   * 'none' (补充词汇) has no textbook context to ground on → null, and
   * the affordance never mounts for it (ADR-0005 option E).
   */
  PhotoAddForm.prototype.meaningContext = function () {
    if (this.bookChoice === 'none') {
      return null;
    }
    if (this.bookChoice === 'new') {
      var values = this.readCreateForm();
      var textbook = {};
      META_FIELDS.forEach(function (meta) {
        if (values[meta.key]) {
          textbook[meta.key] = values[meta.key];
        }
      });
      return { textbook: textbook, learnedLessons: null };
    }
    var book = this.chosenBook();
    if (!book) {
      return null;
    }
    return {
      textbook: {
        edition: book.edition || '',
        stage: book.stage || '',
        grade: book.grade || '',
        volume: book.volume || '',
        module: book.module || '',
      },
      learnedLessons: typeof book.learnedLessons === 'number' ? book.learnedLessons : null,
    };
  };

  /**
   * Render the supplement strip. Hidden unless: the wiring exists, a
   * recognition is on display, a textbook context is confirmed and
   * eligible (still-missing) words exist. The label switches to
   * 重试生成 once any previous attempt went unanswered; the note names
   * the unanswered count honestly (AC-5).
   */
  PhotoAddForm.prototype.renderMeaningBar = function () {
    var self = this;
    this.meaningBar.hidden = true;
    clearEl(this.meaningBar);
    this.meaningGenBtn = null;
    this.meaningGenLabel = null;
    // Issue #10: the supplement generates ENGLISH-style glosses — a 语文
    // draft's meaning column is the material's own pinyin/annotation
    // (empty is legal, 不强制英语式释义), so the affordance never mounts
    // for a Chinese draft.
    if (this.draftCategory === 'chinese') {
      return;
    }
    if (!this.generateMeanings || !global.supplementWords || !this.rows.length) {
      return;
    }
    var context = this.meaningContext();
    if (!context) {
      return;
    }
    var summary = global.meaningGenSummary(this.rows);
    if (!summary.target) {
      // Nothing to send — every previously-unanswered row either got its
      // meaning (from a later slice or by hand) or was resolved; the
      // per-row 生成失败 notes have already cleared with the meanings.
      return;
    }

    var bar = document.createElement('div');
    bar.className = 'photo-meaning-bar-inner';
    var btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'btn btn-secondary photo-meaning-gen-btn';
    var label = document.createElement('span');
    label.className = 'btn-label';
    this.meaningGenBtn = btn;
    this.meaningGenLabel = label;
    btn.appendChild(label);
    btn.addEventListener('click', function () {
      self.handleGenerateMeanings();
    });
    bar.appendChild(btn);

    var hint = document.createElement('span');
    hint.className = 'photo-meaning-hint';
    hint.textContent = summary.failed
      ? '按已确认教材与进度补充缺失释义；上次有 ' + summary.failed + ' 词未答出'
      : '按已确认教材与已学进度补充缺失释义，标明「系统补充」，可编辑或取消';
    bar.appendChild(hint);

    this.meaningBar.appendChild(bar);
    this.meaningBar.hidden = false;
    this.updateMeaningBar();
  };

  /**
   * In-place affordance sync (the updateCommitButton kin): label count,
   * busy state. Called from onRowEdited so keystrokes keep the count live
   * without rebuilding the strip.
   */
  PhotoAddForm.prototype.updateMeaningBar = function () {
    if (!this.meaningGenBtn || !this.meaningGenLabel) {
      return;
    }
    var summary = global.meaningGenSummary(this.rows);
    this.meaningGenLabel.textContent = this.meaningBusy
      ? '生成中…'
      : (summary.failed ? '重试生成（' + summary.target + ' 词）' : '补充缺失释义（' + summary.target + ' 词）');
    this.meaningGenBtn.disabled = this.meaningBusy || !summary.target;
  };

  /**
   * One supplement run: the still-missing eligible words go out in
   * MAX_BATCH_ENTRIES-sized sequential slices (ADR-0005 #6 — a whole
   * textbook supplements without the parent splitting it), each reply
   * folds in as it lands, and an HTTP failure stops the run honestly
   * naming what already applied (the submitChunked partialOutcome kin).
   */
  PhotoAddForm.prototype.handleGenerateMeanings = function () {
    var self = this;
    if (this.meaningBusy || !this.generateMeanings || !global.chunkBatch) {
      return;
    }
    var context = this.meaningContext();
    var words = global.supplementWords(this.rows);
    if (!context || !words.length) {
      return;
    }

    this.meaningBusy = true;
    this.updateMeaningBar();
    this.showFormError('');

    var applied = 0;
    var failed = 0;
    var slices = global.chunkBatch(words, MAX_BATCH_ENTRIES);
    var appliedSlices = 0;
    var chain = Promise.resolve();
    slices.forEach(function (part) {
      chain = chain.then(function () {
        // Re-read the confirmed context PER SLICE: the textbook select
        // stays live during a multi-slice run, and each slice's prompt
        // must be truthful about its own inputs — never a context the
        // parent already switched away from (code-review finding).
        var sliceContext = self.meaningContext();
        if (!sliceContext) {
          // The parent switched to 不指定教材 mid-run: no confirmed
          // context grounds further slices — stop honestly (the catch
          // marks the un-run slices and names what already applied).
          throw new Error('教材归属已改为不指定，已停止补充');
        }
        return self.generateMeanings({
          words: part,
          textbook: sliceContext.textbook,
          learnedLessons: sliceContext.learnedLessons,
        }).then(function (reply) {
          self.rows = global.applyGeneratedMeanings(self.rows, reply || {});
          applied += Object.keys((reply && reply.meanings) || {}).length;
          failed += ((reply && reply.missing) || []).length;
          appliedSlices++;
          // Fold each slice in as it lands so a later failure never
          // discards earlier answers.
          self.renderPreview();
        });
      });
    });

    chain.then(function () {
      self.meaningBusy = false;
      self.renderPreview();
      if (failed) {
        self.showFormError('有 ' + failed + ' 个词未生成释义（已保留缺失，可重试或提交）');
      }
    }).catch(function (err) {
      // Words whose slices never got a reply carry the same honest
      // per-row failure flag a reply.missing word does — the affordance
      // switches to 重试生成 and the rows stay submittable (ADR-0005 #5).
      var unanswered = [];
      for (var i = appliedSlices; i < slices.length; i++) {
        unanswered = unanswered.concat(slices[i]);
      }
      self.rows = global.markGenerationFailed(self.rows, unanswered);
      self.meaningBusy = false;
      self.renderPreview();
      var partial = applied ? '（已补充 ' + applied + ' 个，不会丢失）' : '';
      self.showFormError('释义生成失败：' + ((err && err.message) || '未知错误') + partial + '。可重试，或保留缺失直接提交。');
    });
  };

  /**
   * Step 4 — run the assign calls sequentially, keeping each outcome.
   * Issue #7: a group can carry a whole textbook's cards, so each
   * (lesson, includeInScope) group is further sliced into
   * MAX_ASSIGN_IDS-sized calls (the server's batch-ids cap) — the receipt
   * keeps one outcome per slice with the slice's own words.
   */
  PhotoAddForm.prototype.runAssignments = function (book, groups) {
    var self = this;
    var results = [];
    var chain = Promise.resolve();
    groups.forEach(function (group) {
      var pairs = (group.cardIds || []).map(function (id, i) {
        return { id: id, word: (group.words || [])[i] || '' };
      });
      global.chunkBatch(pairs, MAX_ASSIGN_IDS).forEach(function (part) {
        // One receipt entry per slice, carrying the slice's own words.
        var pushResult = function (ok, extra) {
          var entry = {
            group: {
              lesson: group.lesson,
              includeInScope: group.includeInScope,
              words: part.map(function (p) { return p.word; }),
            },
            ok: ok,
          };
          Object.keys(extra || {}).forEach(function (key) {
            entry[key] = extra[key];
          });
          results.push(entry);
        };
        chain = chain.then(function () {
          return self.assignWords(book.id, {
            cardIds: part.map(function (p) { return p.id; }),
            lesson: group.lesson,
            includeInScope: group.includeInScope,
          }).then(function (result) {
            pushResult(true, { result: result });
          }).catch(function (err) {
            pushResult(false, { error: (err && err.message) || '未知错误' });
          });
        });
      });
    });
    return chain.then(function () { return results; });
  };

  PhotoAddForm.prototype.setCommitBusy = function (busy) {
    var kit = global.__parentFormKit || {};
    if (kit.setLoading) {
      kit.setLoading(this.commitBtn, busy);
    } else {
      this.commitBtn.disabled = busy;
    }
    this.recognizeBtn.disabled = busy || !this.currentFile;
    this.discardBtn.disabled = busy;
  };

  /**
   * Per-word add outcomes shared by both receipts (the #3 supplementary
   * banner and the #5 textbook banner). Three honesty modes:
   *   - no skips: every submitted word was added (server-confirmed).
   *   - skips + snapshot (fresh): per-word truth re-read from the library
   *     (added / 已有 with the original kept / genuinely failed).
   *   - skips but snapshot unavailable (fresh === null): the server's
   *     counts stand and the skipped words are reported as UNVERIFIED —
   *     never silently upgraded to added, never falsely named failures.
   */
  function buildAddOutcomeLists(outcome, submittedRows, fresh) {
    var byWord = {};
    if (fresh) {
      (fresh || []).forEach(function (w) {
        if (w && w.word) {
          byWord[String(w.word).trim()] = w.meaning == null ? '' : String(w.meaning).trim();
        }
      });
    }
    var added = [];
    var existed = [];
    var failed = [];
    var unverified = [];
    var raced = (outcome && outcome.skipped || 0) > 0;
    submittedRows.forEach(function (r) {
      if (!raced) {
        added.push(r.word);
        return;
      }
      if (!fresh) {
        // Snapshot unavailable: the server says outcome.added of these were
        // added, but not WHICH. Naming any word a failure would be a guess.
        unverified.push(r.word);
        return;
      }
      if (Object.prototype.hasOwnProperty.call(byWord, r.word)) {
        if (byWord[r.word] === r.meaning.trim()) {
          added.push(r.word); // raced to an identical entry — now in the library
        } else {
          existed.push(r.word); // raced to a DIFFERENT meaning — original kept
        }
      } else {
        failed.push(r.word);
      }
    });
    return { added: added, existed: existed, failed: failed, unverified: unverified, raced: raced };
  }

  /**
   * buildTextbookReceipt — the Issue #6 per-item commit receipt. Buckets
   * every preview row into exactly one category:
   *   'added'       新增 — the word entered the library (verified or
   *                  server-confirmed) and its source merged
   *   'merged'      合并 — an already-present word whose source merged
   *                  (exists, 保留/采用 resolution, raced-to-different)
   *   'pendingDiff' 待处理差异 — a conflict the parent did NOT resolve
   *   'failed'      失败 — something the parent must fix (add/edit/assign
   *                  step failed, or the card could not be resolved); the
   *                  note always admits what DID persist
   *   'unverified'  未核对 — a raced add the (unavailable) snapshot could
   *                  not verify — never guessed up or down
   *   'excluded'    未提交 — invalid or unchecked rows (never failures)
   * summary is BY CONSTRUCTION the tally of items (AC-6: 汇总与实际结果
   * 一致). Pure — the caller supplies only server-truth pieces.
   */
  function buildTextbookReceipt(input) {
    var edited = {};
    (input.editResults || []).forEach(function (e) {
      if (e && e.word) {
        edited[e.word] = e;
      }
    });
    var assignFailed = {};
    (input.groupResults || []).forEach(function (r) {
      if (r && !r.ok && r.group) {
        (r.group.words || []).forEach(function (w) {
          assignFailed[w] = { lesson: r.group.lesson || '', error: r.error || '未知错误' };
        });
      }
    });
    var snapMeaning = {};
    (input.fresh || []).forEach(function (w) {
      if (w && w.word) {
        snapMeaning[String(w.word).trim()] = w.meaning == null ? '' : String(w.meaning).trim();
      }
    });
    var raced = !!(input.addOutcome && input.addOutcome.skipped);
    var submitted = {};
    (input.addRows || []).forEach(function (r) {
      if (r && r.word) {
        submitted[r.word] = true;
      }
    });
    var unassigned = {};
    (input.unassigned || []).forEach(function (w) {
      unassigned[w] = true;
    });

    var assignFailNote = function (word, prefix) {
      var f = assignFailed[word] || {};
      return (prefix || '') + '课次' + (f.lesson ? '「' + f.lesson + '」' : '') +
        '指定失败：' + (f.error || '未知错误') + '——可在词库中重试';
    };

    var items = [];
    (input.rows || []).forEach(function (row) {
      var item = { word: row.word, category: '', note: '' };
      var meaning = String(row.meaning == null ? '' : row.meaning).trim();
      if (row.status === 'invalid') {
        item.category = 'excluded';
        item.note = describeInvalidReason(row.reason);
      } else if (row.status === 'conflict' && !isResolved(row)) {
        item.category = 'pendingDiff';
        item.note = '词库释义「' + (row.existingMeaning || '（空）') + '」与照片释义「' +
          meaning + '」不同，未确认——已保留原释义，仅报告差异';
      } else if (row.checked === false) {
        item.category = 'excluded';
        item.note = '未勾选';
      } else if (row.status === 'new') {
        if (raced && !input.fresh) {
          item.category = 'unverified';
          item.note = '已提交，但词库读取失败，未能核对实际结果';
        } else if (raced && !Object.prototype.hasOwnProperty.call(snapMeaning, row.word)) {
          item.category = 'failed';
          item.note = '未添加成功，请重试';
        } else if (raced && snapMeaning[row.word] !== meaning) {
          if (assignFailed[row.word]) {
            item.category = 'failed';
            item.note = assignFailNote(row.word, '并发已存在不同释义，保留原释义；');
          } else {
            item.category = 'merged';
            item.note = '并发已存在不同释义，保留原释义，仅合并来源';
          }
        } else if (assignFailed[row.word]) {
          item.category = 'failed';
          item.note = assignFailNote(row.word, '词条已保存；');
        } else if (unassigned[row.word]) {
          item.category = 'failed';
          item.note = '词条已保存，但词库读取失败，未能指定课次——可在词库中补指定';
        } else {
          item.category = 'added';
          item.note = '已添加并指定课次';
        }
      } else if (row.status === 'conflict' && row.resolution === 'adopt') {
        var edit = edited[row.word];
        if (edit && !edit.ok) {
          item.category = 'failed';
          item.note = '释义更新失败：' + (edit.error || '未知错误');
        } else if (assignFailed[row.word]) {
          item.category = 'failed';
          item.note = assignFailNote(row.word, '释义已按照片更新；');
        } else {
          item.category = 'merged';
          item.note = '采用照片释义更新，已合并来源';
        }
      } else if (unassigned[row.word]) {
        item.category = 'failed';
        item.note = '词库读取失败，未能指定课次——可在词库中补指定';
      } else if (assignFailed[row.word]) {
        item.category = 'failed';
        item.note = assignFailNote(row.word);
      } else if (row.status === 'conflict') {
        item.category = 'merged';
        item.note = '保留词库释义，仅合并来源';
      } else {
        item.category = 'merged';
        item.note = '词库已有，仅指定课次';
      }
      items.push(item);
    });

    var summary = { added: 0, merged: 0, pendingDiff: 0, failed: 0, unverified: 0, excluded: 0 };
    items.forEach(function (item) {
      if (Object.prototype.hasOwnProperty.call(summary, item.category)) {
        summary[item.category] += 1;
      }
    });
    return { items: items, summary: summary };
  }

  /**
   * summarizeAssignScope (Issue #81) — the 默写安排 half of the textbook
   * receipt, derived from the ACTUAL assign results (ok groups only: a
   * failed group's words are already 失败 under 保存结果 and are never
   * counted as scheduled here). With a post-save snapshot the verdict is
   * computed PER WORD from the saved entries and EVERY source, mirroring
   * the server's scope rule (card_repo's ScopeEligibleOnly twin):
   * participates × (includeInScope on ANY textbook ∨ a lesson inside ANY
   * textbook's learned prefix ∨ no attribution at all). Without one
   * (snapshot fetch failed) it degrades to the assign payloads — still
   * honoring an explicit includeInScope for ANY lesson shape. Pure.
   */
  function summarizeAssignScope(book, groupResults, postSave, books) {
    var lessons = (book && book.lessons) || [];
    var learned = (book && book.learnedLessons) || 0;
    var out = {
      schedulable: 0, joined: 0, heldUnlearned: 0, heldPending: 0, paused: 0,
      unlearnedNames: [],
    };
    // The learned prefixes as the SERVER has them now (post-save), keyed
    // by textbook id — the receipt must not judge eligibility by the
    // client's possibly stale pre-save copy of the target book.
    var learnedByBook = {};
    (books || []).forEach(function (b) {
      if (!b || !b.id) {
        return;
      }
      var set = {};
      (b.lessons || []).slice(0, Math.max(0, b.learnedLessons || 0))
        .forEach(function (name) { set[name] = true; });
      learnedByBook[b.id] = set;
    });
    if (book && book.id && !learnedByBook[book.id]) {
      var target = {};
      lessons.slice(0, Math.max(0, learned)).forEach(function (name) {
        target[name] = true;
      });
      learnedByBook[book.id] = target;
    }
    var byWord = {};
    (postSave || []).forEach(function (w) {
      if (w && w.word) {
        byWord[String(w.word).trim()] = w;
      }
    });

    var verdictFromPayload = function (group, lesson) {
      // Degraded truth: an explicit include joins the scope regardless of
      // the lesson's shape or progress (the pre-fix code ignored it for
      // non-empty lessons).
      if (group.includeInScope) {
        out.joined += 1;
        return;
      }
      if (!lesson) {
        out.heldPending += 1;
        return;
      }
      var idx = lessons.indexOf(lesson);
      if (idx >= 0 && idx + 1 <= learned) {
        out.schedulable += 1;
      } else {
        out.heldUnlearned += 1;
        if (out.unlearnedNames.indexOf(lesson) < 0) {
          out.unlearnedNames.push(lesson);
        }
      }
    };

    (groupResults || []).forEach(function (r) {
      if (!r || !r.ok || !r.group) {
        return;
      }
      var group = r.group;
      var groupLesson = metaField(group.lesson);
      (group.words || []).forEach(function (word) {
        var saved = postSave ? byWord[String(word).trim()] : null;
        if (!saved) {
          verdictFromPayload(group, groupLesson);
          return;
        }
        // Post-save truth: participates composes with scope (AND), and
        // ANY textbook source can admit the word (US-33: one learned
        // assignment among several suffices; a sticky or foreign include
        // joins the range just the same).
        if (saved.participates === false) {
          out.paused += 1;
          return;
        }
        var includeHit = false;
        var learnedHit = false;
        var targetLesson = groupLesson;
        var entries = saved.textbooks || [];
        entries.forEach(function (a) {
          if (!a) {
            return;
          }
          if (a.includeInScope) {
            includeHit = true;
          }
          var set = a.id != null ? learnedByBook[a.id] : null;
          if (set && a.lesson && set[a.lesson]) {
            learnedHit = true;
          }
          if (book && a.id === book.id) {
            targetLesson = metaField(a.lesson);
          }
        });
        if (includeHit) {
          out.joined += 1;
        } else if (learnedHit) {
          out.schedulable += 1;
        } else if (!entries.length) {
          out.joined += 1; // nothing attributed → supplementary default
        } else if (targetLesson) {
          out.heldUnlearned += 1;
          if (out.unlearnedNames.indexOf(targetLesson) < 0) {
            out.unlearnedNames.push(targetLesson);
          }
        } else {
          out.heldPending += 1;
        }
      });
    });
    return out;
  }

  /**
   * renderScopeReceipt (Issue #81) — the visually separate 默写安排 block
   * inside a result banner: real counts + reasons, beside (never merged
   * into) the 保存结果 buckets. A sensible next step closes the block —
   * what the parent can do NOW versus what unlocks later.
   */
  function renderScopeReceipt(box, scope) {
    var total = scope.schedulable + scope.joined + scope.heldUnlearned +
      scope.heldPending + (scope.paused || 0);
    if (!total) {
      return;
    }
    var scopeBox = document.createElement('div');
    scopeBox.className = 'result-scope';
    var title = document.createElement('p');
    title.className = 'result-scope-title';
    title.textContent = '默写安排';
    scopeBox.appendChild(title);
    var addLine = function (text, tone) {
      var line = document.createElement('p');
      line.className = 'result-scope-line result-scope-' + tone;
      line.textContent = text;
      scopeBox.appendChild(line);
    };
    if (scope.schedulable) {
      addLine('可安排默写 ' + scope.schedulable + ' 词（课次已学）', 'good');
    }
    if (scope.joined) {
      addLine('可安排默写 ' + scope.joined + ' 词（' +
        (scope.mode === 'none' ? '补充词汇默认加入范围' : '已加入默写范围') + '）', 'good');
    }
    if (scope.heldUnlearned) {
      addLine('暂不自动安排 ' + scope.heldUnlearned + ' 词（课次' +
        quoteList(scope.unlearnedNames) + '尚未学到）', 'wait');
    }
    if (scope.heldPending) {
      addLine('暂不自动安排 ' + scope.heldPending + ' 词（课次待确认）', 'wait');
    }
    if (scope.paused) {
      addLine('暂不自动安排 ' + scope.paused + ' 词（已移出课堂参与，可在词库中恢复）', 'wait');
    }
    var next = document.createElement('p');
    next.className = 'result-scope-line result-scope-next';
    if (scope.schedulable || scope.joined) {
      next.textContent = '下一步：这些词现在就能安排默写；其余词按进度或加入范围自动跟进。';
    } else if (scope.heldUnlearned) {
      next.textContent = '下一步：在「教材管理」中更新已学进度，这些词会自动进入默写安排。';
    } else if (scope.paused) {
      next.textContent = '下一步：这些词已移出课堂参与，在「词库」中恢复参与后即可安排默写。';
    } else {
      next.textContent = '下一步：在词库中为这些词指定课次，或加入默写范围。';
    }
    scopeBox.appendChild(next);
    box.appendChild(scopeBox);
  }

  /**
   * appendPoemReceipt (Issue #11) — the poem half of BOTH commit banners:
   * one detail line per server outcome class (新增/已有/无法保存, per-poem
   * titles), and — on a transport failure — the honest 未保存 note (what
   * landed is never fabricated; the counts stay the server's own).
   */
  function appendPoemReceipt(box, poemReceipt) {
    var byStatus = { added: [], existing: [], invalid: [] };
    (poemReceipt.results || []).forEach(function (r) {
      var title = (r && r.title) || '（无诗题）';
      if (r && byStatus[r.status]) {
        byStatus[r.status].push(r.reason ? (title + '（' + r.reason + '）') : title);
      }
    });
    var lines = [];
    if (byStatus.added.length) {
      lines.push('新增古诗：' + byStatus.added.join('、'));
    }
    if (byStatus.existing.length) {
      lines.push('古诗已有（不会重复建立）：' + byStatus.existing.join('、'));
    }
    if (byStatus.invalid.length) {
      lines.push('无法保存（请修改后重试）：' + byStatus.invalid.join('、'));
    }
    if (lines.length) {
      var detail = document.createElement('p');
      detail.className = 'result-detail';
      detail.textContent = lines.join('；');
      box.appendChild(detail);
    }
    if (poemReceipt.error) {
      var failed = document.createElement('p');
      failed.className = 'result-note';
      failed.textContent = '古诗保存失败：' + poemReceipt.error +
        '。已添加的部分不会丢失，请重试未完成的部分。';
      box.appendChild(failed);
    }
  }

  /**
   * appendPassageReceipt (Issue #13) — the passage half of BOTH commit
   * banners: one detail line per server outcome class (新增/已有/无法保
   * 存, per-passage titles), and — on a transport failure — the honest
   * 未保存 note (what landed is never fabricated; the counts stay the
   * server's own).
   */
  function appendPassageReceipt(box, passageReceipt) {
    var byStatus = { added: [], existing: [], invalid: [] };
    (passageReceipt.results || []).forEach(function (r) {
      var title = (r && r.title) || '（无标题）';
      if (r && byStatus[r.status]) {
        byStatus[r.status].push(r.reason ? (title + '（' + r.reason + '）') : title);
      }
    });
    var lines = [];
    if (byStatus.added.length) {
      lines.push('新增课文：' + byStatus.added.join('、'));
    }
    if (byStatus.existing.length) {
      lines.push('课文已有（不会重复建立）：' + byStatus.existing.join('、'));
    }
    if (byStatus.invalid.length) {
      lines.push('无法保存（请修改后重试）：' + byStatus.invalid.join('、'));
    }
    if (lines.length) {
      var detail = document.createElement('p');
      detail.className = 'result-detail';
      detail.textContent = lines.join('；');
      box.appendChild(detail);
    }
    if (passageReceipt.error) {
      var failed = document.createElement('p');
      failed.className = 'result-note';
      failed.textContent = '课文保存失败：' + passageReceipt.error +
        '。已添加的部分不会丢失，请重试未完成的部分。';
      box.appendChild(failed);
    }
  }

  /**
   * Per-item result view (transparency prohibition: partial success is
   * never a plain success). Groups keep every word visible with its own
   * outcome.
   */
  PhotoAddForm.prototype.renderCommitResult = function (outcome, submittedRows, fresh, poemReceipt, passageReceipt) {
    clearEl(this.banner);
    var kit = global.__parentFormKit || {};

    var lists = buildAddOutcomeLists(outcome, submittedRows, fresh);
    var added = lists.added;
    var existed = lists.existed;
    var failed = lists.failed;
    var unverified = lists.unverified;
    var raced = lists.raced;
    // Without a snapshot, the per-word lists stay empty by design: the
    // summary carries the server's authoritative counts and the detail
    // names every submitted word as 未核对 (below) — picking "winners"
    // from an unreadable library would fabricate verdicts.

    var notSubmitted = this.rows.filter(function (r) {
      return r.status !== 'new' || r.checked === false;
    });
    // Issue #11/#13: poems and passages not joining this confirm
    // (unchecked or invalid) get their own 未提交 line — a whole work is
    // named by its title.
    var poemsNotSubmitted = this.poemRows.filter(function (r) {
      return r.status === 'invalid' || r.checked === false;
    });
    var passagesNotSubmitted = this.passageRows.filter(function (r) {
      return r.status === 'invalid' || r.checked === false;
    });

    var box = document.createElement('div');
    var partial = existed.length > 0 || failed.length > 0 || unverified.length > 0 ||
      notSubmitted.length > 0 || poemsNotSubmitted.length > 0 || passagesNotSubmitted.length > 0 ||
      (poemReceipt && (poemReceipt.existing > 0 || poemReceipt.error)) ||
      (passageReceipt && (passageReceipt.existing > 0 || passageReceipt.error));
    box.className = 'result-banner ' +
      (partial ? 'result-banner-warning' : 'result-banner-success');
    box.setAttribute('role', 'status');
    box.setAttribute('aria-live', 'polite');

    var summary = document.createElement('p');
    summary.className = 'result-summary';
    var addedCount = raced && !fresh ? (outcome.added || 0) : added.length;
    summary.textContent = '已添加 ' + addedCount + ' 个' +
      (existed.length ? '，已有 ' + existed.length + ' 个' : '') +
      (failed.length ? '，未添加 ' + failed.length + ' 个' : '') +
      (unverified.length ? '，未核对 ' + unverified.length + ' 个' : '') +
      (notSubmitted.length ? '，未提交 ' + notSubmitted.length + ' 个' : '') +
      (poemReceipt && !poemReceipt.skipped
        ? ('，已添加古诗 ' + poemReceipt.added + ' 首' +
          (poemReceipt.existing ? '，古诗已有 ' + poemReceipt.existing + ' 首' : ''))
        : '') +
      (poemsNotSubmitted.length ? '，未提交古诗 ' + poemsNotSubmitted.length + ' 首' : '') +
      (passageReceipt && !passageReceipt.skipped
        ? ('，已添加课文 ' + passageReceipt.added + ' 篇' +
          (passageReceipt.existing ? '，课文已有 ' + passageReceipt.existing + ' 篇' : ''))
        : '') +
      (passagesNotSubmitted.length ? '，未提交课文 ' + passagesNotSubmitted.length + ' 篇' : '');
    box.appendChild(summary);

    var detail = document.createElement('p');
    detail.className = 'result-detail';
    var parts = [];
    if (raced && !fresh) {
      // Server-confirmed count only; the words themselves are listed as
      // 未核对 below (a network blip must not fabricate per-word verdicts).
      if (addedCount > 0) {
        parts.push('已添加 ' + addedCount + ' 个（服务器确认）');
      }
    } else if (added.length) {
      parts.push('已添加：' + added.join('、'));
    }
    if (existed.length) {
      parts.push('已有（保留词库原释义）：' + existed.join('、'));
    }
    if (failed.length) {
      parts.push('未添加成功（请重试）：' + failed.join('、'));
    }
    if (unverified.length) {
      parts.push('未核对（词库读取失败，请刷新词库查看）：' + unverified.join('、'));
    }
    detail.textContent = parts.join('；');
    if (parts.length) {
      box.appendChild(detail);
    }

    // Issue #11/#13: the poem and passage outcomes ride the same banner —
    // per-item lines from the server's own results (added/existing/
    // invalid), plus the transport-failure note naming what did not get
    // saved.
    if (poemReceipt && !poemReceipt.skipped) {
      appendPoemReceipt(box, poemReceipt);
    }
    if (passageReceipt && !passageReceipt.skipped) {
      appendPassageReceipt(box, passageReceipt);
    }

    if (notSubmitted.length) {
      var skippedNote = document.createElement('p');
      skippedNote.className = 'result-detail';
      var skippedWords = notSubmitted.map(function (r) {
        var why = r.status === 'invalid'
          ? describeInvalidReason(r.reason)
          : (r.status === 'new' ? '未勾选' : statusLabel(r));
        return r.word + '（' + why + '）';
      });
      skippedNote.textContent = '未提交：' + skippedWords.join('、');
      box.appendChild(skippedNote);
    }

    if (poemsNotSubmitted.length) {
      var poemsSkipped = document.createElement('p');
      poemsSkipped.className = 'result-detail';
      poemsSkipped.textContent = '未提交古诗：' + poemsNotSubmitted.map(function (r) {
        var why = r.status === 'invalid'
          ? describePoemInvalidReason(r.reason) : '未勾选';
        return (r.title || '（无诗题）') + '（' + why + '）';
      }).join('、');
      box.appendChild(poemsSkipped);
    }

    if (passagesNotSubmitted.length) {
      var passagesSkipped = document.createElement('p');
      passagesSkipped.className = 'result-detail';
      passagesSkipped.textContent = '未提交课文：' + passagesNotSubmitted.map(function (r) {
        var why = r.status === 'invalid'
          ? describePassageInvalidReason(r.reason) : '未勾选';
        return (r.title || '（无标题）') + '（' + why + '）';
      }).join('、');
      box.appendChild(passagesSkipped);
    }

    // Issue #81: 保存结果 above, 默写安排 below — visually separate. In
    // this flow the words carry no textbook, so the scope truth is the
    // server's add outcome: every landed new word defaults into the
    // dictation range (补充词汇默认参与), unverified/failed ones are not
    // counted as schedulable.
    renderScopeReceipt(box, {
      mode: 'none',
      schedulable: 0,
      joined: addedCount,
      heldUnlearned: 0,
      heldPending: 0,
      unlearnedNames: [],
    });

    var note = document.createElement('p');
    note.className = 'result-note';
    note.textContent = kit.SELECTION_NOTE_TEXT || '新词将按复习安排选入课堂，不一定是下一课。';
    box.appendChild(note);

    this.banner.appendChild(box);
  };

  /**
   * The Issue #5 receipt: per-word add outcomes (same honesty modes as
   * renderCommitResult) PLUS per-group assignment outcomes, the words the
   * snapshot could not resolve, and the not-yet-learned-lesson note (AC4:
   * stored fine, out of automatic selection until the progress moves).
   * A partial success is never a plain success.
   */
  /** The Issue #6 per-category receipt labels (buildTextbookReceipt kin). */
  var RECEIPT_CATEGORIES = [
    { key: 'added', label: '新增' },
    { key: 'merged', label: '合并' },
    { key: 'pendingDiff', label: '待处理差异' },
    { key: 'failed', label: '失败' },
    { key: 'unverified', label: '未核对' },
    { key: 'excluded', label: '未提交' },
  ];

  PhotoAddForm.prototype.renderTextbookCommitResult = function (receipt) {
    clearEl(this.banner);
    var kit = global.__parentFormKit || {};
    var book = receipt.book;
    var built = buildTextbookReceipt({
      rows: this.rows,
      addRows: receipt.submittedRows,
      addOutcome: receipt.addOutcome,
      fresh: receipt.fresh,
      editResults: receipt.editResults,
      groups: receipt.groups,
      groupResults: receipt.groupResults,
      unassigned: receipt.unassigned,
    });

    // Issue #81: the 默写安排 verdict comes from the ACTUAL post-save
    // state (summarizeAssignScope over the post-assign word entries, all
    // eligibility sources and the fresh progress; assign payloads only
    // when that re-read failed) — rendered as its own block below, never
    // merged into the 保存结果 buckets.
    var scope = summarizeAssignScope(book, receipt.groupResults, receipt.postSave, receipt.booksAfter);
    scope.mode = 'textbook';

    var box = document.createElement('div');
    var partial = built.summary.failed > 0 || built.summary.pendingDiff > 0 ||
      built.summary.unverified > 0 || built.summary.excluded > 0;
    box.className = 'result-banner ' +
      (partial ? 'result-banner-warning' : 'result-banner-success');
    box.setAttribute('role', 'status');
    box.setAttribute('aria-live', 'polite');

    // AC-6: the summary names the four result kinds (plus the honesty
    // buckets when non-zero) and is the tally of the per-item list below.
    // Issue #11: the poem half rides the same sentence (新增/已有古诗).
    var summary = document.createElement('p');
    summary.className = 'result-summary';
    var parts = RECEIPT_CATEGORIES.filter(function (c) {
      return built.summary[c.key] > 0;
    }).map(function (c) {
      return c.label + ' ' + built.summary[c.key] + ' 个';
    });
    if (receipt.poemReceipt && !receipt.poemReceipt.skipped) {
      parts.push('新增古诗 ' + receipt.poemReceipt.added + ' 首' +
        (receipt.poemReceipt.existing ? '，古诗已有 ' + receipt.poemReceipt.existing + ' 首' : ''));
    }
    if (receipt.passageReceipt && !receipt.passageReceipt.skipped) {
      parts.push('新增课文 ' + receipt.passageReceipt.added + ' 篇' +
        (receipt.passageReceipt.existing ? '，课文已有 ' + receipt.passageReceipt.existing + ' 篇' : ''));
    }
    summary.textContent = '教材「' + (book ? book.name : '？') + '」：' +
      (parts.length ? parts.join('，') : '没有可提交的内容');
    box.appendChild(summary);

    // Per-item lines, one per category in the fixed order.
    var detail = document.createElement('p');
    detail.className = 'result-detail';
    var lines = [];
    RECEIPT_CATEGORIES.forEach(function (c) {
      var words = built.items.filter(function (i) { return i.category === c.key; });
      if (!words.length) {
        return;
      }
      lines.push(c.label + '：' + words.map(function (i) {
        return i.note ? i.word + '（' + i.note + '）' : i.word;
      }).join('、'));
    });
    detail.textContent = lines.join('；');
    if (lines.length) {
      box.appendChild(detail);
    }

    // The group view stays: which lesson each batch landed in.
    var groupLines = [];
    receipt.groupResults.forEach(function (r) {
      var label = r.group.lesson
        ? '课次「' + r.group.lesson + '」'
        : (r.group.includeInScope ? '默写范围（课次待确认）' : '课次待确认');
      groupLines.push((r.ok ? '已指定' : '指定失败') + label + '：' + r.group.words.join('、') +
        (r.ok ? '' : ' — ' + r.error));
    });
    if (groupLines.length) {
      var groupNote = document.createElement('p');
      groupNote.className = 'result-note';
      groupNote.textContent = groupLines.join('；');
      box.appendChild(groupNote);
    }

    // Issue #81: the separate 默写安排 block — actual saved scheduling
    // (可安排默写 N 词 because 课次已学/已加入范围, M 词暂不自动安排
    // because 课次尚未学到/待确认) with a next step, beside the 保存结果
    // buckets above and never summarized as one success.
    renderScopeReceipt(box, scope);

    // Issue #11: the poem outcomes + the honest classroom note — the
    // scope rule (已学进度) already governs poems server-side, but the
    // 古诗课堂 itself is the NEXT task: never 可完成背默, never dictation.
    if (receipt.poemReceipt && !receipt.poemReceipt.skipped) {
      appendPoemReceipt(box, receipt.poemReceipt);
      var poemNote = document.createElement('p');
      poemNote.className = 'result-note';
      poemNote.textContent = '古诗按整首保存；学习范围随教材已学进度生效，古诗课堂将在后续更新中开放。';
      box.appendChild(poemNote);
    }

    // Issue #13: the passage outcomes + the same honest classroom note —
    // the practice ranges are stored with the original (选段不复制课文),
    // the scope rule governs passages server-side, but the 课文课堂 is a
    // LATER task: passages never enter the word-dictation selection.
    if (receipt.passageReceipt && !receipt.passageReceipt.skipped) {
      appendPassageReceipt(box, receipt.passageReceipt);
      var passageNote = document.createElement('p');
      passageNote.className = 'result-note';
      passageNote.textContent = '课文按整篇保存，练习范围（整篇或指定自然段）随原文一起记录、可随时调整；学习范围随教材已学进度生效，课文课堂将在后续更新中开放。';
      box.appendChild(passageNote);
    }

    var note = document.createElement('p');
    note.className = 'result-note';
    note.textContent = kit.SELECTION_NOTE_TEXT || '新词将按复习安排选入课堂，不一定是下一课。';
    box.appendChild(note);

    this.banner.appendChild(box);
  };

  /**
   * 放弃识别结果: the draft is client-side only — dropping it provably
   * leaves the library untouched (nothing was written at recognition).
   * A PDF draft also releases the pdf.js document (page retries end with
   * the draft — Issue #7).
   */
  PhotoAddForm.prototype.handleDiscard = function () {
    var kit = global.__parentFormKit || {};
    this.releasePdf();
    this.resetEntries();
    this.showFormError('');
    if (kit.clearContainer) {
      kit.clearContainer(this.banner);
    } else {
      this.banner.textContent = '';
    }
  };

  // ------------------------------------------------------------------
  // Standalone image helpers (student photo-controller shapes, trimmed to
  // the one-shot REST upload — no chunking, no WS).
  // ------------------------------------------------------------------

  /**
   * Decode the file into an <img> via an object URL (revoked after load) —
   * no base64 round trip, so a multi-MB photo never doubles into a data
   * URL string first (the thumbnail path uses the same mechanism).
   */
  function loadImageFromFile(file) {
    return new Promise(function (resolve, reject) {
      var url = URL.createObjectURL(file);
      var img = new Image();
      img.onload = function () {
        URL.revokeObjectURL(url);
        resolve(img);
      };
      img.onerror = function () {
        URL.revokeObjectURL(url);
        reject(new Error('无法读取这张照片，请换一张'));
      };
      img.src = url;
    });
  }

  function canvasToJpegBlob(canvas, quality) {
    return new Promise(function (resolve) {
      canvas.toBlob(function (blob) {
        resolve(blob);
      }, 'image/jpeg', quality);
    });
  }

  // Window-or-module-global tail (state-manager.js convention).
  global.PhotoAddForm = PhotoAddForm;
  global.classifyPhotoEntries = classifyPhotoEntries;
  global.matchTextbookForMeta = matchTextbookForMeta;
  global.distinctLessons = distinctLessons;
  global.distinctRowLessons = distinctRowLessons;
  global.isCommittable = isCommittable;
  global.isResolved = isResolved;
  global.planAssignments = planAssignments;
  global.buildTextbookReceipt = buildTextbookReceipt;
  // Issue #81 P2: the 默写安排 receipt verdict + the state line's hint —
  // pure over their inputs, so the Node unit tests pin the post-save
  // eligibility contract (window.parseBatch precedent).
  global.summarizeAssignScope = summarizeAssignScope;
  global.scheduleHint = scheduleHint;
  // Issue #11: the whole-poem draft surfaces (the classifyPhotoEntries
  // kin — pure, so the Node unit test pins the contract).
  global.normalizePoemText = normalizePoemText;
  global.classifyPoemDrafts = classifyPoemDrafts;
  global.describePoemInvalidReason = describePoemInvalidReason;
  global.mergePagePoems = mergePagePoems;
  global.mergePageOthers = mergePageOthers;
  // Issue #13: the whole-passage draft surfaces (the poem kin — pure, so
  // the Node unit test pins the contract).
  global.normalizePassageText = normalizePassageText;
  global.classifyPassageDrafts = classifyPassageDrafts;
  global.describePassageInvalidReason = describePassageInvalidReason;
  global.mergePagePassages = mergePagePassages;
  // Issue #7: the upload budget's ONE encoder — parent-pdf.js's page
  // renderer walks the same ladder and cap the photo path uses.
  global.PHOTO_QUALITY_LADDER = QUALITY_LADDER;
  global.PHOTO_MAX_UPLOAD_BYTES = MAX_UPLOAD_BYTES;
})(typeof window !== 'undefined' ? window : this);
