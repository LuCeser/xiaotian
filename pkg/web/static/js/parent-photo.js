/**
 * @file Parent page — 照片导入 (Issue #3): the parent photographs an
 *       English word list (PNG/JPEG), the service recognizes DRAFT entries
 *       through the vision seam, and the parent reviews/edits/checks them
 *       before anything is committed.
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
 * classifyPhotoEntries is a PURE function exposed on the window for the
 * Node unit test (tests/test-parent-photo-classify.js — window.parseBatch
 * precedent); everything DOM-ish lives in PhotoAddForm.
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
      var row = { index: i, word: word, meaning: meaning, status: 'new', reason: '', existingMeaning: '' };

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
  // PhotoAddForm — pick → recognize → review/edit → commit (photo tab)
  // ------------------------------------------------------------------

  /**
   * options:
   *   recognizePhoto(blob) → Promise<{entries: [...]}> — POST
   *       /api/v1/words/photo/recognize (wired in parent-app.js with its
   *       own longer timeout; the vision call can take ~20s)
   *   submitWords(payload) → Promise<{added, skipped}> — POST /api/v1/words
   *   fetchWords(category) → Promise<Array> — GET /api/v1/words?category=
   *       (the classification snapshot; ALSO re-fetched after commit to
   *       reconcile raced duplicates honestly)
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
        if (!entries.length) {
          self.setBusy(false);
          self.renderPreviewHint('这张照片里没有识别到词表，请换一张更清晰的照片');
          self.resetEntries();
          return;
        }
        // Snapshot the REAL library for classification, then render.
        return self.fetchWords('english')
          .catch(function () {
            return [];
          })
          .then(function (existing) {
            self.existingWords = existing || [];
            self.rows = classifyPhotoEntries(entries, self.existingWords);
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

  /** Re-run classification over the CURRENT (edited) row values. */
  PhotoAddForm.prototype.reclassify = function () {
    var entries = this.rows.map(function (r) {
      return { word: r.word, meaning: r.meaning };
    });
    var next = classifyPhotoEntries(entries, this.existingWords);
    // Carry over the checked flag the parent set (new rows default on).
    for (var i = 0; i < next.length; i++) {
      next[i].checked = this.rows[i].checked !== false && next[i].status === 'new';
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

    var table = document.createElement('table');
    table.className = 'preview-table photo-preview-table';

    var thead = document.createElement('thead');
    var headRow = document.createElement('tr');
    ['加入', '单词', '释义（原文）', '状态'].forEach(function (text) {
      var th = document.createElement('th');
      th.setAttribute('scope', 'col');
      th.textContent = text;
      headRow.appendChild(th);
    });
    thead.appendChild(headRow);
    table.appendChild(thead);

    var tbody = document.createElement('tbody');
    this.rows.forEach(function (row, i) {
      tbody.appendChild(self.buildReviewRow(row, i));
    });
    table.appendChild(tbody);
    this.preview.appendChild(table);
    this.updateCommitButton();
  };

  /** One editable review row. textContent-only rendering (Pitfall 10). */
  PhotoAddForm.prototype.buildReviewRow = function (row, i) {
    var self = this;
    var tr = document.createElement('tr');
    tr.className = 'photo-row photo-row-' + row.status;
    tr.setAttribute('data-row-index', String(i));

    var checkCell = document.createElement('td');
    checkCell.className = 'preview-index';
    var check = document.createElement('input');
    check.type = 'checkbox';
    check.className = 'photo-row-check';
    check.id = 'parent-photo-check-' + i;
    check.checked = row.status === 'new' && row.checked !== false;
    check.disabled = row.status !== 'new';
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

    var statusCell = document.createElement('td');
    statusCell.className = 'preview-status photo-status photo-status-' + row.status;
    renderStatusCell(statusCell, row);
    tr.appendChild(statusCell);

    return tr;
  };

  /**
   * (Re)build one row's status cell content — the single writer for the
   * chip + note markup, used by both the initial render (buildReviewRow)
   * and the caret-preserving edit sync (onRowEdited). textContent only
   * (Pitfall 10).
   */
  function renderStatusCell(statusCell, row) {
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
        : '词库已有相同词条';
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
    var self = this;
    var rowEls = this.preview.querySelectorAll('.photo-row');
    for (var i = 0; i < rowEls.length && i < this.rows.length; i++) {
      var row = this.rows[i];
      var tr = rowEls[i];
      tr.className = 'photo-row photo-row-' + row.status;

      var check = tr.querySelector('.photo-row-check');
      if (check) {
        check.disabled = row.status !== 'new';
        check.checked = row.status === 'new' && row.checked !== false;
      }

      var statusCell = tr.querySelector('.photo-status');
      if (statusCell) {
        renderStatusCell(statusCell, row);
      }
    }
    this.updateCommitButton();
  };

  PhotoAddForm.prototype.updateCommitButton = function () {
    var label = this.commitBtn.querySelector('.btn-label');
    var n = this.submittableRows().length;
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

  /**
   * Commit: re-classify (edits may have changed statuses), submit ONLY the
   * checked new rows through the ordinary add path, then reconcile against
   * the REAL library so per-item outcomes are honest even when another
   * device raced us (skipped > 0 ⇒ re-fetch and re-read the truth).
   */
  PhotoAddForm.prototype.handleCommit = function () {
    if (this.commitBtn.disabled) {
      return;
    }
    var self = this;
    var kit = global.__parentFormKit || {};
    this.showFormError('');
    this.reclassify();
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
          self.currentFile = null;
          self.fileInput.value = '';
          self.fileMeta.textContent = '';
          self.thumb.hidden = true;
          self.thumb.removeAttribute('src');
          self.recognizeBtn.disabled = true;
          self.resetEntries();
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
   * Per-item result view (transparency prohibition: partial success is
   * never a plain success). Groups keep every word visible with its own
   * outcome. Three honesty modes:
   *   - no skips: every submitted word was added (server-confirmed).
   *   - skips + snapshot (fresh): per-word truth re-read from the library
   *     (added / 已有 with the original kept / genuinely failed).
   *   - skips but snapshot unavailable (fresh === null): the server's
   *     counts stand and the skipped words are reported as UNVERIFIED —
   *     never silently upgraded to added, never falsely named failures.
   */
  PhotoAddForm.prototype.renderCommitResult = function (outcome, submittedRows, fresh) {
    var kit = global.__parentFormKit || {};
    if (kit.clearContainer) {
      kit.clearContainer(this.banner);
    } else {
      this.banner.textContent = '';
    }

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
    var raced = (outcome.skipped || 0) > 0;
    submittedRows.forEach(function (r, i) {
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
})(typeof window !== 'undefined' ? window : this);
