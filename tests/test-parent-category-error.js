// tests/test-parent-category-error.js — regression test for issue #42: the
// category field's setError must both write the copy AND bring the field
// into view (scroll + focus), otherwise an inline message off-screen reads
// as "the button did nothing". Clearing must stay silent (handleSubmit calls
// setError('') on every submit). `node tests/test-parent-category-error.js`.
'use strict';

const assert = require('node:assert');

// Minimal DOM shim — parent-modes.js only touches the DOM inside the builder
// functions, so requiring the IIFE is safe once `document` exists.
function makeNode(tag) {
  const listeners = {};
  const classes = {};
  const node = {
    tagName: tag,
    className: '',
    textContent: '',
    children: [],
    attrs: {},
    checked: false,
    scrollCalls: [],
    focusCalls: 0,
    setAttribute: function (name, value) {
      node.attrs[name] = value;
    },
    appendChild: function (child) {
      node.children.push(child);
      child.parentNode = node;
      return child;
    },
    addEventListener: function (type, fn) {
      (listeners[type] || (listeners[type] = [])).push(fn);
    },
    dispatch: function (type) {
      (listeners[type] || []).forEach(function (fn) {
        fn({ target: node });
      });
    },
    scrollIntoView: function (options) {
      node.scrollCalls.push(options);
    },
    focus: function () {
      node.focusCalls++;
    },
    classList: {
      toggle: function (cls, force) {
        const has = !!classes[cls];
        const on = force === undefined ? !has : !!force;
        if (on) {
          classes[cls] = true;
        } else {
          delete classes[cls];
        }
        node.className = Object.keys(classes).join(' ');
      },
    },
  };
  return node;
}

global.document = {
  createElement: function (tag) {
    return makeNode(tag);
  },
};

const { __parentFormKit } = require('../pkg/web/static/js/parent-modes.js');

// 1. Structural contract: [label, radiogroup, error]; the error line is a
//    polite live region so a screen reader announces the inline validation.
{
  const kit = __parentFormKit.buildCategoryField('category', null);
  assert.strictEqual(kit.field.children.length, 3);
  const error = kit.field.children[2];
  assert.strictEqual(error.className, 'form-error');
  assert.strictEqual(error.attrs['aria-live'], 'polite');
}

// 2. No pre-selection (D-06).
{
  const kit = __parentFormKit.buildCategoryField('category', null);
  assert.strictEqual(kit.selected(), '');
}

// 3. Non-empty setError writes the copy and brings the field to the user:
//    one scroll to center, one focus on the first radio (issue #42).
{
  const kit = __parentFormKit.buildCategoryField('category', null);
  const error = kit.field.children[2];
  const firstRadio = kit.field.children[1].children[0].children[0];

  kit.setError('请选择分类');

  assert.strictEqual(error.textContent, '请选择分类');
  assert.strictEqual(kit.field.scrollCalls.length, 1);
  assert.deepStrictEqual(kit.field.scrollCalls[0], { block: 'center' });
  assert.strictEqual(firstRadio.focusCalls, 1);
}

// 4. Clearing must be silent: no scroll, no focus steal (handleSubmit calls
//    setError('') before every submit).
{
  const kit = __parentFormKit.buildCategoryField('category', null);
  const error = kit.field.children[2];
  const firstRadio = kit.field.children[1].children[0].children[0];

  kit.setError('请选择分类');
  const scrollsBefore = kit.field.scrollCalls.length;
  const focusBefore = firstRadio.focusCalls;

  kit.setError('');

  assert.strictEqual(error.textContent, '');
  assert.strictEqual(kit.field.scrollCalls.length, scrollsBefore);
  assert.strictEqual(firstRadio.focusCalls, focusBefore);
}

// 5. Selecting a radio clears the inline error and reports the value.
{
  const kit = __parentFormKit.buildCategoryField('category', null);
  const error = kit.field.children[2];
  const firstRadio = kit.field.children[1].children[0].children[0];

  kit.setError('请选择分类');
  firstRadio.checked = true;
  firstRadio.dispatch('change');

  assert.strictEqual(error.textContent, '');
  assert.strictEqual(kit.selected(), 'english');
}

console.log('parent category error: OK');
