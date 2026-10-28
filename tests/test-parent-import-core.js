/**
 * Issue #80 U3 — pure helpers for per-photo recognition identity.
 * `node tests/test-parent-import-core.js`
 */
'use strict';

const path = require('node:path');

global.window = global;
require(path.resolve(__dirname, '..', 'pkg', 'web', 'static', 'js', 'parent-import-core.js'));
// Issue #80 U6: the batch run machine drives the SHARED page plan, so the
// page-level transitions (applyPageEvent) come from parent-pdf-core.
require(path.resolve(__dirname, '..', 'pkg', 'web', 'static', 'js', 'parent-pdf-core.js'));

const core = global;

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

(function testReplaceNotAppend() {
  var state = core.createImportPhotoState();
  core.setPhotoRecognition(state, 2, {
    meta: { edition: 'A' },
    entries: [{ word: 'x', meaning: '甲', lesson: '第1课' }],
  });
  core.setPhotoRecognition(state, 2, {
    meta: { edition: 'B' },
    entries: [{ word: 'y', meaning: '乙', lesson: '第9课' }],
  });
  var rec = core.getPhotoRecognition(state, 2);
  eq(rec.entries[0].word, 'y', 'retry replaces entries');
  eq(rec.meta.edition, 'B', 'retry replaces meta');
  eq(core.photoMetaListFromState(state).length, 2, 'meta list spans page index');
})();

(function testOriginalAfterEdit() {
  var state = core.createImportPhotoState();
  core.setPhotoRecognition(state, 1, {
    meta: { grade: '三' },
    entries: [{ word: 'cat', meaning: '猫', lesson: '第2课' }],
  });
  var row = { word: 'cat', meaning: '改义', lesson: '家长课', page: 1 };
  var orig = core.originalEntryForRow(state, row);
  eq(orig.lesson, '第2课', 'original lesson survives row edits');
  eq(orig.meaning, '猫', 'original meaning from OCR');
})();

(function testStoredEntriesCarryOrdinal() {
  var state = core.createImportPhotoState();
  core.setPhotoRecognition(state, 1, {
    meta: { grade: '三' },
    entries: [
      { word: 'bat', meaning: '甲', lesson: '第1课' },
      { word: 'bat', meaning: '乙', lesson: '第2课' },
    ],
  });
  var rec = core.getPhotoRecognition(state, 1);
  eq(rec.entries[0].entryIndex, 0, 'stored entry 0 carries its ordinal');
  eq(rec.entries[1].entryIndex, 1, 'stored entry 1 carries its ordinal');
  eq(core.recognitionEntriesForPage(state, 1)[1].entryIndex, 1,
    'entries export keeps the ordinal');
})();

(function testOrdinalLinksOwnEntryAfterWordEdit() {
  var state = core.createImportPhotoState();
  core.setPhotoRecognition(state, 1, {
    meta: { grade: '三' },
    entries: [
      { word: 'bat', meaning: '甲', lesson: '第1课' },
      { word: 'bat', meaning: '乙', lesson: '第2课' },
    ],
  });
  // The parent corrected both word faces — the current word matches no
  // stored entry; the ordinal must still find each row's OWN entry.
  var row0 = { word: 'cat', meaning: '甲', lesson: '第1课', page: 1, entryIndex: 0 };
  var row1 = { word: 'hat', meaning: '乙', lesson: '第2课', page: 1, entryIndex: 1 };
  eq(core.originalEntryForRow(state, row0).word, 'bat',
    'corrected word still links to its original OCR entry');
  eq(core.originalEntryForRow(state, row1).lesson, '第2课',
    'same-page duplicate word links to its OWN entry, not the first match');
})();

(function testUnstampedRowFallsBackToWordMatch() {
  var state = core.createImportPhotoState();
  core.setPhotoRecognition(state, 1, {
    meta: { grade: '三' },
    entries: [{ word: 'cat', meaning: '猫', lesson: '第2课' }],
  });
  var row = { word: 'cat', meaning: '猫', lesson: '', page: 1 };
  eq(core.originalEntryForRow(state, row).lesson, '第2课',
    'rows without the ordinal keep the word-match fallback');
  var gone = { word: 'cat', meaning: '猫', lesson: '', page: 1, entryIndex: 9 };
  assert(core.originalEntryForRow(state, gone) === null,
    'a stale out-of-range ordinal returns null, not a wrong entry');
})();

// ===== Issue #80 U6 — the batch run state machine (§3.2 transitions) =====
// The batch facts (aborted / executing / invalidated) live beside the page
// plan; page statuses move through parent-pdf-core's real page machine.

function runStatuses(run) {
  return run.plan.pages.map(function (p) { return p.status; }).join(',');
}

(function testBatchTransitions() {
  var run = core.createImportRunState(core.createPagePlan(3));
  eq(runStatuses(run), 'pending,pending,pending', 'fresh run starts all pending');
  eq(run.aborted, false, 'fresh run is not stopped');
  eq(run.executing, false, 'fresh run has no real execution');

  core.beginImportRun(run); // 开始
  eq(run.executing, true, '开始 marks real execution');

  run.plan = core.applyPageEvent(run.plan, 1, 'start');
  run.plan = core.applyPageEvent(run.plan, 1, 'done'); // 成功
  run.plan = core.applyPageEvent(run.plan, 2, 'start');
  run.plan = core.applyPageEvent(run.plan, 2, 'fail', '超时'); // 失败
  run.plan = core.applyPageEvent(run.plan, 3, 'start'); // 第 3 张识别中

  core.stopImportRun(run); // 停止
  eq(run.aborted, true, '停止 sets the batch stop flag');
  eq(run.executing, true, '停止 keeps the wind-down as real execution');
  eq(runStatuses(run), 'done,failed,running',
    'the in-flight page stays running until the wind-down settles');

  core.settleImportRun(run); // 收尾
  eq(run.executing, false, '收尾 ends the execution');
  eq(runStatuses(run), 'done,failed,pending',
    '停止后当前 running 项恢复 pending（迟到处理未触发也由收尾保证）');

  eq(JSON.stringify(core.pendingImportPages(run)), '[3]',
    '剩余工作只有 pending——done/failed 都不在继续范围');
  core.resumeImportRun(run); // 继续
  eq(runStatuses(run), 'done,failed,pending', '继续不重跑 done，也不动 failed');
  eq(run.aborted, false, '继续 clears the stop flag');
  eq(run.executing, true, '继续 restarts real execution');
})();

(function testNoOverlappingExecution() {
  var run = core.createImportRunState(core.createPagePlan(2));
  core.beginImportRun(run);
  run.plan = core.applyPageEvent(run.plan, 1, 'start');
  core.stopImportRun(run); // wind-down in flight
  core.resumeImportRun(run); // must be a no-op while executing
  eq(run.aborted, true, 'wind-down 期间继续不得清除停止标记');
  eq(run.executing, true, 'wind-down 期间不得启动第二次执行');
  core.retryImportPage(run, 1); // also a no-op while busy
  eq(runStatuses(run), 'running,pending', 'busy 期间重试不得改动任何页');
})();

(function testRetryTouchesOnlyTargetPage() {
  var run = core.createImportRunState(core.createPagePlan(3));
  core.beginImportRun(run);
  run.plan = core.applyPageEvent(run.plan, 1, 'start');
  run.plan = core.applyPageEvent(run.plan, 1, 'done');
  run.plan = core.applyPageEvent(run.plan, 2, 'start');
  run.plan = core.applyPageEvent(run.plan, 2, 'fail', '超时');
  run.plan = core.applyPageEvent(run.plan, 3, 'start');
  run.plan = core.applyPageEvent(run.plan, 3, 'fail', '空识别');
  core.settleImportRun(run);
  core.retryImportPage(run, 3); // 重试
  eq(runStatuses(run), 'done,failed,pending', '失败重试只处理目标张');
  eq(run.executing, true, '重试也是一次真实执行');
  run.plan = core.applyPageEvent(run.plan, 3, 'start');
  run.plan = core.applyPageEvent(run.plan, 3, 'done');
  core.settleImportRun(run);
  eq(runStatuses(run), 'done,failed,done', '重试完成后其余 failed 保持原状');
})();

(function testInvalidateRun() {
  var run = core.createImportRunState(core.createPagePlan(2));
  core.beginImportRun(run);
  run.plan = core.applyPageEvent(run.plan, 1, 'start');
  core.invalidateImportRun(run); // 失效
  eq(run.invalidated, true, '失效 marks the run dead');
  eq(run.executing, false, '失效 ends the execution');
  eq(runStatuses(run), 'pending,pending', '失效把识别中的页恢复为 pending');
  core.resumeImportRun(run); // a dead run never resumes
  eq(run.executing, false, '失效后的批次不可继续');
  core.retryImportPage(run, 1); // nor retry
  eq(runStatuses(run), 'pending,pending', '失效后的批次不可重试');
})();

if (failures) {
  console.error('test-parent-import-core: ' + failures + ' of ' + checks + ' checks failed');
  process.exit(1);
}
console.log('test-parent-import-core: all ' + checks + ' checks passed');
