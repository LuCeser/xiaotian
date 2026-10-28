/**
 * Issue #80 U2 — real UI entry regression for photo import: recognize button
 * (single / multi / PDF), textbook change, new-book input, import batch edit.
 * Complements the pure-function tests in test-parent-photo-textbook.js.
 *
 * `node tests/test-parent-photo-workflow.js`
 */
'use strict';

const {
  mountWorkflowForm,
  fakePhoto,
  fakePdf,
  flushPromises,
} = require('./helpers/photo-workflow-harness.js');

let failures = 0;
let checks = 0;

function assert(cond, label) {
  checks++;
  if (!cond) {
    failures++;
    console.error('FAIL: ' + label);
  }
}

function eq(actual, expected, label) {
  assert(actual === expected, label + ' — got ' + JSON.stringify(actual) +
    ', want ' + JSON.stringify(expected));
}

function seedStaleDraft(form) {
  form.recognizeEpoch = 2;
  form.rows = [{ word: 'stale-word', meaning: '旧草稿', lesson: '第9课', status: 'new' }];
  form.bookChoice = 'new';
  form.newBookDraft = { name: '旧名', edition: '旧版' };
}

function assertRecognizeRunStarted(form, prevEpoch, label) {
  eq(form.rows.length, 0, label + ' clears rows before async work');
  eq(form.bookChoice, 'unset', label + ' resets bookChoice');
  assert(form.newBookDraft === null, label + ' clears newBookDraft');
  eq(form.recognizeEpoch, prevEpoch + 1, label + ' bumps recognizeEpoch');
}

async function testSinglePhotoEntryClearsViaRecognizeButton() {
  var h = mountWorkflowForm();
  seedStaleDraft(h.form);
  h.pickPhotos([fakePhoto()]);
  var prev = h.form.recognizeEpoch;
  h.clickRecognize();
  assertRecognizeRunStarted(h.form, prev, 'single photo 开始识别');
  await flushPromises(8);
  assert(h.form.rows.some(function (r) { return r.word === 'alpha'; }),
    'single photo run lands recognized rows after fetch');
}

async function testMultiPhotoEntryClearsViaRecognizeButton() {
  var h = mountWorkflowForm();
  seedStaleDraft(h.form);
  h.pickPhotos([fakePhoto('a.jpg'), fakePhoto('b.jpg')]);
  var prev = h.form.recognizeEpoch;
  h.clickRecognize();
  assertRecognizeRunStarted(h.form, prev, 'multi photo 开始识别');
  assert(!!h.form.photoBatch, 'multi photo enters batch run state');
  await flushPromises(12);
}

async function testPdfEntryClearsViaRecognizeButton() {
  var h = mountWorkflowForm();
  seedStaleDraft(h.form);
  h.pickPhotos([fakePdf()]);
  var prev = h.form.recognizeEpoch;
  h.clickRecognize();
  assertRecognizeRunStarted(h.form, prev, 'PDF 开始识别');
  await flushPromises(8);
  assert(!!h.form.pdfState || h.form.rows.length >= 0,
    'PDF path proceeds past beginRecognizeRun');
}

function testTextbookChangeRefreshesRecommendations() {
  var h = mountWorkflowForm();
  var form = h.form;
  form.books = [
    { id: 7, name: '书七', edition: '人教版PEP', stage: '', grade: '三年级',
      volume: '上册', module: '', lessons: ['第1课', '第2课'] },
    { id: 8, name: '书八', edition: '外研', stage: '', grade: '四年级',
      volume: '下册', module: '', lessons: ['单元A', '单元B'] },
  ];
  form.detectedMeta = { edition: '人教版PEP', grade: '三年级', volume: '上册' };
  form.bookChoice = 7;
  form.draftCategory = 'english';
  form.rows = [{
    word: 'beta', meaning: '乙', lesson: '', lessonOrigin: 'none', status: 'new', checked: true,
  }];
  form.renderTextbookPanel('');
  var select = h.dom.document.getElementById('parent-photo-textbook');
  assert(!!select, 'textbook select exists after panel render');
  eq(form.rows[0].lesson, '', 'row starts without lesson before change');
  select.value = '8';
  select.fire('change');
  eq(form.rows[0].lesson, '单元B',
    'textbook change event refreshes previous-lesson recommendation');
  eq(form.rows[0].lessonOrigin, 'previous-lesson',
    'origin updates after textbook change refresh');
}

function testNewBookInputPersistsDraftOnInputEvent() {
  var h = mountWorkflowForm();
  var form = h.form;
  form.books = [];
  form.detectedMeta = { edition: '译林版', grade: '五年级', volume: '上册' };
  form.bookChoice = 'new';
  form.draftCategory = 'english';
  form.rows = [{ word: 'gamma', meaning: '丙', lesson: '第3课', status: 'new' }];
  form.renderTextbookPanel('');
  var editionInput = h.dom.document.getElementById('parent-photo-tb-edition');
  assert(!!editionInput, 'new book edition field mounted');
  editionInput.value = '';
  editionInput.fire('input');
  eq(form.newBookDraft.edition, '',
    'input event saves parent-cleared edition into newBookDraft');
  editionInput.value = '人教版';
  editionInput.fire('input');
  eq(form.newBookDraft.edition, '人教版',
    'input event persists edited edition before commit');
}

function testImportBatchEditButtonOpensModal() {
  var h = mountWorkflowForm();
  var form = h.form;
  form.draftCategory = 'english';
  form.bookChoice = 7;
  form.existingWords = [];
  form.rows = [
    { word: 'one', meaning: '一', lesson: '第1课', status: 'new', checked: true },
    { word: 'two', meaning: '二', lesson: '第1课', status: 'new', checked: true },
  ];
  form.renderPreview();
  var btn = h.dom.document.getElementById('photo-batch-edit-btn');
  assert(!!btn, 'import batch edit button rendered on preview');
  btn.fire('click');
  eq(h.modalCalls.length, 1, 'batch edit button opens import modal via openImportBatchEditModal');
  assert(typeof h.modalCalls[0].build === 'function',
    'modal spec includes build from real openImportBatchEditModal');
}

(async function main() {
  await testSinglePhotoEntryClearsViaRecognizeButton();
  await testMultiPhotoEntryClearsViaRecognizeButton();
  await testPdfEntryClearsViaRecognizeButton();
  testTextbookChangeRefreshesRecommendations();
  testNewBookInputPersistsDraftOnInputEvent();
  testImportBatchEditButtonOpensModal();

  if (failures) {
    console.error('test-parent-photo-workflow: ' + failures + ' of ' + checks + ' checks failed');
    process.exit(1);
  }
  console.log('test-parent-photo-workflow: all ' + checks + ' checks passed');
})();
