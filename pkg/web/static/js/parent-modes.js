/**
 * @file Parent page — add-form component (Phase 9, Plan 01 tracer).
 *
 * SingleAddForm: the 单个添加 form for the 添加单词 card.
 * Mounted into a container by parent-app.js (class-per-component per
 * ui-components.js conventions).
 *
 * Contract (UI-SPEC + D-06/D-08):
 *   - Category radio pair 英语|中文 — REQUIRED, NO pre-selection (D-06);
 *     submitting without a choice shows 请选择分类.
 *   - 单词 input is the only required field (D-08); 意思/例句 optional;
 *     单元 is free text with an associated <datalist id="unit-options">
 *     (populated later in Plan 09-06).
 *   - Loading: submit button disabled, label KEPT (UI-SPEC Button contract).
 *   - Success: toast 已添加; word/meaning/example cleared while category
 *     and unit are RETAINED for rapid consecutive entry (auto-decided).
 *   - Error: toast 保存失败：{message}。内容没有保存，请检查后重试。
 *
 * Security (Pitfall 10 / T-09-01): ALL data rendering goes through
 * document.createElement + Node.textContent — never innerHTML.
 */

(function (global) {
  'use strict';

  var TOAST_DURATION_MS = 2500;

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

  function SingleAddForm(container, options) {
    options = options || {};
    this.onSubmit = options.onSubmit || function () {
      return Promise.reject(new Error('SingleAddForm: missing onSubmit handler'));
    };
    this.onSuccess = options.onSuccess || function () {};

    this.form = document.createElement('form');
    this.form.className = 'single-add-form';
    this.form.noValidate = true;

    // ---- Category radio pair (required, NO pre-selection, D-06) ----
    var catField = document.createElement('div');
    catField.className = 'form-field';

    var catLabel = document.createElement('span');
    catLabel.className = 'form-label';
    catLabel.textContent = '分类';
    catField.appendChild(catLabel);

    this.categoryOptions = document.createElement('div');
    this.categoryOptions.className = 'category-options';
    this.categoryOptions.setAttribute('role', 'radiogroup');
    this.categoryOptions.setAttribute('aria-label', '分类');

    var self = this;
    this.categoryRadios = [];
    [
      { value: 'english', text: '英语' },
      { value: 'chinese', text: '中文' },
    ].forEach(function (cat) {
      var option = document.createElement('label');
      option.className = 'category-option';
      var radio = document.createElement('input');
      radio.type = 'radio';
      radio.name = 'parent-category';
      radio.value = cat.value;
      radio.addEventListener('change', function () {
        self.categoryError.textContent = '';
        self.categoryRadios.forEach(function (other) {
          other.option.classList.toggle('checked', other.radio.checked);
        });
      });
      var text = document.createElement('span');
      text.textContent = cat.text;
      option.appendChild(radio);
      option.appendChild(text);
      self.categoryOptions.appendChild(option);
      self.categoryRadios.push({ radio: radio, option: option });
    });
    catField.appendChild(this.categoryOptions);

    this.categoryError = document.createElement('p');
    this.categoryError.className = 'form-error';
    this.categoryError.setAttribute('aria-live', 'polite');
    catField.appendChild(this.categoryError);
    this.form.appendChild(catField);

    // ---- 单词 (required, D-08) ----
    this.form.appendChild(this.buildField(
      '单词', 'parent-word', false, '请输入单词'));

    // ---- 意思 (optional) ----
    this.form.appendChild(this.buildField('意思', 'parent-meaning', false, ''));

    // ---- 例句 (optional, textarea) ----
    this.form.appendChild(this.buildField('例句', 'parent-example', true, ''));

    // ---- 单元 (free text + datalist; options populated in Plan 09-06) ----
    var unitField = document.createElement('div');
    unitField.className = 'form-field';
    var unitLabel = document.createElement('label');
    unitLabel.className = 'form-label';
    unitLabel.setAttribute('for', 'parent-unit');
    unitLabel.textContent = '单元';
    this.unitInput = document.createElement('input');
    this.unitInput.className = 'form-input';
    this.unitInput.id = 'parent-unit';
    this.unitInput.name = 'unit';
    this.unitInput.type = 'text';
    this.unitInput.setAttribute('autocomplete', 'off');
    this.unitInput.setAttribute('list', 'unit-options');
    this.unitList = document.createElement('datalist');
    this.unitList.id = 'unit-options';
    unitField.appendChild(unitLabel);
    unitField.appendChild(this.unitInput);
    unitField.appendChild(this.unitList);
    this.form.appendChild(unitField);

    // ---- Submit button ----
    this.submitBtn = document.createElement('button');
    this.submitBtn.type = 'submit';
    this.submitBtn.className = 'btn btn-primary';
    this.submitBtn.textContent = '添加单词';
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

  /**
   * Build one labeled text field. Validation error copy is per-field.
   */
  SingleAddForm.prototype.buildField = function (labelText, id, isTextarea, requiredMessage) {
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
    input.required = !!requiredMessage;
    var error = document.createElement('p');
    error.className = 'form-error';
    error.setAttribute('aria-live', 'polite');
    field.appendChild(label);
    field.appendChild(input);
    field.appendChild(error);
    this[id + 'Input'] = input;
    this[id + 'Error'] = error;
    return field;
  };

  SingleAddForm.prototype.selectedCategory = function () {
    for (var i = 0; i < this.categoryRadios.length; i++) {
      if (this.categoryRadios[i].radio.checked) {
        return this.categoryRadios[i].radio.value;
      }
    }
    return '';
  };

  SingleAddForm.prototype.handleSubmit = function () {
    if (this.submitBtn.disabled) {
      return;
    }
    this.categoryError.textContent = '';
    this['parent-wordError'].textContent = '';

    var category = this.selectedCategory();
    if (!category) {
      this.categoryError.textContent = '请选择分类';
      return;
    }
    var word = this['parent-wordInput'].value.trim();
    if (!word) {
      this['parent-wordError'].textContent = '请输入单词';
      return;
    }

    var self = this;
    var payload = {
      category: category,
      unit: this.unitInput.value.trim(),
      entries: [
        {
          word: word,
          meaning: this['parent-meaningInput'].value.trim(),
          example: this['parent-exampleInput'].value,
        },
      ],
    };

    // Loading: disabled + label kept (UI-SPEC Button contract).
    this.submitBtn.disabled = true;
    this.onSubmit(payload)
      .then(function (result) {
        showToast('已添加', 'success');
        // Clear word/meaning/example; RETAIN category + unit (auto-decided).
        self['parent-wordInput'].value = '';
        self['parent-meaningInput'].value = '';
        self['parent-exampleInput'].value = '';
        self.onSuccess(result || { added: 0, skipped: 0 });
      })
      .catch(function (err) {
        var message = (err && err.message) ? err.message : '未知错误';
        showToast('保存失败：' + message + '。内容没有保存，请检查后重试。', 'error');
      })
      .then(function () {
        self.submitBtn.disabled = false;
      });
  };

  // Window-or-module-global tail (state-manager.js convention) so Node
  // tests can require this file later.
  global.SingleAddForm = SingleAddForm;
  global.__parentToast = showToast;
})(typeof window !== 'undefined' ? window : this);
