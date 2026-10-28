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
  await flushPromises(20);
  eq(h.form.rows.length, 2, 'multi photo run lands one row per photo');
  assert(h.form.rows.every(function (r) { return r.word === 'alpha'; }),
    'multi photo run merges each photo content into rows');
  assert(h.form.photoBatch.plan.pages.every(function (p) { return p.status === 'done'; }),
    'multi photo run marks every photo done');
  eq(h.form.discardBtn.hidden, false, 'finishRun surfaces discard after the batch');
}

async function testPdfEntryClearsViaRecognizeButton() {
  var h = mountWorkflowForm();
  seedStaleDraft(h.form);
  h.pickPhotos([fakePdf()]);
  var prev = h.form.recognizeEpoch;
  h.clickRecognize();
  assertRecognizeRunStarted(h.form, prev, 'PDF 开始识别');
  await flushPromises(20);
  eq(h.form.rows.length, 1, 'PDF run lands the page rows');
  assert(h.form.rows.length === 1 && h.form.rows[0].word === 'alpha',
    'PDF run classifies the page content');
  assert(h.form.pdfState.plan.pages.every(function (p) { return p.status === 'done'; }),
    'PDF run marks the page done');
  eq(h.form.discardBtn.hidden, false, 'finishRun surfaces discard after the PDF run');
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

async function testSinglePhotoRecognizeRegistersIdentity() {
  var h = mountWorkflowForm({
    recognizePhoto: function () {
      return Promise.resolve({
        entries: [{ word: 'alpha', meaning: '甲', lesson: '第3课' }],
        textbook: { edition: '人教版PEP', grade: '三年级', volume: '上册', lesson: '第3课' },
      });
    },
  });
  h.pickPhotos([fakePhoto()]);
  h.clickRecognize();
  await flushPromises(12);
  var rec = global.getPhotoRecognition(h.form.importPhotoState, 1);
  assert(!!rec, 'single photo 开始识别 registers page-1 snapshot');
  eq(rec.entries[0].lesson, '第3课', 'single photo keeps OCR lesson in store');
  eq(rec.meta.grade, '三年级', 'single photo keeps header meta in store');
}

async function testMultiPhotoBatchRegistersPerPhotoIdentity() {
  var calls = 0;
  var h = mountWorkflowForm({
    recognizePhoto: function () {
      calls += 1;
      if (calls === 1) {
        return Promise.resolve({
          entries: [{ word: 'first', meaning: '壹', lesson: '第1课' }],
          textbook: { edition: 'A版', grade: '三年级', volume: '上册' },
        });
      }
      return Promise.resolve({
        entries: [{ word: 'second', meaning: '贰', lesson: '第4课' }],
        textbook: { edition: 'B版', grade: '四年级', volume: '下册' },
      });
    },
  });
  h.pickPhotos([fakePhoto('one.jpg'), fakePhoto('two.jpg')]);
  h.clickRecognize();
  await flushPromises(20);
  var form = h.form;
  eq(global.getPhotoRecognition(form.importPhotoState, 1).entries[0].lesson,
    '第1课', 'photo 1 OCR lesson stored by identity');
  eq(global.getPhotoRecognition(form.importPhotoState, 2).entries[0].lesson,
    '第4课', 'photo 2 OCR lesson stored by identity');
  eq(global.getPhotoRecognition(form.importPhotoState, 1).meta.edition,
    'A版', 'photo 1 header meta isolated from photo 2');
  eq(global.getPhotoRecognition(form.importPhotoState, 2).meta.edition,
    'B版', 'photo 2 header meta isolated from photo 1');
}

async function testRetryReplacesPhotoIdentity() {
  var calls = 0;
  var h = mountWorkflowForm({
    recognizePhoto: function () {
      calls += 1;
      if (calls === 1) {
        return Promise.resolve({
          entries: [{ word: 'keep', meaning: '留', lesson: '第1课' }],
          textbook: { edition: 'A', grade: '三', volume: '上' },
        });
      }
      if (calls === 2) {
        return Promise.reject(new Error('forced fail'));
      }
      return Promise.resolve({
        entries: [{ word: 'fresh', meaning: '新', lesson: '第9课' }],
        textbook: { edition: 'C', grade: '五', volume: '上' },
      });
    },
  });
  h.pickPhotos([fakePhoto('a.jpg'), fakePhoto('b.jpg')]);
  h.clickRecognize();
  await flushPromises(20);
  var form = h.form;
  eq(global.getPhotoRecognition(form.importPhotoState, 1).entries[0].word,
    'keep', 'photo 1 lands before retry');
  assert(!global.getPhotoRecognition(form.importPhotoState, 2),
    'failed photo 2 has no snapshot until a successful landing');
  form.handlePdfRetryPage(2);
  await flushPromises(12);
  eq(global.getPhotoRecognition(form.importPhotoState, 1).entries[0].word,
    'keep', 'retry leaves photo 1 snapshot untouched');
  eq(global.getPhotoRecognition(form.importPhotoState, 2).entries[0].word,
    'fresh', 'retry replaces photo 2 snapshot');
  eq(global.getPhotoRecognition(form.importPhotoState, 2).entries[0].lesson,
    '第9课', 'retry replaces photo 2 OCR lesson');
  eq(global.photoMetaListFromState(form.importPhotoState).length, 2,
    'meta list length stays one slot per photo after retry');
  eq(form.detectedMetaList.length, 2,
    'detectedMetaList has no duplicate header slots after retry');
}

async function testReclassifyKeepsOriginalEntryLink() {
  var h = mountWorkflowForm({
    recognizePhoto: function () {
      return Promise.resolve({
        entries: [{ word: 'beta', meaning: '乙', lesson: '第2课' }],
        textbook: { edition: '译林', grade: '四年级', volume: '上册' },
      });
    },
  });
  h.pickPhotos([fakePhoto()]);
  h.clickRecognize();
  await flushPromises(12);
  var form = h.form;
  assert(form.rows.length > 0, 'rows landed for reclassify test');
  form.rows[0].meaning = '家长改义';
  form.rows[0].lesson = '第8课';
  form.reclassify();
  var orig = global.originalEntryForRow(form.importPhotoState, form.rows[0]);
  assert(!!orig, 'reclassify still links row to photo OCR entry');
  eq(orig.lesson, '第2课', 'reclassify preserves original OCR lesson lookup');
  eq(orig.meaning, '乙', 'reclassify preserves original OCR meaning lookup');
}

(async function main() {
  await testSinglePhotoEntryClearsViaRecognizeButton();
  await testMultiPhotoEntryClearsViaRecognizeButton();
  await testPdfEntryClearsViaRecognizeButton();
  testTextbookChangeRefreshesRecommendations();
  testNewBookInputPersistsDraftOnInputEvent();
  testImportBatchEditButtonOpensModal();
  await testSinglePhotoRecognizeRegistersIdentity();
  await testMultiPhotoBatchRegistersPerPhotoIdentity();
  await testRetryReplacesPhotoIdentity();
  await testReclassifyKeepsOriginalEntryLink();

  if (failures) {
    console.error('test-parent-photo-workflow: ' + failures + ' of ' + checks + ' checks failed');
    process.exit(1);
  }
  console.log('test-parent-photo-workflow: all ' + checks + ' checks passed');
})();
