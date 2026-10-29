'use strict';

/**
 * Mount PhotoAddForm against the minimal DOM for workflow entry tests
 * (Issue #80 U2). Stubs compression, vision, and fetches — not the
 * init/refresh/save/edit methods under test.
 */
const path = require('node:path');
const { createPhotoDom } = require('./photo-workflow-dom.js');

function tick() {
  return new Promise(function (resolve) {
    setImmediate(resolve);
  });
}

function flushPromises(count) {
  var chain = Promise.resolve();
  for (var i = 0; i < count; i++) {
    chain = chain.then(tick);
  }
  return chain;
}

function fakePhoto(name) {
  return {
    name: name || 'list.jpg',
    size: 2048,
    type: 'image/jpeg',
  };
}

function fakePdf(name) {
  return {
    name: name || 'scan.pdf',
    size: 4096,
    type: 'application/pdf',
  };
}

function installWorkflowGlobals(dom, options) {
  options = options || {};
  global.window = global;
  global.document = dom.document;
  global.URL.createObjectURL = function () { return 'blob:test'; };
  global.URL.revokeObjectURL = function () {};

  global.__parentFormKit = {
    buildCategoryField: function (_name, onChange) {
      // The real kit's contract (parent-modes.js D-06): 英语|中文 radios
      // whose change fires onSelected(selected()) — tests drive the REAL
      // subject switch by firing a radio's change, never by calling the
      // form's method. 英语 pre-checked: the photo form's recognize gate
      // requires a selection before any run.
      var field = dom.document.createElement('div');
      var radios = ['english', 'chinese'].map(function (value) {
        var radio = dom.document.createElement('input');
        radio.type = 'radio';
        radio.value = value;
        radio.checked = value === 'english';
        field.appendChild(radio);
        return radio;
      });
      radios.forEach(function (radio) {
        radio.addEventListener('change', function () {
          radios.forEach(function (other) {
            other.checked = other === radio;
          });
          if (onChange) {
            onChange(radio.value);
          }
        });
      });
      return {
        field: field,
        selected: function () {
          var picked = radios.filter(function (r) { return r.checked; })[0];
          return picked ? picked.value : '';
        },
        setError: function () {},
      };
    },
    buildUnitField: function () {
      var field = dom.document.createElement('div');
      var input = dom.document.createElement('input');
      field.appendChild(input);
      return { field: field, input: input };
    },
    buildLoadingButton: function (label) {
      var btn = dom.document.createElement('button');
      btn.textContent = label;
      return btn;
    },
    clearContainer: function (el) {
      el.textContent = '';
      el.childNodes.length = 0;
    },
  };

  require(path.resolve(__dirname, '..', '..', 'pkg', 'web', 'static', 'js', 'parent-meaning-core.js'));
  require(path.resolve(__dirname, '..', '..', 'pkg', 'web', 'static', 'js', 'parent-textbook.js'));
  require(path.resolve(__dirname, '..', '..', 'pkg', 'web', 'static', 'js', 'parent-batch-edit.js'));
  require(path.resolve(__dirname, '..', '..', 'pkg', 'web', 'static', 'js', 'parent-import-core.js'));
  require(path.resolve(__dirname, '..', '..', 'pkg', 'web', 'static', 'js', 'parent-pdf-core.js'));
  require(path.resolve(__dirname, '..', '..', 'pkg', 'web', 'static', 'js', 'parent-photo.js'));

  global.PdfPageRunner = {
    isAvailable: function () { return true; },
    open: function () {
      return Promise.resolve({
        doc: { id: 'doc1' },
        pageCount: options.pdfPageCount || 1,
      });
    },
    close: function () {},
    renderPage: function () {
      return Promise.resolve(new Blob(['page']));
    },
  };
}

/**
 * @param {object} [options]
 * @param {object} [options.modalKit] — __parentModalKit (openModal spy)
 * @param {Function} [options.recognizePhoto]
 * @param {number} [options.pdfPageCount] — pages the fake PDF reports
 */
function mountWorkflowForm(options) {
  options = options || {};
  var dom = createPhotoDom();
  installWorkflowGlobals(dom, options);

  var root = dom.document.createElement('div');
  var modalCalls = [];
  global.__parentModalKit = Object.assign({
    openModal: function (spec) {
      modalCalls.push(spec);
    },
  }, options.modalKit || {});

  var form = new global.PhotoAddForm(root, {
    recognizePhoto: options.recognizePhoto || function () {
      return Promise.resolve({
        entries: [{ word: 'alpha', meaning: '甲', lesson: '' }],
        textbook: { edition: '人教版PEP', grade: '三年级', volume: '上册' },
      });
    },
    fetchWords: function () { return Promise.resolve([]); },
    fetchTextbooks: function () {
      return Promise.resolve([
        { id: 7, name: '书七', edition: '人教版PEP', stage: '', grade: '三年级',
          volume: '上册', module: '', lessons: ['第1课', '第2课'] },
        { id: 8, name: '书八', edition: '外研', stage: '', grade: '四年级',
          volume: '下册', module: '', lessons: ['单元A', '单元B'] },
      ]);
    },
    fetchPoems: function () { return Promise.resolve([]); },
    fetchPassages: function () { return Promise.resolve([]); },
    submitWords: function () { return Promise.resolve({ results: [] }); },
    textbookDeps: {
      fetchTextbooks: function () {
        return Promise.resolve([
          { id: 7, name: '书七', edition: '人教版PEP', stage: '', grade: '三年级',
            volume: '上册', module: '', lessons: ['第1课', '第2课'] },
          { id: 8, name: '书八', edition: '外研', stage: '', grade: '四年级',
            volume: '下册', module: '', lessons: ['单元A', '单元B'] },
        ]);
      },
      createTextbook: function () { return Promise.resolve({ id: 99 }); },
      updateTextbook: function () { return Promise.resolve({}); },
      assignWords: function () { return Promise.resolve({}); },
    },
  });

  form.prepareUpload = function () {
    return Promise.resolve(new Uint8Array([0xff, 0xd8, 1]));
  };

  return {
    dom: dom,
    form: form,
    modalCalls: modalCalls,
    pickPhotos: function (files) {
      form.fileInput.files = files;
      form.handleFilePicked();
    },
    clickRecognize: function () {
      form.recognizeBtn.fire('click');
    },
    /** The status bar's REAL run buttons (null when not rendered). */
    barStop: function () {
      return form.pdfBar.querySelector('.pdf-stop-btn') || null;
    },
    barResume: function () {
      return form.pdfBar.querySelector('.pdf-resume-btn') || null;
    },
    /** The status bar's REAL retry chip button of one page (null when not rendered). */
    barRetry: function (pageNo) {
      var chip = null;
      form.pdfBar.querySelectorAll('.pdf-page-chip').forEach(function (c) {
        if (c.getAttribute('data-page') === String(pageNo)) {
          chip = c;
        }
      });
      return (chip && chip.querySelector('.pdf-retry-btn')) || null;
    },
    /**
     * Fire the REAL category radio's change (the D-06 subject-switch
     * entry) — returns false when no such radio mounted.
     */
    switchCategory: function (value) {
      var radio = form.categoryKit.field.querySelectorAll('input').filter(function (r) {
        return r.value === value;
      })[0] || null;
      if (!radio) {
        return false;
      }
      radio.fire('change');
      return true;
    },
    /** Click the status bar's REAL retry chip for one failed page. */
    clickRetryPage: function (pageNo) {
      var chip = null;
      form.pdfBar.querySelectorAll('.pdf-page-chip').forEach(function (c) {
        if (c.getAttribute('data-page') === String(pageNo)) {
          chip = c;
        }
      });
      var btn = chip && chip.querySelector('.pdf-retry-btn');
      if (!btn) {
        return false;
      }
      btn.fire('click');
      return true;
    },
  };
}

module.exports = {
  tick: tick,
  flushPromises: flushPromises,
  fakePhoto: fakePhoto,
  fakePdf: fakePdf,
  mountWorkflowForm: mountWorkflowForm,
};
