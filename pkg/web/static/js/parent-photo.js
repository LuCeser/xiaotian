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
   * photo path and every PDF page: the entries array (absent → []) and
   * the textbook header, with an all-empty header cut to null (honesty
   * cuts both ways — a reply whose every field is unreadable is no
   * header, never a 待确认 shell built from nothing).
   */
  function parseRecognized(data) {
    var entries = data && Array.isArray(data.entries) ? data.entries : [];
    var meta = data && data.textbook && typeof data.textbook === 'object'
      ? data.textbook : null;
    if (meta && !META_KEYS.concat(['lesson']).some(function (key) {
      return metaField(meta[key]);
    })) {
      meta = null;
    }
    return { entries: entries, meta: meta };
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
          category: 'english',
          unit: unit,
          entries: part.map(function (r) {
            return { word: r.word, meaning: r.meaning, example: '' };
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
   *   recognizePhoto(blob) → Promise<{textbook, entries: [...]}> — POST
   *       /api/v1/words/photo/recognize (wired in parent-app.js with its
   *       own longer timeout; the vision call can take ~20s)
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
   *   onCommitted(result) — fired after a successful commit (refresh hook)
   *   onCategoryChange(v) — fired once at mount with 'english' so the
   *       shared 单元 datalist populates for this flow's fixed category
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
    var td = options.textbookDeps || {};
    this.fetchTextbooks = td.fetchTextbooks || null;
    this.createTextbook = td.createTextbook || null;
    this.updateTextbook = td.updateTextbook || null;
    this.assignWords = td.assignWords || null;
    // Issue #6: the 采用照片释义 resolution rides the ordinary edit path
    // (PUT /words/{id} — text changes never touch FSRS state).
    this.editWord = td.editWord || null;
    this.onCommitted = options.onCommitted || function () {};
    this.onCategoryChange = options.onCategoryChange || function () {};

    var kit = global.__parentFormKit || {};

    this.form = document.createElement('form');
    this.form.className = 'photo-add-form';
    this.form.noValidate = true;

    // Fixed-category note: this flow imports ENGLISH word lists (Issue #3).
    // 语文词语照片导入 is a later task — a static explanation, not a radio.
    var catField = document.createElement('div');
    catField.className = 'form-field';
    var catLabel = document.createElement('span');
    catLabel.className = 'form-label';
    catLabel.textContent = '分类';
    var catNote = document.createElement('p');
    catNote.className = 'form-hint';
    catNote.textContent = '英语词表（语文词语照片导入暂未开放）';
    catField.appendChild(catLabel);
    catField.appendChild(catNote);
    this.form.appendChild(catField);

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
    pickHint.textContent = '拍摄或选择一张英语单词表照片（PNG / JPEG），或整本扫描 PDF（最多 100 页），无需裁图。';
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

    this.formError = document.createElement('p');
    this.formError.className = 'form-error';
    this.formError.setAttribute('aria-live', 'polite');
    this.formError.hidden = true;
    this.form.appendChild(this.formError);

    // Per-page status bar (Issue #7): one chip per PDF page (页码 + 状态，
    // failed pages carry a 重试该页 button) plus the whole-file summary
    // and a 停止 button while a run is in flight. Hidden for photos.
    this.pdfBar = document.createElement('div');
    this.pdfBar.className = 'pdf-status-bar';
    this.pdfBar.hidden = true;
    this.form.appendChild(this.pdfBar);

    // Textbook attribution panel (Issue #5): rendered between the picker
    // and the review table after each recognition; hidden until then.
    this.textbookPanel = document.createElement('div');
    this.textbookPanel.className = 'photo-textbook-panel';
    this.textbookPanel.hidden = true;
    this.form.appendChild(this.textbookPanel);

    // Review table: rendered after recognition; every row editable.
    this.preview = document.createElement('div');
    this.preview.className = 'preview-scroll';
    this.previewHint = document.createElement('p');
    this.previewHint.className = 'preview-empty';
    this.previewHint.textContent = '选择照片或 PDF 后点「开始识别」';
    this.preview.appendChild(this.previewHint);
    this.form.appendChild(this.preview);

    // 确认添加（{n} 个） + 放弃识别结果.
    var actions = document.createElement('div');
    actions.className = 'photo-actions';
    this.commitBtn = kit.buildLoadingButton
      ? kit.buildLoadingButton('确认添加（0 个）')
      : this.buildFallbackButton('确认添加（0 个）');
    this.commitBtn.disabled = true;
    this.discardBtn = document.createElement('button');
    this.discardBtn.type = 'button';
    this.discardBtn.className = 'btn btn-secondary';
    this.discardBtn.textContent = '放弃识别结果';
    this.discardBtn.hidden = true;
    actions.appendChild(this.commitBtn);
    actions.appendChild(this.discardBtn);
    this.form.appendChild(actions);

    // Result banner host (rendered after each commit).
    this.banner = document.createElement('div');
    this.form.appendChild(this.banner);

    // State.
    this.currentFile = null;
    this.rows = []; // current classified rows (source of truth on edit)
    this.existingWords = [];
    this.detectedMeta = null; // the READ-off-the-photo textbook header
    this.books = []; // existing english textbooks (textbook panel list)
    this.bookChoice = 'none'; // 'none' | 'new' | a textbook id (number)
    this.includePending = false; // 待确认课次的词直接加入默写范围
    // Issue #7: while a PDF is in play — { doc, plan, aborted, metaDone,
    // panelRendered }. doc stays open for page retries until discard /
    // commit reset / a new file; rows themselves live in this.rows.
    this.pdfState = null;

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

    container.appendChild(this.form);

    // The shared datalist populates for THIS flow's fixed category.
    this.onCategoryChange('english');
  }

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
      if (!(global.PdfPageRunner && global.PdfPageRunner.isAvailable())) {
        this.recognizeBtn.disabled = true;
        this.showFormError('PDF 支持未加载，请刷新页面或改用照片导入');
        return;
      }
      this.recognizeBtn.disabled = false;
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
    this.renderPdfBar();
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
        return self.recognizePhoto(bytes);
      })
      .then(function (data) {
        var parsed = parseRecognized(data);
        var entries = parsed.entries;
        var meta = parsed.meta;
        if (!entries.length) {
          self.setBusy(false);
          self.renderPreviewHint('这张照片里没有识别到词表，请换一张更清晰的照片');
          self.resetEntries();
          return;
        }
        self.detectedMeta = meta;
        self.bookChoice = 'none';
        self.includePending = false;
        // Snapshot the REAL library for classification; load the textbook
        // list in parallel for the attribution panel. A textbook-list
        // failure degrades the panel (with its reason), never the preview.
        var booksLoad = self.fetchTextbooks
          ? self.fetchTextbooks('english').catch(function () { return null; })
          : Promise.resolve(null);
        return Promise.all([
          self.fetchWords('english').catch(function () { return []; }),
          booksLoad,
        ]).then(function (loaded) {
          self.existingWords = loaded[0] || [];
          self.books = loaded[1] || [];
          self.rows = classifyPhotoEntries(entries, self.existingWords);
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
        // ONE library snapshot classifies every page as it lands (the run
        // can take minutes; raced duplicates stay honest through the
        // server's UNIQUE + the post-commit reconciliation — the photo
        // flow's precedent, just stretched over pages).
        var booksLoad = self.fetchTextbooks
          ? self.fetchTextbooks('english').catch(function () { return null; })
          : Promise.resolve(null);
        return Promise.all([
          self.fetchWords('english').catch(function () { return []; }),
          booksLoad,
        ]).then(function (loaded) {
          self.existingWords = loaded[0] || [];
          self.books = loaded[1] || [];
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
        return self.recognizePhoto(blob);
      })
      .then(function (data) {
        var parsed = parseRecognized(data);
        if (!state.metaDone && parsed.meta) {
          self.detectedMeta = parsed.meta;
          state.metaDone = true;
        }
        var classified = global.classifyPhotoEntries(parsed.entries, self.existingWords);
        self.rows = global.mergePageRows(self.rows, pageNo, classified);
        state.plan = global.applyPageEvent(state.plan, pageNo, 'done');
        // The textbook panel renders with the FIRST rows that land: its
        // preselect fixes bookChoice, so the reclassify below (and any
        // mid-run edit) sees the real textbook mode — the ordering the
        // photo flow gets for free by rendering the panel right after its
        // single recognition. A retry never rebuilds it.
        if (!state.panelRendered && self.rows.length) {
          self.renderTextbookPanel('');
          state.panelRendered = true;
        }
        // Settle cross-page duplicates NOW — the 与第 N 行重复 verdicts
        // come from classifyPhotoEntries over the whole merged array, and
        // the preview should show them the moment the page lands.
        self.reclassify();
        if (self.rows.length) {
          self.renderPreview();
        }
        self.renderPdfBar();
      })
      .catch(function (err) {
        state.plan = global.applyPageEvent(state.plan, pageNo, 'fail',
          (err && err.message) || '识别失败');
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
    if (!this.rows.length) {
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
    });
    var includeText = document.createElement('span');
    includeText.textContent = '待确认课次的词直接加入默写范围（不受进度限制）';
    includeLabel.appendChild(include);
    includeLabel.appendChild(includeText);
    host.appendChild(includeLabel);
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
    }
    this.rows = next;
  };

  /** Render the editable review table from this.rows. */
  PhotoAddForm.prototype.renderPreview = function () {
    var self = this;
    clearEl(this.preview);
    if (!this.rows.length) {
      this.renderPreviewHint('选择照片或 PDF 后点「开始识别」');
      this.updateCommitButton();
      return;
    }
    var textbookMode = this.bookChoice !== 'none';

    var table = document.createElement('table');
    table.className = 'preview-table photo-preview-table';

    var thead = document.createElement('thead');
    var headRow = document.createElement('tr');
    var headers = textbookMode
      ? ['加入', '单词', '释义（原文）', '课次', '状态']
      : ['加入', '单词', '释义（原文）', '状态'];
    headers.forEach(function (text) {
      var th = document.createElement('th');
      th.setAttribute('scope', 'col');
      th.textContent = text;
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
    });
    checkCell.appendChild(check);
    tr.appendChild(checkCell);

    var wordCell = document.createElement('td');
    var wordInput = document.createElement('input');
    wordInput.type = 'text';
    wordInput.className = 'form-input photo-cell-input';
    wordInput.value = row.word;
    wordInput.setAttribute('aria-label', '单词 第 ' + (i + 1) + ' 行');
    wordInput.addEventListener('input', function () {
      self.rows[i].word = wordInput.value;
      self.onRowEdited();
    });
    wordCell.appendChild(wordInput);
    tr.appendChild(wordCell);

    var meaningCell = document.createElement('td');
    var meaningInput = document.createElement('input');
    meaningInput.type = 'text';
    meaningInput.className = 'form-input photo-cell-input';
    meaningInput.value = row.meaning;
    meaningInput.setAttribute('aria-label', '释义 第 ' + (i + 1) + ' 行');
    meaningInput.addEventListener('input', function () {
      self.rows[i].meaning = meaningInput.value;
      self.onRowEdited();
    });
    meaningCell.appendChild(meaningInput);
    tr.appendChild(meaningCell);

    if (textbookMode) {
      // Per-row lesson (AC3: 逐条编辑仍然可用) — free text with the shared
      // datalist suggestions; empty means 待确认. The commit appends any
      // confirmed lesson the book's list lacks.
      var lessonCell = document.createElement('td');
      lessonCell.className = 'photo-lesson-cell';
      var lessonInput = document.createElement('input');
      lessonInput.type = 'text';
      lessonInput.className = 'form-input photo-cell-input';
      lessonInput.setAttribute('list', 'parent-photo-lesson-options');
      lessonInput.setAttribute('aria-label', '课次 第 ' + (i + 1) + ' 行');
      lessonInput.placeholder = '待确认';
      lessonInput.value = row.lesson || '';
      lessonInput.disabled = !committable;
      lessonInput.addEventListener('input', function () {
        self.rows[i].lesson = lessonInput.value;
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
  };

  PhotoAddForm.prototype.updateCommitButton = function () {
    var label = this.commitBtn.querySelector('.btn-label');
    var n = this.bookChoice !== 'none' ? this.committedRows().length : this.submittableRows().length;
    if (label) {
      label.textContent = '确认添加（' + n + ' 个）';
    }
    this.commitBtn.disabled = n === 0;
  };

  PhotoAddForm.prototype.submittableRows = function () {
    return this.rows.filter(function (r) {
      return r.status === 'new' && r.checked !== false;
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
    if (!rows.length) {
      this.showFormError('没有可添加的新词，请勾选或编辑后再确认');
      this.updateCommitButton();
      return;
    }

    if (!global.chunkBatch) {
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
    submitChunked(this, rows)
      .catch(function (err) {
        failedWith = err;
        return err.partialOutcome || { added: 0, skipped: 0 };
      })
      .then(function (outcome) {
        // Reconcile on skips (a raced duplicate) OR a mid-run slice
        // failure (what actually landed matters). A failed snapshot fetch
        // resolves null — "unavailable", NOT an empty library — so a
        // network blip can never reclassify server-confirmed adds as
        // failures (honesty cuts both ways).
        var reconcile = (outcome.skipped || 0) > 0 || failedWith
          ? self.fetchWords('english').catch(function () { return null; })
          : Promise.resolve(null);
        return reconcile.then(function (fresh) {
          if (fresh) {
            self.existingWords = fresh;
          }
          self.renderCommitResult(outcome, rows, fresh);
          if (failedWith) {
            var message = (failedWith && failedWith.message) ? failedWith.message : '未知错误';
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
    if (!committed.length) {
      this.showFormError('没有勾选任何词，请勾选后再确认');
      this.updateCommitButton();
      return;
    }
    if (!global.chunkBatch) {
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
    this.resolveTargetBook(committed, createValues)
      .then(function (book) {
        established = book;
        return self.addAndAssign(book, committed);
      })
      .then(function (receipt) {
        self.renderTextbookCommitResult(receipt);
        self.onCommitted(receipt.addOutcome || {});
        // The textbook list changed (created/appended lessons/backfilled
        // meta) — reload it for the next recognition, then reset the flow.
        if (self.fetchTextbooks) {
          self.fetchTextbooks('english').catch(function () { return null; }).then(function (books) {
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
          ? self.fetchTextbooks('english').catch(function () { return null; })
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
  PhotoAddForm.prototype.resolveTargetBook = function (committed, createValues) {
    var self = this;
    var finalLessons = distinctRowLessons(committed);

    if (this.bookChoice === 'new') {
      var payload = {
        name: createValues.name,
        category: 'english',
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
      return self.fetchWords('english').catch(function () { return null; })
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
              };
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
   * Per-item result view (transparency prohibition: partial success is
   * never a plain success). Groups keep every word visible with its own
   * outcome.
   */
  PhotoAddForm.prototype.renderCommitResult = function (outcome, submittedRows, fresh) {
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

    var box = document.createElement('div');
    var partial = existed.length > 0 || failed.length > 0 || unverified.length > 0 || notSubmitted.length > 0;
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
      (notSubmitted.length ? '，未提交 ' + notSubmitted.length + ' 个' : '');
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

    // Which assigned lessons sit beyond the book's learned progress?
    var learned = book && book.learnedLessons ? book.learnedLessons : 0;
    var lessons = (book && book.lessons) || [];
    var unlearnedNames = [];
    receipt.groupResults.forEach(function (r) {
      if (r.ok && r.group.lesson && lessons.indexOf(r.group.lesson) >= 0 &&
        lessons.indexOf(r.group.lesson) + 1 > learned &&
        unlearnedNames.indexOf(r.group.lesson) < 0) {
        unlearnedNames.push(r.group.lesson);
      }
    });

    var box = document.createElement('div');
    var partial = built.summary.failed > 0 || built.summary.pendingDiff > 0 ||
      built.summary.unverified > 0 || built.summary.excluded > 0;
    box.className = 'result-banner ' +
      (partial ? 'result-banner-warning' : 'result-banner-success');
    box.setAttribute('role', 'status');
    box.setAttribute('aria-live', 'polite');

    // AC-6: the summary names the four result kinds (plus the honesty
    // buckets when non-zero) and is the tally of the per-item list below.
    var summary = document.createElement('p');
    summary.className = 'result-summary';
    var parts = RECEIPT_CATEGORIES.filter(function (c) {
      return built.summary[c.key] > 0;
    }).map(function (c) {
      return c.label + ' ' + built.summary[c.key] + ' 个';
    });
    summary.textContent = '教材「' + (book ? book.name : '？') + '」：' +
      (parts.length ? parts.join('，') : '没有可提交的词');
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

    if (unlearnedNames.length) {
      var unlearnedNote = document.createElement('p');
      unlearnedNote.className = 'result-note';
      unlearnedNote.textContent = '课次「' + unlearnedNames.join('」「') +
        '」尚未学到：这些词已保存，但不会自动选入默写范围；在教材管理中改进度后即可加入。';
      box.appendChild(unlearnedNote);
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
  // Issue #7: the upload budget's ONE encoder — parent-pdf.js's page
  // renderer walks the same ladder and cap the photo path uses.
  global.PHOTO_QUALITY_LADDER = QUALITY_LADDER;
  global.PHOTO_MAX_UPLOAD_BYTES = MAX_UPLOAD_BYTES;
})(typeof window !== 'undefined' ? window : this);
