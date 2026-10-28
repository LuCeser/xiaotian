/**
 * Issue #80 U2 — real UI entry regression for photo import: recognize button
 * (single / multi / PDF), textbook change, new-book input, import batch edit.
 * Issue #80 U3 — per-photo recognition identity: snapshot registration, the
 * retry chip REPLACING an existing snapshot, and word-edit → reclassify
 * keeping each row linked to its own OCR entry. Complements the
 * pure-function tests in test-parent-photo-textbook.js.
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

/**
 * Issue #80 U3 review P2-1: correcting a WORD FACE through the real word
 * input (→ onRowEdited → reclassify) must not orphan the row from its OCR
 * entry, and same-page duplicate words must link to their OWN entries —
 * identity is the photo-local ordinal, never the current word.
 */
async function testWordEditKeepsOriginalEntryLink() {
  var h = mountWorkflowForm({
    recognizePhoto: function () {
      return Promise.resolve({
        entries: [
          { word: 'bat', meaning: '甲', lesson: '第1课' },
          { word: 'bat', meaning: '乙', lesson: '第2课' },
        ],
        textbook: { edition: '人教版PEP', grade: '三年级', volume: '上册' },
      });
    },
  });
  h.pickPhotos([fakePhoto()]);
  h.clickRecognize();
  await flushPromises(12);
  var form = h.form;
  var inputs = form.preview.querySelectorAll('.photo-word-input');
  eq(inputs.length, 2, 'one real word input per landed row');
  inputs[0].value = 'cat';
  inputs[0].fire('input');
  inputs[1].value = 'hat';
  inputs[1].fire('input');
  eq(form.rows[0].word, 'cat', 'word edit 0 landed on the row model');
  eq(form.rows[1].word, 'hat', 'word edit 1 landed on the row model');
  eq(form.rows[1].status, 'new', 'reclassify reran after the edits');
  var orig0 = global.originalEntryForRow(form.importPhotoState, form.rows[0]);
  assert(!!orig0, 'corrected word still links to its original OCR entry');
  if (orig0) {
    eq(orig0.word, 'bat', 'original entry keeps the OCR word face');
  }
  var orig1 = global.originalEntryForRow(form.importPhotoState, form.rows[1]);
  assert(!!orig1, 'duplicate word still links to its original OCR entry');
  if (orig1) {
    eq(orig1.lesson, '第2课', 'duplicate word links to its OWN entry, not the first match');
  }
}

/** Issue #80 U3 review P2-2: the real PDF entry registers page snapshots. */
async function testPdfEntryRegistersPageSnapshots() {
  var calls = 0;
  var h = mountWorkflowForm({
    pdfPageCount: 2,
    recognizePhoto: function () {
      calls += 1;
      return Promise.resolve(calls === 1 ? {
        entries: [{ word: 'p1', meaning: '壹', lesson: '第1课' }],
        textbook: { edition: 'E1', grade: '三年级', volume: '上册' },
      } : {
        entries: [{ word: 'p2', meaning: '贰', lesson: '第5课' }],
        textbook: { edition: 'E2', grade: '四年级', volume: '下册' },
      });
    },
  });
  h.pickPhotos([fakePdf()]);
  h.clickRecognize();
  await flushPromises(30);
  var form = h.form;
  var rec1 = global.getPhotoRecognition(form.importPhotoState, 1);
  var rec2 = global.getPhotoRecognition(form.importPhotoState, 2);
  assert(!!rec1, 'PDF page 1 registers its snapshot');
  assert(!!rec2, 'PDF page 2 registers its snapshot');
  if (rec1) {
    eq(rec1.entries[0].word, 'p1', 'PDF page 1 snapshot keeps its OCR entry');
    eq(rec1.meta.edition, 'E1', 'PDF page 1 header meta isolated from page 2');
  }
  if (rec2) {
    eq(rec2.entries[0].word, 'p2', 'PDF page 2 snapshot keeps its OCR entry');
    eq(rec2.meta.edition, 'E2', 'PDF page 2 header meta isolated from page 1');
  }
  eq(global.photoMetaListFromState(form.importPhotoState).length, 2,
    'PDF meta list keeps one slot per page');
  eq(form.detectedMeta.edition, 'E1', 'PDF whole-file header still first-page-wins');
  assert(form.pdfState.plan.pages.every(function (p) { return p.status === 'done'; }),
    'PDF run marks both pages done');
}

/**
 * Issue #80 U3 review P2-2: a PDF page that recognized nothing fails
 * honestly (empty snapshot registered), and retrying it from the REAL
 * status-bar chip REPLACES that existing snapshot — no append, no skip.
 */
async function testPdfRetryReplacesRegisteredSnapshot() {
  var calls = 0;
  var h = mountWorkflowForm({
    pdfPageCount: 2,
    recognizePhoto: function () {
      calls += 1;
      if (calls === 1) {
        return Promise.resolve({
          entries: [{ word: 'a1', meaning: '壹', lesson: '第1课' }],
          textbook: { edition: 'EA', grade: '三年级', volume: '上册' },
        });
      }
      if (calls === 2) {
        return Promise.resolve({
          entries: [],
          textbook: { edition: 'E2', grade: '四年级', volume: '下册' },
        });
      }
      return Promise.resolve({
        entries: [{ word: 'b9', meaning: '玖', lesson: '第9课' }],
        textbook: { edition: 'EB', grade: '五年级', volume: '上册' },
      });
    },
  });
  h.pickPhotos([fakePdf()]);
  h.clickRecognize();
  await flushPromises(30);
  var form = h.form;
  eq(form.pdfState.plan.pages[1].status, 'failed',
    'empty PDF page fails honestly instead of a silent done');
  var empty = global.getPhotoRecognition(form.importPhotoState, 2);
  assert(!!empty, 'empty page still registers its (empty) identity slot');
  if (empty) {
    eq(empty.entries.length, 0, 'empty page snapshot carries no entries');
    eq(empty.meta.edition, 'E2', 'empty page snapshot keeps its header meta');
  }
  assert(h.clickRetryPage(2), 'status bar renders the real retry chip for page 2');
  await flushPromises(16);
  var replaced = global.getPhotoRecognition(form.importPhotoState, 2);
  assert(!!replaced, 'page 2 snapshot survives the retry');
  if (replaced) {
    eq(replaced.entries[0] ? replaced.entries[0].word : '',
      'b9', 'retry REPLACES the existing page-2 snapshot with fresh content');
    eq(replaced.entries[0] ? replaced.entries[0].lesson : '',
      '第9课', 'retry replaces the OCR lesson too');
    eq(replaced.meta.edition, 'EB', 'retry replaces the header meta too');
  }
  var kept1 = global.getPhotoRecognition(form.importPhotoState, 1);
  assert(!!kept1, 'page 1 snapshot still present after the retry');
  if (kept1) {
    eq(kept1.entries[0].word, 'a1', 'retry leaves page 1 snapshot untouched');
  }
  eq(global.photoMetaListFromState(form.importPhotoState).length, 2,
    'meta list never grows a duplicate slot for the retried page');
  eq(form.detectedMetaList.length, 2, 'detectedMetaList has no appended slot after retry');
  eq(form.pdfState.plan.pages[1].status, 'done', 'retried page settles done');
  assert(form.rows.some(function (r) { return r.word === 'b9'; }),
    'retried page rows land in the preview');
}

/**
 * Issue #80 U3 review fix2: a run where EVERY page recognized nothing is
 * RETRYABLE — the all-empty finish must NOT wipe what the run registered.
 * The kept header stays consistent with metaDone, so a successful retry
 * preselects the SAME textbook the same result preselects on first try
 * (the control mount below proves that first-try expectation).
 */
async function testPdfAllEmptyRunKeepsMetaForRetryPreselect() {
  var metaA = { edition: '人教版PEP', grade: '三年级', volume: '上册' };
  var valid = {
    entries: [{ word: 'late', meaning: '迟', lesson: '第1课' }],
    textbook: metaA,
  };
  // Control: the SAME valid result on first try preselects book 7.
  var control = mountWorkflowForm({
    recognizePhoto: function () { return Promise.resolve(valid); },
  });
  control.pickPhotos([fakePdf()]);
  control.clickRecognize();
  await flushPromises(24);
  eq(control.form.bookChoice, 7,
    'control: the same result on first try preselects the matching book');

  var calls = 0;
  var h = mountWorkflowForm({
    recognizePhoto: function () {
      calls += 1;
      return Promise.resolve(calls === 1 ? { entries: [], textbook: metaA } : valid);
    },
  });
  h.pickPhotos([fakePdf()]);
  h.clickRecognize();
  await flushPromises(24);
  var form = h.form;
  eq(form.pdfState.plan.pages[0].status, 'failed',
    'all-empty single-page PDF fails honestly');
  var empty = global.getPhotoRecognition(form.importPhotoState, 1);
  assert(!!empty, 'all-empty finish KEEPS the registered empty snapshot for retry');
  if (empty) {
    eq(empty.entries.length, 0, 'kept all-empty snapshot carries no entries');
    eq(empty.meta.edition, '人教版PEP', 'kept all-empty snapshot keeps its header meta');
  }
  assert(form.detectedMeta !== null && form.pdfState.metaDone === true,
    'all-empty finish keeps detectedMeta consistent with metaDone');
  assert(h.clickRetryPage(1), 'status bar renders the real retry chip');
  await flushPromises(20);
  var replaced = global.getPhotoRecognition(form.importPhotoState, 1);
  assert(!!replaced && !!replaced.entries[0] && replaced.entries[0].word === 'late',
    'retry REPLACES the kept empty snapshot with fresh content');
  eq(form.pdfState.plan.pages[0].status, 'done', 'retried page settles done');
  eq(form.detectedMeta && form.detectedMeta.edition, '人教版PEP',
    'textbook header survives the all-empty finish and retry');
  eq(form.bookChoice, 7,
    'retry-after-all-empty preselects the same book as the first-try control');
  assert(form.rows.some(function (r) { return r.word === 'late'; }),
    'retried rows land in the preview');
}

/**
 * The photo-batch kin of the all-empty case: EVERY photo empty keeps every
 * registered (empty) snapshot, so retrying one photo REPLACES its own slot
 * instead of first-registering into a wiped store.
 */
async function testPhotoBatchAllEmptyKeepsSnapshotsForRetry() {
  var calls = 0;
  var h = mountWorkflowForm({
    recognizePhoto: function () {
      calls += 1;
      if (calls <= 2) {
        return Promise.resolve({
          entries: [],
          textbook: calls === 1
            ? { edition: 'PA', grade: '三年级', volume: '上册' }
            : { edition: 'PB', grade: '四年级', volume: '下册' },
        });
      }
      return Promise.resolve({
        entries: [{ word: 's2', meaning: '贰', lesson: '第7课' }],
        textbook: { edition: 'PB', grade: '四年级', volume: '下册' },
      });
    },
  });
  h.pickPhotos([fakePhoto('one.jpg'), fakePhoto('two.jpg')]);
  h.clickRecognize();
  await flushPromises(30);
  var form = h.form;
  eq(form.photoBatch.plan.pages[0].status, 'failed', 'photo 1 all-empty fails honestly');
  eq(form.photoBatch.plan.pages[1].status, 'failed', 'photo 2 all-empty fails honestly');
  eq(global.photoMetaListFromState(form.importPhotoState).length, 2,
    'all-empty photo batch keeps one registered slot per photo');
  eq(form.detectedMetaList.length, 2, 'all-empty finish keeps detectedMetaList slots');
  assert(form.detectedMeta !== null && form.photoBatch.metaDone === true,
    'all-empty finish keeps detectedMeta consistent with metaDone');
  assert(h.clickRetryPage(2), 'status bar renders the real retry chip for photo 2');
  await flushPromises(16);
  var kept1 = global.getPhotoRecognition(form.importPhotoState, 1);
  assert(!!kept1, "photo 1's empty snapshot SURVIVES the all-empty finish and the retry");
  if (kept1) {
    eq(kept1.entries.length, 0, 'photo 1 slot still carries its own (empty) record');
    eq(kept1.meta.edition, 'PA', 'photo 1 slot still carries its own header meta');
  }
  var replaced = global.getPhotoRecognition(form.importPhotoState, 2);
  assert(!!replaced && !!replaced.entries[0] && replaced.entries[0].word === 's2',
    'retry REPLACES photo 2 slot — not first-registration into a wiped store');
  eq(global.photoMetaListFromState(form.importPhotoState).length, 2,
    'meta list keeps one slot per photo after the retry');
  eq(form.photoBatch.plan.pages[1].status, 'done', 'retried photo settles done');
  eq(form.photoBatch.plan.pages[0].status, 'failed',
    'photo 1 stays failed — its own retry chip remains');
}

/** The photo-batch kin of the PDF retry-replace case (same real entries). */
async function testPhotoRetryReplacesRegisteredSnapshot() {
  var calls = 0;
  var h = mountWorkflowForm({
    recognizePhoto: function () {
      calls += 1;
      if (calls === 1) {
        return Promise.resolve({
          entries: [{ word: 's1', meaning: '壹', lesson: '第2课' }],
          textbook: { edition: 'PA', grade: '三年级', volume: '上册' },
        });
      }
      if (calls === 2) {
        return Promise.resolve({
          entries: [],
          textbook: { edition: 'PB', grade: '四年级', volume: '下册' },
        });
      }
      return Promise.resolve({
        entries: [{ word: 's2', meaning: '贰', lesson: '第7课' }],
        textbook: { edition: 'PC', grade: '五年级', volume: '上册' },
      });
    },
  });
  h.pickPhotos([fakePhoto('one.jpg'), fakePhoto('two.jpg')]);
  h.clickRecognize();
  await flushPromises(30);
  var form = h.form;
  eq(form.photoBatch.plan.pages[1].status, 'failed',
    'empty photo fails honestly instead of a silent done');
  var empty = global.getPhotoRecognition(form.importPhotoState, 2);
  assert(!!empty, 'empty photo still registers its (empty) identity slot');
  if (empty) {
    eq(empty.entries.length, 0, 'empty photo snapshot carries no entries');
  }
  assert(h.clickRetryPage(2), 'status bar renders the real retry chip for photo 2');
  await flushPromises(16);
  var replaced = global.getPhotoRecognition(form.importPhotoState, 2);
  assert(!!replaced, 'photo 2 snapshot survives the retry');
  if (replaced) {
    eq(replaced.entries[0] ? replaced.entries[0].word : '',
      's2', 'retry REPLACES the existing photo-2 snapshot with fresh content');
    eq(replaced.meta.edition, 'PC', 'retry replaces the header meta too');
  }
  var kept1 = global.getPhotoRecognition(form.importPhotoState, 1);
  assert(!!kept1, 'photo 1 snapshot still present after the retry');
  if (kept1) {
    eq(kept1.entries[0].word, 's1', 'retry leaves photo 1 snapshot untouched');
  }
  eq(global.photoMetaListFromState(form.importPhotoState).length, 2,
    'meta list never grows a duplicate slot for the retried photo');
  eq(form.detectedMetaList.length, 2, 'detectedMetaList has no appended slot after retry');
  eq(form.photoBatch.plan.pages[1].status, 'done', 'retried photo settles done');
}

/**
 * Issue #80 U4 — the book switch re-derives recommendations for EVERY
 * choice (A→B→新建→空教材→A) while the three lesson identities keep their
 * own facts: the photo's OCR lesson and the parent's value (an explicit
 * clear included) survive; only recommendation-derived rows recompute, so
 * an old recommendation can never freeze into a "recognized" fact.
 */
async function testBookSwitchRecomputesEveryChoice() {
  var h = mountWorkflowForm({
    recognizePhoto: function () {
      return Promise.resolve({
        entries: [
          { word: 'r1', meaning: '一', lesson: '' },
          { word: 'r2', meaning: '二', lesson: '' },
          { word: 'r3', meaning: '三', lesson: '' },
          { word: 'r4', meaning: '四', lesson: '' },
        ],
        textbook: { edition: '人教版PEP', grade: '三年级', volume: '上册' },
      });
    },
  });
  h.pickPhotos([fakePhoto()]);
  h.clickRecognize();
  await flushPromises(14);
  var form = h.form;
  eq(form.rows.length, 4, 'U4 切换: four rows land from the real recognize button');
  eq(form.bookChoice, 7, 'U4 切换: matching meta preselects book 7');
  // The parent's own calls ride REAL lesson inputs: one hand-set lesson,
  // one explicit clear (the empty string is the parent's call too).
  var inputs = form.preview.querySelectorAll('.photo-lesson-input');
  eq(inputs.length, 4, 'U4 切换: one real lesson input per landed row');
  inputs[2].value = '家长课';
  inputs[2].fire('input');
  inputs[3].value = '';
  inputs[3].fire('input');
  eq(form.rows[2].lesson, '家长课', 'U4 切换: parent edit lands on the row');
  eq(form.rows[2].lessonOrigin, 'parent', 'U4 切换: parent edit origin');
  eq(form.rows[3].lesson, '', 'U4 切换: parent explicit clear empties the row');
  eq(form.rows[3].lessonOrigin, 'parent',
    'U4 切换: the explicit clear is still the parent\'s call');

  var select = h.dom.document.getElementById('parent-photo-textbook');
  select.value = '8';
  select.fire('change');
  eq(form.rows[0].lesson, '单元B', 'A→B: recommendation rows recompute to book 8 末课');
  eq(form.rows[0].lessonOrigin, 'previous-lesson', 'A→B: origin follows the new source');
  eq(form.rows[1].lesson, '单元B', 'A→B: every recommendation row recomputes');
  eq(form.rows[2].lesson, '家长课', 'A→B: parent value survives the switch');
  eq(form.rows[2].lessonOrigin, 'parent', 'A→B: parent origin survives the switch');
  eq(form.rows[3].lesson, '', 'A→B: parent explicit clear survives the switch');
  eq(form.rows[3].lessonOrigin, 'parent', 'A→B: the clear stays the parent\'s');

  select.value = 'new';
  select.fire('change');
  eq(form.rows[0].lesson, '', 'B→新建: no other book 末课 is borrowed');
  eq(form.rows[0].lessonOrigin, 'none', 'B→新建: honest 待确认 origin');
  eq(form.rows[2].lesson, '家长课', 'B→新建: parent value survives');
  eq(form.rows[3].lesson, '', 'B→新建: explicit clear survives');

  select.value = 'none';
  select.fire('change');
  eq(form.rows[0].lesson, '', '新建→空教材: still no borrowed 末课');
  eq(form.rows[0].lessonOrigin, 'none', '新建→空教材: origin none');
  eq(form.rows[3].lesson, '', '新建→空教材: explicit clear still survives');

  select.value = '7';
  select.fire('change');
  eq(form.rows[0].lesson, '第2课', '空教材→A: switching back recomputes book 7 末课');
  eq(form.rows[0].lessonOrigin, 'previous-lesson', '空教材→A: origin previous-lesson');
  eq(form.rows[2].lesson, '家长课', '空教材→A: parent value survives the whole chain');
}

/**
 * Issue #80 U4 — 前张不借后张: photo 1's header never recognized, so its
 * refresh must derive from ITS OWN header-less snapshot plus the chosen
 * book — never from the merged batch meta whose 课次 belongs to photo 2.
 */
async function testEarlierPhotoNeverBorrowsLaterHeaderLesson() {
  var calls = 0;
  var h = mountWorkflowForm({
    recognizePhoto: function () {
      calls += 1;
      if (calls === 1) {
        // No textbook header on photo 1 — an honest header-less photo.
        return Promise.resolve({
          entries: [{ word: 'p1a', meaning: '壹', lesson: '' }],
        });
      }
      return Promise.resolve({
        entries: [{ word: 'p2a', meaning: '贰', lesson: '' }],
        textbook: { edition: '人教版PEP', grade: '三年级', volume: '上册', lesson: '第4课' },
      });
    },
  });
  h.pickPhotos([fakePhoto('one.jpg'), fakePhoto('two.jpg')]);
  h.clickRecognize();
  await flushPromises(30);
  var form = h.form;
  eq(form.rows.length, 2, 'U4 前张: both photos land their rows');
  eq(form.rows[0].lesson, '', 'U4 前张: header-less photo 1 has no lesson of its own');
  eq(form.rows[1].lesson, '第4课', 'U4 前张: photo 2 keeps its own header lesson');
  eq(form.rows[1].lessonOrigin, 'recognized', 'U4 前张: photo 2 lesson origin recognized');
  eq(form.detectedMeta && form.detectedMeta.lesson, '第4课',
    'U4 前张: the merged batch meta DOES carry photo 2 课次 (the borrowing temptation)');
  eq(String(form.bookChoice), 'unset',
    'U4 前张: header-less first photo leaves the choice unset');
  var select = h.dom.document.getElementById('parent-photo-textbook');
  select.value = '7';
  select.fire('change');
  eq(form.rows[0].lesson, '第2课',
    '前张刷新取所选教材末课（第2课），不借整批 meta 里后张的第4课');
  eq(form.rows[0].lessonOrigin, 'previous-lesson', '前张刷新来源是 previous-lesson');
  eq(form.rows[1].lesson, '第4课', '后张自己的识别课次在刷新后保留');
}

/**
 * Issue #80 U4 — 后张按前序推荐计算: photo ORDER, not completion order.
 * Photo 1 fails its first attempt, so photo 2 lands first; the retried
 * photo 1 lands LAST with its own lesson. The lesson-less photo 3 — and
 * the post-retry book-switch refresh — must keep following photo 2 (photo
 * order), never the late-completed photo 1 (completion order).
 */
async function testPriorRecommendationsFollowPhotoOrderNotCompletionOrder() {
  var calls = 0;
  var h = mountWorkflowForm({
    recognizePhoto: function () {
      calls += 1;
      if (calls === 1) {
        return Promise.resolve({
          entries: [],
          textbook: { edition: '人教版PEP', grade: '三年级', volume: '上册' },
        });
      }
      if (calls === 2) {
        return Promise.resolve({
          entries: [{ word: 'w2', meaning: '贰', lesson: '' }],
          textbook: { edition: '人教版PEP', grade: '三年级', volume: '上册', lesson: '第4课' },
        });
      }
      if (calls === 3) {
        return Promise.resolve({
          entries: [{ word: 'w3', meaning: '叁', lesson: '' }],
          textbook: { edition: '人教版PEP', grade: '三年级', volume: '上册' },
        });
      }
      return Promise.resolve({
        entries: [{ word: 'w1', meaning: '壹', lesson: '第2课' }],
        textbook: { edition: '人教版PEP', grade: '三年级', volume: '上册' },
      });
    },
  });
  h.pickPhotos([fakePhoto('one.jpg'), fakePhoto('two.jpg'), fakePhoto('three.jpg')]);
  h.clickRecognize();
  await flushPromises(40);
  var form = h.form;
  eq(form.photoBatch.plan.pages[0].status, 'failed', 'U4 前序: photo 1 empty attempt fails');
  eq(form.rows.length, 2, 'U4 前序: photos 2 and 3 land');
  eq(form.rows[0].word, 'w2', 'U4 前序: photo 2 rows keep photo order');
  eq(form.rows[0].lesson, '第4课', 'U4 前序: photo 2 own header lesson stamps its row');
  eq(form.rows[1].lesson, '第4课',
    'U4 前序: lesson-less photo 3 follows photo 2 (photo order) at landing');
  eq(form.rows[1].lessonOrigin, 'photo-order', 'U4 前序: photo 3 landing origin photo-order');
  assert(h.clickRetryPage(1), 'U4 前序: photo 1 real retry chip rendered');
  await flushPromises(18);
  eq(form.rows.length, 3, 'U4 前序: retried photo 1 lands its row');
  eq(form.rows[0].word, 'w1', 'U4 前序: retried photo 1 inserts at its PHOTO position');
  eq(form.rows[0].lesson, '第2课', 'U4 前序: retried photo 1 own lesson stamps its row');
  eq(form.rows[0].lessonOrigin, 'recognized', 'U4 前序: retried own lesson origin recognized');
  var select = h.dom.document.getElementById('parent-photo-textbook');
  select.value = '8';
  select.fire('change');
  eq(form.rows[0].lesson, '第2课', 'U4 前序: photo 1 own lesson survives the switch');
  eq(form.rows[1].lesson, '第4课', 'U4 前序: photo 2 own lesson survives the switch');
  eq(form.rows[2].lesson, '第4课',
    '刷新后 photo 3 仍按片序取 photo 2 的第4课，不取完成顺序上更近的补拍 photo 1 第2课');
  eq(form.rows[2].lessonOrigin, 'photo-order', 'U4 前序: photo 3 refreshed origin photo-order');
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
  await testWordEditKeepsOriginalEntryLink();
  await testPdfEntryRegistersPageSnapshots();
  await testPdfRetryReplacesRegisteredSnapshot();
  await testPhotoRetryReplacesRegisteredSnapshot();
  await testPdfAllEmptyRunKeepsMetaForRetryPreselect();
  await testPhotoBatchAllEmptyKeepsSnapshotsForRetry();
  await testBookSwitchRecomputesEveryChoice();
  await testEarlierPhotoNeverBorrowsLaterHeaderLesson();
  await testPriorRecommendationsFollowPhotoOrderNotCompletionOrder();

  if (failures) {
    console.error('test-parent-photo-workflow: ' + failures + ' of ' + checks + ' checks failed');
    process.exit(1);
  }
  console.log('test-parent-photo-workflow: all ' + checks + ' checks passed');
})();
