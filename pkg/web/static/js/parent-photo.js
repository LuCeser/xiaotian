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
 *   - Rows without a lesson: the page-level 「待确认课次的词直接加入默写
 *     范围」checkbox is the explicit include-in-scope handling; supplementary
 *     classification never requires inventing a lesson (AC5).
 *   - 不指定教材 keeps the exact Issue #3 supplementary flow.
 *
 * classifyPhotoEntries / matchTextbookForMeta / suggestTextbookName /
 * nextLessonList / planAssignments are PURE functions exposed on the
 * window for the Node unit tests (tests/test-parent-photo-classify.js,
 * tests/test-parent-photo-textbook.js — window.parseBatch precedent);
 * everything DOM-ish lives in PhotoAddForm.
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
      var row = { index: i, word: word, meaning: meaning, lesson: lesson, status: 'new', reason: '', existingMeaning: '' };

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

  /** The meta fields that identify a textbook (lesson excluded — it is
   *  per-page/per-word, not part of the book's identity). */
  var META_KEYS = ['edition', 'stage', 'grade', 'volume', 'module'];

  function metaField(v) {
    return v == null ? '' : String(v).trim();
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
   * suggestTextbookName — the create-form's name prefill from the readable
   * meta fields, joined with ·. A lesson alone suggests nothing (it is not
   * the book's identity). Pure.
   */
  function suggestTextbookName(meta) {
    if (!meta) {
      return '';
    }
    var parts = [];
    META_KEYS.forEach(function (key) {
      var value = metaField(meta[key]);
      if (value) {
        parts.push(value);
      }
    });
    return parts.join('·');
  }

  /**
   * nextLessonList — the confirmed lesson list after this import: an
   * existing lesson is a no-op, a missing one appends at the END (teaching
   * order is explicit and never sorted; the parent can reorder in 教材管理).
   * Pure, non-mutating.
   */
  function nextLessonList(lessons, lesson) {
    var list = (lessons || []).slice();
    var name = metaField(lesson);
    if (!name || list.indexOf(name) >= 0) {
      return list;
    }
    list.push(name);
    return list;
  }

  /**
   * planAssignments — group the committable rows into textbook-assign
   * calls (POST /textbooks/{id}/assign takes ONE lesson + one
   * includeInScope per batch). Committable = checked rows with status
   * 'new' or 'exists' AND a card id resolved from the post-commit library
   * snapshot (conflict rows keep the library's meaning — their merge is
   * Issue #6's work; invalid rows have no card). Groups keep
   * first-appearance order. includePending=true (the page-level 「待确认
   * 课次的词直接加入默写范围」checkbox) lifts ONLY the lesson-less group
   * into scope — rows with a real lesson always ride the textbook's
   * progress. Pure.
   */
  function planAssignments(rows, idByWord, includePending) {
    var groups = [];
    var byKey = {};
    (rows || []).forEach(function (row) {
      if (!row || (row.status !== 'new' && row.status !== 'exists') || row.checked === false) {
        return;
      }
      var id = idByWord && Object.prototype.hasOwnProperty.call(idByWord, row.word)
        ? idByWord[row.word] : null;
      if (!id) {
        return; // no resolved card → no honest assignment (receipt says so)
      }
      var lesson = metaField(row.lesson);
      var include = includePending && !lesson;
      var key = lesson + '\u0000' + (include ? '1' : '0');
      if (!Object.prototype.hasOwnProperty.call(byKey, key)) {
        byKey[key] = { lesson: lesson, includeInScope: include, cardIds: [], words: [] };
        groups.push(byKey[key]);
      }
      byKey[key].cardIds.push(id);
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
   *   fetchTextbooks(category) → Promise<Array> — GET /api/v1/textbooks
   *   createTextbook(payload) → Promise<book> — POST /api/v1/textbooks
   *   updateTextbook(id, payload) → Promise<book> — PUT /api/v1/textbooks/{id}
   *   assignWords(textbookId, payload) → Promise<result> — POST
   *       /api/v1/textbooks/{id}/assign (all four are the Issue #4
   *       surfaces reused as-is; absent ⇒ the textbook panel degrades to
   *       the plain Issue #3 supplementary flow)
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
    this.fetchTextbooks = options.fetchTextbooks || null;
    this.createTextbook = options.createTextbook || null;
    this.updateTextbook = options.updateTextbook || null;
    this.assignWords = options.assignWords || null;
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

    // Photo picker row: one file at a time (Issue #3 settles the
    // single-photo limit; multi-page PDF is a later task).
    var pickField = document.createElement('div');
    pickField.className = 'form-field';
    var pickLabel = document.createElement('label');
    pickLabel.className = 'form-label';
    pickLabel.textContent = '词表照片';
    var pickHint = document.createElement('p');
    pickHint.className = 'form-hint';
    pickHint.textContent = '拍摄或选择一张英语单词表照片（PNG / JPEG），无需裁图。';
    var pickRow = document.createElement('div');
    pickRow.className = 'photo-pick-row';
    this.fileInput = document.createElement('input');
    this.fileInput.type = 'file';
    this.fileInput.accept = 'image/png,image/jpeg';
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
    this.previewHint.textContent = '选择照片后点「开始识别」';
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

  PhotoAddForm.prototype.handleFilePicked = function () {
    var file = this.fileInput.files && this.fileInput.files[0];
    this.showFormError('');
    this.currentFile = file || null;
    if (!file) {
      this.fileMeta.textContent = '';
      this.thumb.hidden = true;
      this.thumb.removeAttribute('src');
      this.recognizeBtn.disabled = true;
      return;
    }
    if (file.type !== 'image/png' && file.type !== 'image/jpeg') {
      this.currentFile = null;
      this.fileMeta.textContent = '';
      this.thumb.hidden = true;
      this.thumb.removeAttribute('src');
      this.recognizeBtn.disabled = true;
      this.showFormError('只支持 PNG 或 JPEG 照片，请换一张');
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
   * Prepare the upload bytes: a PNG/JPEG already under the cap ships
   * verbatim (preserves PNGs); anything bigger walks the canvas ladder and
   * re-encodes as JPEG. Resolves null when even the last rung stays over
   * the cap (the honest too-large outcome — nothing is uploaded).
   */
  PhotoAddForm.prototype.prepareUpload = function (file) {
    if (file.size <= MAX_UPLOAD_BYTES) {
      return Promise.resolve(file);
    }
    return readAsDataURL(file)
      .then(loadImage)
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
        var entries = data && Array.isArray(data.entries) ? data.entries : [];
        var meta = data && data.textbook && typeof data.textbook === 'object'
          ? data.textbook : null;
        if (meta && !META_KEYS.concat(['lesson']).some(function (key) {
          return metaField(meta[key]);
        })) {
          meta = null; // an all-empty header is no header (honesty cut both ways)
        }
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
          self.renderTextbookPanel(loaded[1] === null ? '教材读取失败，可重试或直接确认添加' : '');
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
    var kit = global.__parentFormKit || {};
    if (kit.clearContainer) {
      kit.clearContainer(this.preview);
    } else {
      this.preview.textContent = '';
    }
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
   * The lesson options shared by the toolbar and every row select:
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
    this.rows.forEach(function (row) {
      var name = row && row.lesson ? String(row.lesson).trim() : '';
      if (name && !seen[name]) {
        seen[name] = true;
        options.push(name);
      }
    });
    return options;
  };

  /** Hide the panel (no recognition yet / reset). */
  PhotoAddForm.prototype.renderTextbookPanelReset = function () {
    this.textbookPanel.hidden = true;
    var kit = global.__parentFormKit || {};
    if (kit.clearContainer) {
      kit.clearContainer(this.textbookPanel);
    } else {
      this.textbookPanel.textContent = '';
    }
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
    var kit = global.__parentFormKit || {};
    this.textbookPanel.hidden = false;
    if (kit.clearContainer) {
      kit.clearContainer(this.textbookPanel);
    } else {
      this.textbookPanel.textContent = '';
    }
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
    var fields = [
      { key: 'edition', label: '版本' }, { key: 'stage', label: '学段' },
      { key: 'grade', label: '年级' }, { key: 'volume', label: '上下册' },
      { key: 'module', label: '模块' }, { key: 'lesson', label: '课次' },
    ];
    var metaParts = fields.map(function (f) {
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
    var kit = global.__parentFormKit || {};
    if (kit.clearContainer) {
      kit.clearContainer(host);
    } else {
      host.textContent = '';
    }
    if (this.bookChoice === 'none') {
      var noneNote = document.createElement('p');
      noneNote.className = 'photo-meta-line';
      noneNote.textContent = '这些词将作为补充词汇加入，默认参与默写（补充词汇不要求虚构课次）。';
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
      nameInput.value = suggestTextbookName(this.detectedMeta);
      nameInput.setAttribute('aria-label', '教材名称');
      nameField.appendChild(nameLabel);
      nameField.appendChild(nameInput);
      host.appendChild(nameField);

      [
        { key: 'edition', label: '版本', ph: '如 人教版PEP' },
        { key: 'stage', label: '学段', ph: '如 小学' },
        { key: 'grade', label: '年级', ph: '如 三年级' },
        { key: 'volume', label: '上下册', ph: '如 上册' },
        { key: 'module', label: '模块', ph: '如 Unit 1-3' },
      ].forEach(function (meta) {
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
      var missing = [];
      this.rows.forEach(function (row) {
        var name = row && row.lesson ? String(row.lesson).trim() : '';
        if (name && !inList[name] && missing.indexOf(name) < 0) {
          missing.push(name);
        }
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
        if ((row.status === 'new' || row.status === 'exists') && row.checked !== false) {
          row.lesson = lesson;
        }
      });
      // Re-render rows so the per-row inputs reflect the batch value.
      self.renderPreview();
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
    META_KEYS.forEach(function (key) {
      var input = document.getElementById('parent-photo-tb-' + key);
      values[key] = input ? input.value.trim() : '';
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
    // default on; in textbook mode exists rows are committable too).
    for (var i = 0; i < next.length; i++) {
      var committable = next[i].status === 'new' ||
        (textbookMode && next[i].status === 'exists');
      next[i].checked = this.rows[i].checked !== false && committable;
      next[i].lesson = this.rows[i].lesson || '';
      if (this.rows[i].status === next[i].status) {
        next[i].existingMeaning = next[i].existingMeaning || this.rows[i].existingMeaning;
      }
    }
    this.rows = next;
  };

  /** Render the editable review table from this.rows. */
  PhotoAddForm.prototype.renderPreview = function () {
    var self = this;
    var kit = global.__parentFormKit || {};
    if (kit.clearContainer) {
      kit.clearContainer(this.preview);
    } else {
      this.preview.textContent = '';
    }
    if (!this.rows.length) {
      this.renderPreviewHint('选择照片后点「开始识别」');
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
    this.rows.forEach(function (row, i) {
      tbody.appendChild(self.buildReviewRow(row, i, textbookMode));
    });
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

  /** One editable review row. textContent-only rendering (Pitfall 10). */
  PhotoAddForm.prototype.buildReviewRow = function (row, i, textbookMode) {
    var self = this;
    var tr = document.createElement('tr');
    tr.className = 'photo-row photo-row-' + row.status;
    tr.setAttribute('data-row-index', String(i));

    // In textbook mode an 已有 row is committable too — checking it does
    // NOT re-add the word (the server skips it anyway); it assigns the
    // card to the chosen textbook's lesson (US-32's multi-source).
    var committable = row.status === 'new' || (textbookMode && row.status === 'exists');

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
    renderStatusCell(statusCell, row, textbookMode);
    tr.appendChild(statusCell);

    return tr;
  };

  /**
   * (Re)build one row's status cell content — the single writer for the
   * chip + note markup, used by both the initial render (buildReviewRow)
   * and the caret-preserving edit sync (onRowEdited). textContent only
   * (Pitfall 10).
   */
  function renderStatusCell(statusCell, row, textbookMode) {
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
        ? '词库释义：' + (row.existingMeaning || '（空）') + '，将保留原释义'
        : (textbookMode ? '词库已有相同词条；勾选后仅指定课次，不重复添加' : '词库已有相同词条');
      statusCell.appendChild(note);
    }
    if (row.status === 'invalid') {
      var why = document.createElement('p');
      why.className = 'photo-status-note';
      why.textContent = describeInvalidReason(row.reason);
      statusCell.appendChild(why);
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

      var committable = row.status === 'new' || (textbookMode && row.status === 'exists');
      var check = tr.querySelector('.photo-row-check');
      if (check) {
        check.disabled = !committable;
        check.checked = committable && row.checked !== false;
      }

      var statusCell = tr.querySelector('.photo-status');
      if (statusCell) {
        renderStatusCell(statusCell, row, textbookMode);
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

  /** Rows that take part in a textbook-mode commit: added (new) or
   *  lesson-assigned (exists); conflict/invalid/unchecked never do. */
  PhotoAddForm.prototype.committedRows = function () {
    return this.rows.filter(function (r) {
      return (r.status === 'new' || r.status === 'exists') && r.checked !== false;
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
    if (rows.length > MAX_BATCH_ENTRIES) {
      this.showFormError('一次最多添加 ' + MAX_BATCH_ENTRIES + ' 个单词，请取消部分勾选后分两次提交');
      return;
    }

    var unit = this.unitInput ? this.unitInput.value.trim() : '';
    var entries = rows.map(function (r) {
      return { word: r.word, meaning: r.meaning, example: '' };
    });

    this.setCommitBusy(true);
    this.submitWords({ category: 'english', unit: unit, entries: entries })
      .then(function (result) {
        var outcome = result || { added: 0, skipped: 0 };
        // Reconcile ONLY when the server reports skips (a raced duplicate).
        // A failed snapshot fetch resolves null — "unavailable", NOT an
        // empty library — so a network blip can never reclassify the
        // server-confirmed adds as failures (honesty cuts both ways).
        var reconcile = (outcome.skipped || 0) > 0
          ? self.fetchWords('english').catch(function () { return null; })
          : Promise.resolve(null);
        return reconcile.then(function (fresh) {
          if (fresh) {
            self.existingWords = fresh;
          }
          self.renderCommitResult(outcome, rows, fresh);
          self.onCommitted(outcome);
          // Reset for the next photo; category note and unit retained.
          self.resetAfterCommit();
        });
      })
      .catch(function (err) {
        var message = (err && err.message) ? err.message : '未知错误';
        if (kit.showToast) {
          kit.showToast('保存失败：' + message + '。内容没有保存，请检查后重试。', 'error');
        } else {
          self.showFormError('保存失败：' + message + '。内容没有保存，请检查后重试。');
        }
      })
      .then(function () {
        self.setCommitBusy(false);
      });
  };

  /** Shared post-success reset (both commit paths land here). */
  PhotoAddForm.prototype.resetAfterCommit = function () {
    this.currentFile = null;
    this.fileInput.value = '';
    this.fileMeta.textContent = '';
    this.thumb.hidden = true;
    this.thumb.removeAttribute('src');
    this.recognizeBtn.disabled = true;
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
    var kit = global.__parentFormKit || {};

    var committed = this.committedRows();
    if (!committed.length) {
      this.showFormError('没有勾选任何词，请勾选后再确认');
      this.updateCommitButton();
      return;
    }
    if (committed.length > MAX_BATCH_ENTRIES) {
      this.showFormError('一次最多提交 ' + MAX_BATCH_ENTRIES + ' 个单词，请取消部分勾选后分两次提交');
      return;
    }
    var createValues = null;
    if (this.bookChoice === 'new') {
      createValues = this.readCreateForm();
      if (!createValues.name) {
        this.showFormError('请先填写教材名称（缺失的教材信息只需这一次确认）');
        return;
      }
    }

    this.setCommitBusy(true);
    this.resolveTargetBook(committed, createValues)
      .then(function (book) {
        return self.addAndAssign(book, committed);
      })
      .then(function (receipt) {
        self.renderTextbookCommitResult(receipt);
        self.onCommitted(receipt.addOutcome || {});
        // The textbook list changed (created/appended lessons) — reload it
        // for the next recognition, then reset the flow.
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
        if (kit.showToast) {
          kit.showToast('保存失败：' + message + '。请检查后重试。', 'error');
        } else {
          self.showFormError('保存失败：' + message + '。请检查后重试。');
        }
      })
      .then(function () {
        self.setCommitBusy(false);
      });
  };

  /**
   * Step 1 — establish the attribution target BEFORE any word is written.
   * 'new' creates the textbook with the CONFIRMED lesson set (distinct
   * non-empty row lessons, row order); an existing book gets its missing
   * confirmed lessons appended (order fixable in 教材管理 later). Both
   * paths fail the whole commit on error — a supplementary add would
   * silently change what selection can reach.
   */
  PhotoAddForm.prototype.resolveTargetBook = function (committed, createValues) {
    var self = this;
    var finalLessons = [];
    var seen = {};
    committed.forEach(function (row) {
      var name = row.lesson ? String(row.lesson).trim() : '';
      if (name && !seen[name]) {
        seen[name] = true;
        finalLessons.push(name);
      }
    });

    if (this.bookChoice === 'new') {
      var payload = {
        name: createValues.name,
        category: 'english',
        lessons: finalLessons,
      };
      META_KEYS.forEach(function (key) {
        if (createValues[key]) {
          payload[key] = createValues[key];
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
    var missing = finalLessons.filter(function (name) {
      return (book.lessons || []).indexOf(name) < 0;
    });
    if (!missing.length) {
      return Promise.resolve(book);
    }
    var next = nextLessonList(book.lessons, null);
    missing.forEach(function (name) {
      next = nextLessonList(next, name);
    });
    return this.updateTextbook(book.id, { lessons: next }).then(function (updated) {
      // Keep the in-memory book current for the receipt's progress hint.
      if (updated && updated.lessons) {
        book.lessons = updated.lessons;
        book.learnedLessons = updated.learnedLessons;
      } else {
        book.lessons = next;
      }
      return book;
    }).catch(function (err) {
      err.message = '课次表更新失败：' + (err.message || '未知错误');
      throw err;
    });
  };

  /**
   * Steps 2–4 — add the new rows, snapshot the truth, assign the groups.
   * Returns the receipt pieces for renderTextbookCommitResult.
   */
  PhotoAddForm.prototype.addAndAssign = function (book, committed) {
    var self = this;
    var addRows = committed.filter(function (r) {
      return r.status === 'new';
    });

    var addPromise = addRows.length
      ? self.submitWords({
        category: 'english',
        unit: self.unitInput ? self.unitInput.value.trim() : '',
        entries: addRows.map(function (r) {
          return { word: r.word, meaning: r.meaning, example: '' };
        }),
      }).catch(function (err) {
        err.message = '词汇添加失败：' + (err.message || '未知错误');
        throw err;
      })
      : Promise.resolve({ added: 0, skipped: 0 });

    return addPromise.then(function (outcome) {
      outcome = outcome || { added: 0, skipped: 0 };
      // The snapshot serves reconciliation (raced duplicates) AND card-id
      // resolution for the assignments — one fetch, two honest uses.
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
          var groups = planAssignments(committed, idByWord, self.includePending);
          var unassigned = committed.filter(function (r) {
            return !Object.prototype.hasOwnProperty.call(idByWord, r.word);
          }).map(function (r) { return r.word; });

          return self.runAssignments(book, groups).then(function (results) {
            return {
              book: book,
              addOutcome: outcome,
              submittedRows: addRows,
              fresh: fresh,
              groups: groups,
              groupResults: results,
              unassigned: unassigned,
            };
          });
        });
    });
  };

  /** Step 4 — run the assign calls sequentially, keeping each outcome. */
  PhotoAddForm.prototype.runAssignments = function (book, groups) {
    var self = this;
    var results = [];
    var chain = Promise.resolve();
    groups.forEach(function (group) {
      chain = chain.then(function () {
        return self.assignWords(book.id, {
          cardIds: group.cardIds,
          lesson: group.lesson,
          includeInScope: group.includeInScope,
        }).then(function (result) {
          results.push({ group: group, ok: true, result: result });
        }).catch(function (err) {
          results.push({ group: group, ok: false, error: (err && err.message) || '未知错误' });
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
    return { added: added, existed: existed, failed: failed, unverified: unverified, raced: raced, byWord: byWord };
  }

  /**
   * Per-item result view (transparency prohibition: partial success is
   * never a plain success). Groups keep every word visible with its own
   * outcome.
   */
  PhotoAddForm.prototype.renderCommitResult = function (outcome, submittedRows, fresh) {
    var kit = global.__parentFormKit || {};
    if (kit.clearContainer) {
      kit.clearContainer(this.banner);
    } else {
      this.banner.textContent = '';
    }

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
  PhotoAddForm.prototype.renderTextbookCommitResult = function (receipt) {
    var kit = global.__parentFormKit || {};
    if (kit.clearContainer) {
      kit.clearContainer(this.banner);
    } else {
      this.banner.textContent = '';
    }
    var book = receipt.book;
    var lists = buildAddOutcomeLists(receipt.addOutcome, receipt.submittedRows, receipt.fresh);
    var addedCount = lists.raced && !receipt.fresh
      ? (receipt.addOutcome.added || 0) : lists.added.length;

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

    var notSubmitted = this.rows.filter(function (r) {
      return (r.status !== 'new' && r.status !== 'exists') || r.checked === false;
    });
    var assignFailed = receipt.groupResults.filter(function (r) { return !r.ok; });
    var assignOkWords = [];
    receipt.groupResults.forEach(function (r) {
      if (r.ok) {
        assignOkWords = assignOkWords.concat(r.group.words);
      }
    });

    var box = document.createElement('div');
    var partial = lists.existed.length > 0 || lists.failed.length > 0 ||
      lists.unverified.length > 0 || assignFailed.length > 0 ||
      receipt.unassigned.length > 0 || notSubmitted.length > 0;
    box.className = 'result-banner ' +
      (partial ? 'result-banner-warning' : 'result-banner-success');
    box.setAttribute('role', 'status');
    box.setAttribute('aria-live', 'polite');

    var summary = document.createElement('p');
    summary.className = 'result-summary';
    var parts = [];
    if (receipt.submittedRows.length) {
      parts.push('已添加 ' + addedCount + ' 个' +
        (lists.existed.length ? '，已有 ' + lists.existed.length + ' 个' : '') +
        (lists.failed.length ? '，未添加 ' + lists.failed.length + ' 个' : '') +
        (lists.unverified.length ? '，未核对 ' + lists.unverified.length + ' 个' : ''));
    }
    parts.push('教材「' + (book ? book.name : '？') + '」指定课次 ' + assignOkWords.length + ' 个' +
      (assignFailed.length ? '，指定失败 ' + assignFailed.length + ' 组' : '') +
      (receipt.unassigned.length ? '，未指定 ' + receipt.unassigned.length + ' 个' : '') +
      (notSubmitted.length ? '，未提交 ' + notSubmitted.length + ' 个' : ''));
    summary.textContent = parts.join('；');
    box.appendChild(summary);

    var detail = document.createElement('p');
    detail.className = 'result-detail';
    var lines = [];
    if (lists.raced && !receipt.fresh && addedCount > 0) {
      lines.push('已添加 ' + addedCount + ' 个（服务器确认）');
    } else if (lists.added.length) {
      lines.push('已添加：' + lists.added.join('、'));
    }
    if (lists.existed.length) {
      lines.push('已有（保留词库原释义，仅指定课次）：' + lists.existed.join('、'));
    }
    if (lists.failed.length) {
      lines.push('未添加成功（请重试）：' + lists.failed.join('、'));
    }
    if (lists.unverified.length) {
      lines.push('未核对（词库读取失败，请刷新词库查看）：' + lists.unverified.join('、'));
    }
    receipt.groupResults.forEach(function (r) {
      var label = r.group.lesson
        ? '课次「' + r.group.lesson + '」'
        : (r.group.includeInScope ? '默写范围（课次待确认）' : '课次待确认');
      if (r.ok) {
        var missingIds = (r.result && r.result.missing) || [];
        var missingWords = [];
        missingIds.forEach(function (id) {
          var idx = r.group.cardIds.indexOf(id);
          if (idx >= 0) {
            missingWords.push(r.group.words[idx]);
          }
        });
        lines.push('已指定' + label + '：' + r.group.words.join('、') +
          (missingWords.length ? '（其中未找到词条：' + missingWords.join('、') + '）' : ''));
      } else {
        lines.push('指定失败（' + label + '）：' + r.group.words.join('、') + ' — ' + r.error +
          '。可在词库勾选后用「指定教材课次」重试');
      }
    });
    if (receipt.unassigned.length) {
      lines.push('未指定（词库读取失败，无法解析词条）：' + receipt.unassigned.join('、') +
        '。词汇已保存，可在词库中补指定课次');
    }
    detail.textContent = lines.join('；');
    if (lines.length) {
      box.appendChild(detail);
    }

    if (notSubmitted.length) {
      var skippedNote = document.createElement('p');
      skippedNote.className = 'result-detail';
      var skippedWords = notSubmitted.map(function (r) {
        var why = r.status === 'invalid'
          ? describeInvalidReason(r.reason)
          : (r.status === 'conflict' ? '释义不同' : (r.status === 'exists' ? '已有' : '未勾选'));
        return r.word + '（' + why + '）';
      });
      skippedNote.textContent = '未提交：' + skippedWords.join('、');
      box.appendChild(skippedNote);
    }

    if (unlearnedNames.length) {
      var unlearnedNote = document.createElement('p');
      unlearnedNote.className = 'result-note';
      unlearnedNote.textContent = '课次「' + unlearnedNames.join('」「') +
        '」尚未学到：这些词已保存，但不会自动选入默写；在教材管理中改进度后即可参与。';
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
   */
  PhotoAddForm.prototype.handleDiscard = function () {
    var kit = global.__parentFormKit || {};
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

  function readAsDataURL(file) {
    return new Promise(function (resolve, reject) {
      var reader = new FileReader();
      reader.onload = function () {
        resolve(reader.result);
      };
      reader.onerror = function () {
        reject(reader.error || new Error('read failed'));
      };
      reader.readAsDataURL(file);
    });
  }

  function loadImage(dataURL) {
    return new Promise(function (resolve, reject) {
      var img = new Image();
      img.onload = function () {
        resolve(img);
      };
      img.onerror = function () {
        reject(new Error('无法读取这张照片，请换一张'));
      };
      img.src = dataURL;
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
  global.suggestTextbookName = suggestTextbookName;
  global.nextLessonList = nextLessonList;
  global.planAssignments = planAssignments;
})(typeof window !== 'undefined' ? window : this);
