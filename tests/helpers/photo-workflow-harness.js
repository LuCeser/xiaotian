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

function installWorkflowGlobals(dom) {
  global.window = global;
  global.document = dom.document;
  global.URL.createObjectURL = function () { return 'blob:test'; };
  global.URL.revokeObjectURL = function () {};

  global.__parentFormKit = {
    buildCategoryField: function (_name, onChange) {
      var field = dom.document.createElement('div');
      return {
        field: field,
        selected: function () { return 'english'; },
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
  require(path.resolve(__dirname, '..', '..', 'pkg', 'web', 'static', 'js', 'parent-pdf-core.js'));
  require(path.resolve(__dirname, '..', '..', 'pkg', 'web', 'static', 'js', 'parent-photo.js'));

  global.PdfPageRunner = {
    isAvailable: function () { return true; },
    open: function () {
      return Promise.resolve({ doc: { id: 'doc1' }, pageCount: 1 });
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
 */
function mountWorkflowForm(options) {
  options = options || {};
  var dom = createPhotoDom();
  installWorkflowGlobals(dom);

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

  form.runPages = function () {
    return Promise.resolve({ pending: 0, running: 0, done: 1, failed: 0 });
  };

  form.finishRun = function () {
    this.discardBtn.hidden = false;
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
  };
}

module.exports = {
  tick: tick,
  flushPromises: flushPromises,
  fakePhoto: fakePhoto,
  fakePdf: fakePdf,
  mountWorkflowForm: mountWorkflowForm,
};
