/**
 * @file Parent page — add-card components + settings card
 *       (Phase 9, Plans 01 + 06 + 07).
 *
 * AddCard: the 添加单词 card body — tabs 单个添加 | 批量添加 (D-10)
 * hosting the two entry forms.
 * SingleAddForm: final single-word form (D-06 category required with
 * 请选择分类 validation, D-08 only 单词 required, shared 单元 datalist,
 * loading spinner, success toast).
 * BatchAddForm: the textbook-lesson capture flow — paste → 解析并预览
 * (client-side window.parseBatch) → preview table with per-line Warning
 * rows → 确认添加（{n} 个） → result banner 已添加 {a} 个，跳过 {b} 个
 * 重复单词 (one lesson = one unit, D-10) plus the 13.3-03 consensus note
 * 新词将按复习安排选入课堂，不一定是下一课 (also on the single-add
 * success toast — closing the "加词 = 下一课" misread).
 * SettingsCard: the 学习设置 card (D-15) — two FSRS sliders (记忆保持率,
 * 最长复习间隔) with hints and live 当前/默认 readouts, 保存设置 → PUT
 * /api/v1/settings/fsrs, 恢复默认 resetting without a request, and the
 * future-only note (D-13).
 *
 * Contracts (UI-SPEC + plan 09-06 must-haves):
 *   - Category radio pair 英语|中文 — REQUIRED, NO pre-selection (D-06).
 *   - Only 单词 is required (D-08); meaning/example empty is valid.
 *   - Loading: submit buttons disabled + inline spinner, label KEPT.
 *   - Success (single): toast 已添加; word/meaning/example cleared while
 *     category and unit are RETAINED for rapid consecutive entry.
 *   - Transparency prohibition: a batch import is NEVER reported as
 *     plain success when words were skipped — both counts always render,
 *     and when skipped > 0 the banner turns Warning and lists the
 *     skipped words (derived from a pre-commit snapshot of the category,
 *     because the API returns only {added, skipped} counts; skips are
 *     UNIQUE(word, category), so skipped == pasted ∩ existing).
 *   - A batch over the API's 100-entry cap is refused client-side with a
 *     clear message (bounded request bodies, Plan 09-01).
 *
 * Security (Pitfall 10 / T-09-01): ALL data rendering goes through
 * document.createElement + Node.textContent — the raw-markup setter that
 * Pitfall 10 bans is never used anywhere in this file.
 * Containers clear via the firstChild-removeChild loop.
 */

(function (global) {
  'use strict';

  var TOAST_DURATION_MS = 2500;

  /**
   * 13.3-03 consensus 口径 line — appended to BOTH add success
   * feedbacks (single-add toast + batch result banner). It closes the
   * "加词 = 下一课" misread: the FSRS schedule picks lesson words, the
   * parent never does. textContent-only, never markup.
   */
  var SELECTION_NOTE_TEXT = '新词将按复习安排选入课堂，不一定是下一课。';

  /** Per-request entry cap — mirrors pkg/web/api/validate.go maxEntries. */
  var MAX_BATCH_ENTRIES = 100;

  /**
   * Show a transient top-center toast. Success (green) and error (red)
   * variants per UI-SPEC Toast contract (aria-live="polite").
   */
  function showToast(message, variant) {
    var existing = document.querySelector('.toast');
    if (existing && existing.parentNode) {
      existing.parentNode.removeChild(existing);
    }
    var toast = document.createElement('div');
    toast.className = 'toast toast-' + (variant === 'success' ? 'success' : 'error');
    toast.setAttribute('role', 'status');
    toast.setAttribute('aria-live', 'polite');
    toast.textContent = message;
    document.body.appendChild(toast);
    setTimeout(function () {
      if (toast.parentNode) {
        toast.parentNode.removeChild(toast);
      }
    }, TOAST_DURATION_MS);
  }

  /** Clear a container via the firstChild-removeChild loop (Pattern 4). */
  function clearContainer(container) {
    while (container.firstChild) {
      container.removeChild(container.firstChild);
    }
  }

  /**
   * Build a primary submit button with an inline spinner span and a kept
   * label (UI-SPEC Button — Loading contract). Toggling the `loading`
   * class reveals the spinner; the label text never changes.
   */
  function buildLoadingButton(labelText) {
    var btn = document.createElement('button');
    btn.type = 'submit';
    btn.className = 'btn btn-primary';
    var spinner = document.createElement('span');
    spinner.className = 'btn-spinner';
    spinner.setAttribute('aria-hidden', 'true');
    var label = document.createElement('span');
    label.className = 'btn-label';
    label.textContent = labelText;
    btn.appendChild(spinner);
    btn.appendChild(label);
    return btn;
  }

  /** Toggle a loading button's disabled + spinner state. */
  function setLoading(btn, loading) {
    btn.disabled = loading;
    btn.classList.toggle('loading', loading);
  }

  /**
   * Build one labeled text field. Returns {field, input, error}.
   * Validation error copy is per-field (aria-live polite).
   */
  function buildTextField(labelText, id, isTextarea) {
    var field = document.createElement('div');
    field.className = 'form-field';
    var label = document.createElement('label');
    label.className = 'form-label';
    label.setAttribute('for', id);
    label.textContent = labelText;
    var input;
    if (isTextarea) {
      input = document.createElement('textarea');
      input.rows = 3;
      input.className = 'form-textarea';
    } else {
      input = document.createElement('input');
      input.type = 'text';
      input.className = 'form-input';
    }
    input.id = id;
    input.name = id;
    input.setAttribute('autocomplete', 'off');
    var error = document.createElement('p');
    error.className = 'form-error';
    error.setAttribute('aria-live', 'polite');
    field.appendChild(label);
    field.appendChild(input);
    field.appendChild(error);
    return { field: field, input: input, error: error };
  }

  /**
   * Build the required category radio pair (D-06): 英语|中文, NO
   * pre-selection, 请选择分类 inline validation. options.onSelected fires
   * with the new value ('' before any selection) on every change.
   * Returns {field, selected(), setError(message)}.
   */
  function buildCategoryField(radioName, onSelected) {
    var field = document.createElement('div');
    field.className = 'form-field';

    var label = document.createElement('span');
    label.className = 'form-label';
    label.textContent = '分类';
    field.appendChild(label);

    var group = document.createElement('div');
    group.className = 'category-options';
    group.setAttribute('role', 'radiogroup');
    group.setAttribute('aria-label', '分类');

    var radios = [];
    var error = document.createElement('p');
    error.className = 'form-error';
    error.setAttribute('aria-live', 'polite');

    function selected() {
      for (var i = 0; i < radios.length; i++) {
        if (radios[i].radio.checked) {
          return radios[i].radio.value;
        }
      }
      return '';
    }

    [
      { value: 'english', text: '英语' },
      { value: 'chinese', text: '中文' },
    ].forEach(function (cat) {
      var option = document.createElement('label');
      option.className = 'category-option';
      var radio = document.createElement('input');
      radio.type = 'radio';
      radio.name = radioName;
      radio.value = cat.value;
      radio.addEventListener('change', function () {
        error.textContent = '';
        radios.forEach(function (other) {
          other.option.classList.toggle('checked', other.radio.checked);
        });
        if (onSelected) {
          onSelected(selected());
        }
      });
      var text = document.createElement('span');
      text.textContent = cat.text;
      option.appendChild(radio);
      option.appendChild(text);
      group.appendChild(option);
      radios.push({ radio: radio, option: option });
    });

    field.appendChild(group);
    field.appendChild(error);
    return {
      field: field,
      selected: selected,
      setError: function (message) {
        error.textContent = message;
      },
    };
  }

  /**
   * Build the 单元 free-text field associated with the SHARED datalist
   * (#unit-options lives in parent.html; parent-app.js populates it from
   * GET /api/v1/units for the currently selected category).
   * Returns {field, input}.
   */
  function buildUnitField(inputId) {
    var field = document.createElement('div');
    field.className = 'form-field';
    var label = document.createElement('label');
    label.className = 'form-label';
    label.setAttribute('for', inputId);
    label.textContent = '单元';
    var input = document.createElement('input');
    input.className = 'form-input';
    input.id = inputId;
    input.name = inputId;
    input.type = 'text';
    input.setAttribute('autocomplete', 'off');
    input.setAttribute('list', 'unit-options');
    field.appendChild(label);
    field.appendChild(input);
    return { field: field, input: input };
  }

  /**
   * Human-readable status text for one parser error row. Reasons come
   * from parent-batch-parser.js ('empty-word', 'word-comma',
   * 'word-too-long', 'duplicate:<firstLine>').
   */
  function describeParseError(err) {
    if (err.reason === 'empty-word') {
      return '这一行没有单词';
    }
    if (err.reason === 'word-comma') {
      return '单词不能包含英文逗号';
    }
    if (err.reason === 'word-too-long') {
      return '单词超过 200 个字符';
    }
    if (/^duplicate:/.test(err.reason)) {
      return '与第 ' + err.reason.slice('duplicate:'.length) + ' 行重复';
    }
    return err.reason;
  }

  // ------------------------------------------------------------------
  // SingleAddForm — final shape (single tab)
  // ------------------------------------------------------------------

  /**
   * options:
   *   onSubmit(payload)   → Promise — POST /api/v1/words
   *   onSuccess(result)   — fired after a successful add (refresh hook)
   *   onCategoryChange(v) — fired when the category radio changes
   */
  function SingleAddForm(container, options) {
    options = options || {};
    this.onSubmit = options.onSubmit || function () {
      return Promise.reject(new Error('SingleAddForm: missing onSubmit handler'));
    };
    this.onSuccess = options.onSuccess || function () {};
    this.onCategoryChange = options.onCategoryChange || function () {};

    this.form = document.createElement('form');
    this.form.className = 'single-add-form';
    this.form.noValidate = true;

    this.category = buildCategoryField('parent-category-single', this.onCategoryChange);
    this.form.appendChild(this.category.field);

    // 单词 required (D-08); 意思/例句 optional (例句 textarea, 3 rows).
    this.word = buildTextField('单词', 'parent-word', false);
    this.meaning = buildTextField('意思', 'parent-meaning', false);
    this.example = buildTextField('例句', 'parent-example', true);
    this.form.appendChild(this.word.field);
    this.form.appendChild(this.meaning.field);
    this.form.appendChild(this.example.field);

    // 单元 — free text over the SHARED datalist (id unit-options).
    var unit = buildUnitField('parent-unit');
    this.unitInput = unit.input;
    this.form.appendChild(unit.field);

    var self = this;
    this.submitBtn = buildLoadingButton('添加单词');
    this.form.appendChild(this.submitBtn);

    this.submitBtn.addEventListener('click', function (event) {
      event.preventDefault();
      self.handleSubmit();
    });
    this.form.addEventListener('submit', function (event) {
      event.preventDefault();
      self.handleSubmit();
    });

    container.appendChild(this.form);
  }

  SingleAddForm.prototype.handleSubmit = function () {
    if (this.submitBtn.disabled) {
      return;
    }
    this.category.setError('');
    this.word.error.textContent = '';

    var category = this.category.selected();
    if (!category) {
      this.category.setError('请选择分类');
      return;
    }
    var word = this.word.input.value.trim();
    if (!word) {
      this.word.error.textContent = '请输入单词';
      return;
    }

    var self = this;
    var payload = {
      category: category,
      unit: this.unitInput.value.trim(),
      entries: [
        {
          word: word,
          meaning: this.meaning.input.value.trim(),
          example: this.example.input.value,
        },
      ],
    };

    // Loading: disabled + spinner, label kept (UI-SPEC Button contract).
    setLoading(this.submitBtn, true);
    this.onSubmit(payload)
      .then(function (result) {
        showToast('已添加。' + SELECTION_NOTE_TEXT, 'success');
        // Clear word/meaning/example; RETAIN category + unit (auto-decided).
        self.word.input.value = '';
        self.meaning.input.value = '';
        self.example.input.value = '';
        self.onSuccess(result || { added: 0, skipped: 0 });
      })
      .catch(function (err) {
        var message = (err && err.message) ? err.message : '未知错误';
        showToast('保存失败：' + message + '。内容没有保存，请检查后重试。', 'error');
      })
      .then(function () {
        setLoading(self.submitBtn, false);
      });
  };

  // ------------------------------------------------------------------
  // BatchAddForm — paste → preview → commit (batch tab)
  // ------------------------------------------------------------------

  /**
   * options:
   *   submitWords(payload) → Promise — POST /api/v1/words
   *   fetchWords(category) → Promise<Array> — GET /api/v1/words?category=
   *       (pre-commit snapshot used to name the skipped words)
   *   onCommitted(result)  — fired after a successful commit (refresh hook)
   *   onCategoryChange(v)  — fired when the category radio changes
   */
  function BatchAddForm(container, options) {
    options = options || {};
    this.submitWords = options.submitWords || function () {
      return Promise.reject(new Error('BatchAddForm: missing submitWords handler'));
    };
    this.fetchWords = options.fetchWords || function () {
      return Promise.reject(new Error('BatchAddForm: missing fetchWords handler'));
    };
    this.onCommitted = options.onCommitted || function () {};
    this.onCategoryChange = options.onCategoryChange || function () {};

    this.form = document.createElement('form');
    this.form.className = 'batch-add-form';
    this.form.noValidate = true;

    this.category = buildCategoryField('parent-category-batch', this.onCategoryChange);
    this.form.appendChild(this.category.field);

    // 单元 applies to the WHOLE pasted lesson (D-10 one-lesson-one-unit).
    var unit = buildUnitField('parent-unit-batch');
    this.unitInput = unit.input;
    this.form.appendChild(unit.field);

    // Paste area (textarea, 8 rows per UI-SPEC Component Inventory).
    var pasteField = document.createElement('div');
    pasteField.className = 'form-field';
    var pasteLabel = document.createElement('label');
    pasteLabel.className = 'form-label';
    pasteLabel.setAttribute('for', 'parent-batch-paste');
    pasteLabel.textContent = '粘贴单词';
    var pasteHint = document.createElement('p');
    pasteHint.className = 'form-hint';
    pasteHint.textContent = '一行一个单词，可用 Tab 或空格分隔意思；# 开头的行会被忽略。';
    this.pasteInput = document.createElement('textarea');
    this.pasteInput.className = 'form-textarea';
    this.pasteInput.id = 'parent-batch-paste';
    this.pasteInput.name = 'parent-batch-paste';
    this.pasteInput.rows = 8;
    this.pasteInput.setAttribute('autocomplete', 'off');
    this.pasteInput.setAttribute('spellcheck', 'false');
    pasteField.appendChild(pasteLabel);
    pasteField.appendChild(pasteHint);
    pasteField.appendChild(this.pasteInput);
    this.form.appendChild(pasteField);

    // 解析并预览 (secondary — the accent stays on 确认添加).
    this.parseBtn = document.createElement('button');
    this.parseBtn.type = 'button';
    this.parseBtn.className = 'btn btn-secondary';
    this.parseBtn.textContent = '解析并预览';
    this.form.appendChild(this.parseBtn);

    // Preview: empty/unparsed state until the first parse (UI Consideration:
    // 粘贴后点「解析并预览」, commit stays disabled).
    this.preview = document.createElement('div');
    this.preview.className = 'preview-scroll';
    this.validRows = [];
    this.parseErrors = [];
    this.renderPreview({ rows: [], errors: [] }, '');
    this.form.appendChild(this.preview);

    // 确认添加（{n} 个） — disabled at zero valid rows.
    this.commitBtn = buildLoadingButton('确认添加（0 个）');
    this.commitBtn.disabled = true;
    this.form.appendChild(this.commitBtn);

    this.formError = document.createElement('p');
    this.formError.className = 'form-error';
    this.formError.setAttribute('aria-live', 'polite');
    this.formError.hidden = true;
    this.form.appendChild(this.formError);

    // Result banner host (rendered after each commit).
    this.banner = document.createElement('div');
    this.form.appendChild(this.banner);

    var self = this;
    this.parseBtn.addEventListener('click', function () {
      self.handleParse();
    });
    this.commitBtn.addEventListener('click', function (event) {
      event.preventDefault();
      self.handleCommit();
    });
    this.form.addEventListener('submit', function (event) {
      event.preventDefault();
      self.handleCommit();
    });

    container.appendChild(this.form);
  }

  /** Show a one-line problem under the commit button. */
  BatchAddForm.prototype.showFormError = function (message) {
    this.formError.textContent = message;
    this.formError.hidden = false;
  };

  /** Parse the paste box and render the preview table. */
  BatchAddForm.prototype.handleParse = function () {
    this.formError.hidden = true;
    if (typeof global.parseBatch !== 'function') {
      this.showFormError('解析器未加载，请刷新页面重试');
      return;
    }
    var raw = this.pasteInput.value;
    var result = global.parseBatch(raw);
    this.validRows = result.rows;
    this.parseErrors = result.errors;
    this.renderPreview(result, raw);
    this.updateCommitButton();
  };

  /**
   * Render the preview table: valid rows first (numbered 1..n, status
   * 将添加), then per-line problems as Warning rows (the # cell shows the
   * source line number; error rows are excluded from the commit count).
   */
  BatchAddForm.prototype.renderPreview = function (result, raw) {
    clearContainer(this.preview);

    if (!result.rows.length && !result.errors.length) {
      var note = document.createElement('p');
      note.className = 'preview-empty';
      note.textContent = raw.trim()
        ? '没有可添加的单词，请检查格式'
        : '粘贴后点「解析并预览」';
      this.preview.appendChild(note);
      return;
    }

    var table = document.createElement('table');
    table.className = 'preview-table';

    var thead = document.createElement('thead');
    var headRow = document.createElement('tr');
    ['#', '单词', '意思', '状态'].forEach(function (text) {
      var th = document.createElement('th');
      th.setAttribute('scope', 'col');
      th.textContent = text;
      headRow.appendChild(th);
    });
    thead.appendChild(headRow);
    table.appendChild(thead);

    var tbody = document.createElement('tbody');
    result.rows.forEach(function (row, i) {
      var tr = document.createElement('tr');
      tr.className = 'preview-row';
      appendCell(tr, String(i + 1), 'preview-index');
      appendCell(tr, row.word, 'preview-word');
      appendCell(tr, row.meaning, 'preview-meaning');
      appendCell(tr, '将添加', 'preview-status preview-status-valid');
      tbody.appendChild(tr);
    });
    result.errors.forEach(function (err) {
      var tr = document.createElement('tr');
      tr.className = 'preview-row preview-row-warning';
      appendCell(tr, String(err.line), 'preview-index');
      appendCell(tr, '—', 'preview-word');
      appendCell(tr, '', 'preview-meaning');
      appendCell(tr, describeParseError(err), 'preview-status');
      tbody.appendChild(tr);
    });
    table.appendChild(tbody);
    this.preview.appendChild(table);
  };

  /** td builder — textContent only (Pitfall 10). */
  function appendCell(row, text, className) {
    var td = document.createElement('td');
    td.className = className;
    td.textContent = text;
    row.appendChild(td);
    return td;
  }

  BatchAddForm.prototype.updateCommitButton = function () {
    var label = this.commitBtn.querySelector('.btn-label');
    var n = this.validRows.length;
    if (label) {
      label.textContent = '确认添加（' + n + ' 个）';
    }
    this.commitBtn.disabled = n === 0;
  };

  BatchAddForm.prototype.handleCommit = function () {
    if (this.commitBtn.disabled) {
      return;
    }
    this.formError.hidden = true;
    clearContainer(this.banner);

    var category = this.category.selected();
    if (!category) {
      this.category.setError('请选择分类');
      return;
    }
    var rows = this.validRows;
    if (!rows.length) {
      this.showFormError('请先粘贴并点「解析并预览」');
      return;
    }
    if (rows.length > MAX_BATCH_ENTRIES) {
      this.showFormError(
        '一次最多添加 ' + MAX_BATCH_ENTRIES + ' 个单词，请分两次粘贴');
      return;
    }

    var self = this;
    var unit = this.unitInput.value.trim();
    var entries = rows.map(function (row) {
      return { word: row.word, meaning: row.meaning, example: '' };
    });

    setLoading(this.commitBtn, true);
    this.parseBtn.disabled = true;

    // Pre-commit snapshot of the category's existing words so the result
    // banner can NAME the skipped words (the API reports only {added,
    // skipped} counts; skips are UNIQUE(word, category), so skipped ==
    // pasted ∩ existing). Best-effort: on failure the banner still shows
    // the honest counts with a generic note (transparency never depends
    // on the snapshot succeeding).
    this.fetchWords(category)
      .catch(function () {
        return [];
      })
      .then(function (existing) {
        var existingSet = {};
        (existing || []).forEach(function (w) {
          if (w && w.word) {
            existingSet[w.word] = true;
          }
        });
        return self.submitWords({
          category: category,
          unit: unit,
          entries: entries,
        }).then(function (result) {
          var outcome = result || { added: 0, skipped: 0 };
          self.renderResult(outcome, rows, existingSet);
          // Reset for the next paste; category + unit retained.
          self.pasteInput.value = '';
          self.validRows = [];
          self.parseErrors = [];
          self.renderPreview({ rows: [], errors: [] }, '');
          self.updateCommitButton();
          self.onCommitted(outcome);
        });
      })
      .catch(function (err) {
        var message = (err && err.message) ? err.message : '未知错误';
        showToast('保存失败：' + message + '。内容没有保存，请检查后重试。', 'error');
      })
      .then(function () {
        setLoading(self.commitBtn, false);
        self.parseBtn.disabled = false;
      });
  };

  /**
   * Result banner per the transparency prohibition: 已添加 {a} 个，跳过
   * {b} 个重复单词 — success colors when b=0, Warning colors plus the
   * readable skipped-word list when b>0. Counts are always the server's.
   */
  BatchAddForm.prototype.renderResult = function (result, rows, existingSet) {
    clearContainer(this.banner);
    var added = result.added || 0;
    var skipped = result.skipped || 0;

    var box = document.createElement('div');
    box.className = 'result-banner ' +
      (skipped > 0 ? 'result-banner-warning' : 'result-banner-success');
    box.setAttribute('role', 'status');
    box.setAttribute('aria-live', 'polite');

    var summary = document.createElement('p');
    summary.className = 'result-summary';
    summary.textContent = '已添加 ' + added + ' 个，跳过 ' + skipped + ' 个重复单词';
    box.appendChild(summary);

    if (skipped > 0) {
      var skippedWords = rows
        .map(function (row) {
          return row.word;
        })
        .filter(function (word) {
          return existingSet[word];
        });
      var detail = document.createElement('p');
      detail.className = 'result-detail';
      detail.textContent = skippedWords.length
        ? '重复单词：' + skippedWords.join('、')
        : '重复单词已跳过';
      box.appendChild(detail);
    }

    // 13.3-03 consensus 口径 line on EVERY batch success (appended after
    // the counts + skipped list so it reads as the closing note).
    var note = document.createElement('p');
    note.className = 'result-note';
    note.textContent = SELECTION_NOTE_TEXT;
    box.appendChild(note);

    this.banner.appendChild(box);
  };

  // ------------------------------------------------------------------
  // AddCard — tabs 单个添加 | 批量添加 (D-10) hosting both forms
  // ------------------------------------------------------------------

  /**
   * options:
   *   submitWords(payload)     → Promise — shared POST /api/v1/words
   *   fetchWords(category)     → Promise<Array> — batch snapshot source
   *   onCommitted(result)      — fired after ANY successful add
   *   onCategoryChange(value)  — fired when either form's radio changes
   *   recognizePhoto(blob)     → Promise<{textbook, entries}> — POST
   *       /api/v1/words/photo/recognize (Issue #3). Present ⇒ the third tab
   *       照片导入 mounts (PhotoAddForm from parent-photo.js); absent ⇒ the
   *       card stays two-tab so older embeds degrade instead of breaking.
   *   fetchTextbooks / createTextbook / updateTextbook / assignWords —
   *       the Issue #4 textbook surfaces, passed through to PhotoAddForm's
   *       Issue #5 attribution panel. All-or-nothing: absent ⇒ the panel
   *       degrades to the plain supplementary flow.
   */
  function AddCard(container, options) {
    options = options || {};
    var submitWords = options.submitWords || function () {
      return Promise.reject(new Error('AddCard: missing submitWords handler'));
    };
    var fetchWords = options.fetchWords || function () {
      return Promise.reject(new Error('AddCard: missing fetchWords handler'));
    };
    var onCommitted = options.onCommitted || function () {};
    var onCategoryChange = options.onCategoryChange || function () {};
    var recognizePhoto = options.recognizePhoto || null;
    var textbookDeps = {
      fetchTextbooks: options.fetchTextbooks || null,
      createTextbook: options.createTextbook || null,
      updateTextbook: options.updateTextbook || null,
      assignWords: options.assignWords || null,
      // Issue #6: the photo form's 采用照片释义 resolution rides the
      // ordinary edit path (passed through to PhotoAddForm).
      editWord: options.editWord || null,
    };

    var tablist = document.createElement('div');
    tablist.className = 'add-tabs';
    tablist.setAttribute('role', 'tablist');
    tablist.setAttribute('aria-label', '添加方式');

    var panelSingle = document.createElement('div');
    panelSingle.className = 'add-panel';
    panelSingle.id = 'add-panel-single';
    panelSingle.setAttribute('role', 'tabpanel');
    panelSingle.setAttribute('aria-labelledby', 'add-tab-single');

    var panelBatch = document.createElement('div');
    panelBatch.className = 'add-panel';
    panelBatch.id = 'add-panel-batch';
    panelBatch.setAttribute('role', 'tabpanel');
    panelBatch.setAttribute('aria-labelledby', 'add-tab-batch');
    panelBatch.hidden = true;

    var tabDefs = [
      { id: 'add-tab-single', text: '单个添加', panel: panelSingle },
      { id: 'add-tab-batch', text: '批量添加', panel: panelBatch },
    ];

    // Issue #3: the 照片导入 tab mounts only when both the handler wiring
    // (parent-app.js) and the component (parent-photo.js) are present — a
    // missing script degrades to the two-tab card, never a dead tab.
    var panelPhoto = null;
    if (recognizePhoto && typeof global.PhotoAddForm === 'function') {
      panelPhoto = document.createElement('div');
      panelPhoto.className = 'add-panel';
      panelPhoto.id = 'add-panel-photo';
      panelPhoto.setAttribute('role', 'tabpanel');
      panelPhoto.setAttribute('aria-labelledby', 'add-tab-photo');
      panelPhoto.hidden = true;
      tabDefs.push({ id: 'add-tab-photo', text: '照片导入', panel: panelPhoto });
    } else if (recognizePhoto) {
      console.error('[parent-modes] PhotoAddForm unavailable — 照片导入 tab not mounted.');
    }

    var tabPairs = [];
    tabDefs.forEach(function (def, index) {
      var btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'add-tab' + (index === 0 ? ' active' : '');
      btn.id = def.id;
      btn.setAttribute('role', 'tab');
      btn.setAttribute('aria-selected', index === 0 ? 'true' : 'false');
      btn.setAttribute('aria-controls', def.panel.id);
      btn.textContent = def.text;
      tabPairs.push({ btn: btn, panel: def.panel });
      btn.addEventListener('click', function () {
        tabPairs.forEach(function (pair) {
          var active = pair.btn === btn;
          pair.panel.hidden = !active;
          pair.btn.classList.toggle('active', active);
          pair.btn.setAttribute('aria-selected', active ? 'true' : 'false');
        });
      });
      tablist.appendChild(btn);
    });

    this.single = new SingleAddForm(panelSingle, {
      onSubmit: submitWords,
      onSuccess: onCommitted,
      onCategoryChange: onCategoryChange,
    });
    this.batch = new BatchAddForm(panelBatch, {
      submitWords: submitWords,
      fetchWords: fetchWords,
      onCommitted: onCommitted,
      onCategoryChange: onCategoryChange,
    });
    if (panelPhoto) {
      this.photo = new global.PhotoAddForm(panelPhoto, {
        recognizePhoto: recognizePhoto,
        submitWords: submitWords,
        fetchWords: fetchWords,
        textbookDeps: textbookDeps,
        // Issue #8: the missing-meaning supplement call (absent ⇒ the
        // affordance never mounts; the import flow is unchanged).
        generateMeanings: options.generateMeanings || null,
        onCommitted: onCommitted,
        onCategoryChange: onCategoryChange,
      });
    }

    container.appendChild(tablist);
    container.appendChild(panelSingle);
    container.appendChild(panelBatch);
    if (panelPhoto) {
      container.appendChild(panelPhoto);
    }
  }

  // ------------------------------------------------------------------
  // SettingsCard — 学习设置 card (D-15, Plan 09-07 Task 3): two FSRS
  // sliders with plain-language hints, live 当前 {v} · 默认 {d} readouts,
  // 保存设置 / 恢复默认, and the future-only note (D-13). Embedded in the
  // page — NOT a separate page.
  // ------------------------------------------------------------------

  // Slider windows per plan 09-07 (the server enforces the same
  // retention window; the interval slider tops at 365 — values above it
  // were never settable from this UI).
  var FSRS_RETENTION_MIN = 0.7;
  var FSRS_RETENTION_MAX = 0.95;
  var FSRS_RETENTION_DEFAULT = 0.85;
  var FSRS_INTERVAL_MIN = 1;
  var FSRS_INTERVAL_MAX = 365;
  var FSRS_INTERVAL_DEFAULT = 180;

  function formatRetention(value) {
    return Number(value).toFixed(2);
  }

  function formatInterval(value) {
    return String(Math.round(value));
  }

  /** Accent track fill for input[type=range] (UI-SPEC Slider row). */
  function updateRangeFill(input) {
    var min = parseFloat(input.min);
    var max = parseFloat(input.max);
    var value = parseFloat(input.value);
    if (!isFinite(min) || !isFinite(max) || !isFinite(value) || max === min) {
      return;
    }
    var pct = ((value - min) / (max - min)) * 100;
    input.style.setProperty('--range-fill', pct + '%');
  }

  /**
   * Build one slider field: label + input[type=range] + hint + live
   * readout 当前 {v} · 默认 {d}. Returns {field, input, setValue(value)}.
   */
  function buildSliderField(spec) {
    var field = document.createElement('div');
    field.className = 'form-field slider-field';

    var label = document.createElement('label');
    label.className = 'form-label';
    label.setAttribute('for', spec.id);
    label.textContent = spec.labelText;
    field.appendChild(label);

    var input = document.createElement('input');
    input.type = 'range';
    input.className = 'range-input';
    input.id = spec.id;
    input.name = spec.id;
    input.min = String(spec.min);
    input.max = String(spec.max);
    input.step = String(spec.step);
    field.appendChild(input);

    var hint = document.createElement('p');
    hint.className = 'form-hint';
    hint.textContent = spec.hint;
    field.appendChild(hint);

    var readout = document.createElement('p');
    readout.className = 'slider-readout';

    function update() {
      var value = parseFloat(input.value);
      if (!isFinite(value)) {
        value = spec.defaultValue;
      }
      readout.textContent = '当前 ' + spec.format(value) +
        ' · 默认 ' + spec.format(spec.defaultValue);
      updateRangeFill(input);
    }

    input.addEventListener('input', update);
    field.appendChild(readout);

    return {
      field: field,
      input: input,
      readout: readout,
      setValue: function (value) {
        input.value = String(value);
        update();
      },
    };
  }

  /**
   * options:
   *   fetchSettings()        → Promise — GET /api/v1/settings/fsrs
   *   saveSettings(settings) → Promise — PUT /api/v1/settings/fsrs
   *
   * Mounting unhides the 学习设置 section (parent.html keeps it hidden
   * until a real component exists — tracer-era contract).
   */
  function SettingsCard(container, options) {
    options = options || {};
    this.fetchSettings = options.fetchSettings || function () {
      return Promise.reject(new Error('SettingsCard: missing fetchSettings handler'));
    };
    this.saveSettings = options.saveSettings || function () {
      return Promise.reject(new Error('SettingsCard: missing saveSettings handler'));
    };
    this.container = container;

    if (typeof container.closest === 'function') {
      var section = container.closest('.settings-section');
      if (section) {
        section.hidden = false;
      }
    }

    this.build();

    var self = this;
    this.fetchSettings()
      .then(function (settings) {
        self.applySettings(settings || {});
      })
      .catch(function () {
        // A failed initial load falls back to the documented defaults in
        // the sliders; the parent can still review and save them.
        self.applySettings({});
      });
  }

  SettingsCard.prototype.build = function () {
    var form = document.createElement('form');
    form.className = 'settings-form';
    form.noValidate = true;

    this.retentionField = buildSliderField({
      labelText: '记忆保持率',
      id: 'settings-retention',
      min: FSRS_RETENTION_MIN,
      max: FSRS_RETENTION_MAX,
      step: 0.01,
      hint: '保持率越高，复习越频繁，每天练习量越大。建议 0.85–0.90。',
      defaultValue: FSRS_RETENTION_DEFAULT,
      format: formatRetention,
    });
    form.appendChild(this.retentionField.field);

    this.intervalField = buildSliderField({
      labelText: '最长复习间隔',
      id: 'settings-interval',
      min: FSRS_INTERVAL_MIN,
      max: FSRS_INTERVAL_MAX,
      step: 1,
      hint: '两次复习之间最多隔多少天。默认 180 天。',
      defaultValue: FSRS_INTERVAL_DEFAULT,
      format: formatInterval,
    });
    form.appendChild(this.intervalField.field);

    // D-13 future-only note.
    var note = document.createElement('p');
    note.className = 'form-hint settings-note';
    note.textContent = '只影响以后的复习安排，不会改动已学单词的进度。';
    form.appendChild(note);

    var actions = document.createElement('div');
    actions.className = 'settings-actions';
    this.saveBtn = buildLoadingButton('保存设置');
    this.saveBtn.type = 'submit';
    this.restoreBtn = document.createElement('button');
    this.restoreBtn.type = 'button';
    this.restoreBtn.className = 'btn btn-secondary';
    this.restoreBtn.textContent = '恢复默认';
    actions.appendChild(this.saveBtn);
    actions.appendChild(this.restoreBtn);
    form.appendChild(actions);

    this.container.appendChild(form);

    var self = this;
    // 恢复默认 resets BOTH sliders and readouts WITHOUT saving — no
    // request is issued; values only persist when 保存设置 is clicked.
    this.restoreBtn.addEventListener('click', function () {
      self.retentionField.setValue(FSRS_RETENTION_DEFAULT);
      self.intervalField.setValue(FSRS_INTERVAL_DEFAULT);
    });
    this.saveBtn.addEventListener('click', function (event) {
      event.preventDefault();
      self.handleSave();
    });
    form.addEventListener('submit', function (event) {
      event.preventDefault();
      self.handleSave();
    });
  };

  /** Clamp loaded/typed values into the slider windows and render. */
  SettingsCard.prototype.applySettings = function (settings) {
    var retention = parseFloat(settings.desired_retention);
    if (!isFinite(retention)) {
      retention = FSRS_RETENTION_DEFAULT;
    }
    retention = Math.min(FSRS_RETENTION_MAX, Math.max(FSRS_RETENTION_MIN, retention));

    var interval = parseInt(settings.maximum_interval, 10);
    if (!isFinite(interval)) {
      interval = FSRS_INTERVAL_DEFAULT;
    }
    interval = Math.min(FSRS_INTERVAL_MAX, Math.max(FSRS_INTERVAL_MIN, interval));

    this.retentionField.setValue(retention);
    this.intervalField.setValue(interval);
  };

  /** 保存设置: clamped values → PUT → toast 已保存，下次听写生效. */
  SettingsCard.prototype.handleSave = function () {
    if (this.saveBtn.disabled) {
      return;
    }
    var retention = parseFloat(this.retentionField.input.value);
    if (!isFinite(retention)) {
      retention = FSRS_RETENTION_DEFAULT;
    }
    retention = Math.min(FSRS_RETENTION_MAX, Math.max(FSRS_RETENTION_MIN, retention));
    retention = Math.round(retention * 100) / 100;

    var interval = parseInt(this.intervalField.input.value, 10);
    if (!isFinite(interval) || interval < FSRS_INTERVAL_MIN) {
      interval = FSRS_INTERVAL_MIN;
    }
    if (interval > FSRS_INTERVAL_MAX) {
      interval = FSRS_INTERVAL_MAX;
    }

    var self = this;
    setLoading(this.saveBtn, true);
    this.saveSettings({
      desired_retention: retention,
      maximum_interval: interval,
    }).then(function () {
      showToast('已保存，下次听写生效', 'success');
    }).catch(function (err) {
      var message = (err && err.message) ? err.message : '未知错误';
      showToast('保存失败：' + message + '。内容没有保存，请检查后重试。', 'error');
    }).then(function () {
      setLoading(self.saveBtn, false);
    });
  };

  // Window-or-module-global tail (state-manager.js convention) so Node
  // tests can require this file later.
  global.SingleAddForm = SingleAddForm;
  global.BatchAddForm = BatchAddForm;
  global.AddCard = AddCard;
  global.SettingsCard = SettingsCard;
  global.__parentToast = showToast;
  // Shared form builders for sibling components (parent-photo.js mounts in
  // the same page and reuses the exact category/unit/button/toast builders
  // so the add forms stay visually and behaviorally one family).
  global.__parentFormKit = {
    buildCategoryField: buildCategoryField,
    buildUnitField: buildUnitField,
    buildTextField: buildTextField,
    buildLoadingButton: buildLoadingButton,
    setLoading: setLoading,
    showToast: showToast,
    clearContainer: clearContainer,
    SELECTION_NOTE_TEXT: SELECTION_NOTE_TEXT,
  };
})(typeof window !== 'undefined' ? window : this);
