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
  core.beginImportRun(run); // nor begin — 失效是终态
  eq(run.executing, false, '失效后的批次不可重新开始');
})();

// ===== Issue #80 U6 — 按钮统一规则（§3.2 的表 → 一个函数） =====

(function testDecisionIdleRow() {
  var run = core.createImportRunState(core.createPagePlan(3));
  var d = core.importRunDecision(run);
  eq(d.phase, 'idle', '尚未开始 → idle');
  eq(d.busy, false, 'pending 页数本身不构成忙碌');
  eq(d.remaining, 3, '剩余工作 = pending 数');
  eq(d.saveAllowed, true, '无执行则不因运行状态禁止保存');
  assert(d.actions.start.shown && d.actions.start.enabled, '尚未开始 → 开始识别可用');
  assert(!d.actions.stop.shown, '尚未开始无停止按钮');
  assert(!d.actions.resume.shown, '尚未开始无继续按钮');
  assert(!d.actions.retry.shown, '尚未开始无重试按钮');
})();

(function testDecisionRunStopResumeEndRows() {
  var run = core.createImportRunState(core.createPagePlan(3));
  core.beginImportRun(run);
  run.plan = core.applyPageEvent(run.plan, 1, 'start');
  run.plan = core.applyPageEvent(run.plan, 1, 'done');
  run.plan = core.applyPageEvent(run.plan, 2, 'start');
  run.plan = core.applyPageEvent(run.plan, 2, 'fail', '超时');
  run.plan = core.applyPageEvent(run.plan, 3, 'start');

  var d = core.importRunDecision(run); // 行2 正在运行
  eq(d.phase, 'running', '执行中 → running');
  eq(d.busy, true, 'executing 是唯一的忙碌事实');
  eq(d.saveAllowed, false, '运行中禁止保存');
  eq(d.counts.done, 1, '成功计入 done');
  eq(d.counts.failed, 1, '失败计入 failed');
  assert(d.actions.stop.shown && d.actions.stop.enabled, '运行中 → 停止可用');
  eq(d.actions.stop.label, '停止（保留已完成张）', '停止按钮说明保留已完成内容');
  assert(d.actions.resume.shown && !d.actions.resume.enabled, '运行中 → 继续禁用');
  assert(d.actions.retry.shown && !d.actions.retry.enabled, '运行中 → 重试禁用');
  eq(JSON.stringify(d.failedPages), '[2]', '失败张列表供逐张重试渲染');

  core.stopImportRun(run);
  d = core.importRunDecision(run); // 行3 已停止、旧执行尚未收尾
  eq(d.phase, 'stopping', '停止后收尾前 → stopping');
  eq(d.busy, true, '收尾仍是真实执行');
  eq(d.saveAllowed, false, '收尾期间禁止保存');
  assert(d.actions.stop.shown && !d.actions.stop.enabled, '停止中 → 停止按钮禁用');
  eq(d.actions.stop.label, '停止中…', '停止中按钮文案');
  assert(d.actions.resume.shown && !d.actions.resume.enabled, '停止中 → 继续暂禁');
  assert(d.actions.retry.shown && !d.actions.retry.enabled, '停止中 → 重试暂禁');

  core.settleImportRun(run);
  d = core.importRunDecision(run); // 行4 已停止、已收尾、仍有 pending
  eq(d.phase, 'stopped', '收尾完成且仍有剩余 → stopped');
  eq(d.busy, false, '收尾完成后不再忙碌');
  eq(d.remaining, 1, '剩余 1 张');
  eq(d.saveAllowed, true, '已停止可保存已完成内容');
  assert(!d.actions.start.shown, '已有进度的批次不再显示开始识别');
  assert(d.actions.resume.shown && d.actions.resume.enabled, '已收尾 → 继续处理可用');
  eq(d.actions.resume.label, '继续处理（剩 1 张）', '继续按钮带剩余数量');
  assert(d.actions.retry.shown && d.actions.retry.enabled, '已停止 → 失败张可单独重试');

  core.resumeImportRun(run);
  run.plan = core.applyPageEvent(run.plan, 3, 'start');
  run.plan = core.applyPageEvent(run.plan, 3, 'done');
  core.settleImportRun(run);
  d = core.importRunDecision(run); // 行6 已结束、含 failed
  eq(d.phase, 'failed', '结束且含失败 → failed');
  assert(!d.actions.resume.shown, '无剩余不显示继续');
  assert(d.actions.retry.shown && d.actions.retry.enabled, '失败结束 → 逐张重试可用');
  eq(d.saveAllowed, true, '失败结束允许保存成功部分');

  core.retryImportPage(run, 2);
  run.plan = core.applyPageEvent(run.plan, 2, 'start');
  run.plan = core.applyPageEvent(run.plan, 2, 'done');
  core.settleImportRun(run);
  d = core.importRunDecision(run); // 行5 全部 done
  eq(d.phase, 'done', '全部完成 → done');
  assert(!d.actions.start.shown && !d.actions.stop.shown &&
    !d.actions.resume.shown && !d.actions.retry.shown, '全部完成 → 无运行按钮');
})();

(function testDecisionSettledWithoutAbortStillContinues() {
  var run = core.createImportRunState(core.createPagePlan(3));
  core.beginImportRun(run);
  run.plan = core.applyPageEvent(run.plan, 1, 'start');
  run.plan = core.applyPageEvent(run.plan, 1, 'done');
  run.plan = core.applyPageEvent(run.plan, 2, 'start');
  run.plan = core.applyPageEvent(run.plan, 2, 'fail', '超时');
  core.settleImportRun(run); // 未按停止、自然收尾但仍有 pending（目标重试后的形态）
  var d = core.importRunDecision(run);
  eq(d.phase, 'stopped', '已有进度且仍有剩余 → 继续处理形态');
  assert(!d.actions.start.shown && d.actions.resume.shown && d.actions.resume.enabled,
    '不再显示开始识别，继续处理可用');
})();

(function testDecisionStoppedBeforeFirstPageLands() {
  // Codex P2-1: begin → 首张 running → 停止 → 收尾。清扫后全部页回到
  // pending，页状态看起来像「从未开始」——idle/stopped 只能由 aborted
  // 事实区分（§3.2：已停止、执行已收尾、仍有 pending → 继续处理）。
  var run = core.createImportRunState(core.createPagePlan(2));
  core.beginImportRun(run);
  run.plan = core.applyPageEvent(run.plan, 1, 'start');
  core.stopImportRun(run);
  core.settleImportRun(run);
  eq(runStatuses(run), 'pending,pending', '首张完成前停止：收尾后全部页恢复 pending');
  var d = core.importRunDecision(run);
  eq(d.phase, 'stopped', '已停止、已收尾、仍有 pending → stopped（读 aborted 事实）');
  assert(!d.actions.start.shown, '停止过的批次不显示开始识别');
  assert(!d.actions.start.enabled, '停止过的批次开始识别不可用');
  assert(d.actions.resume.shown, '已停止且有剩余 → 显示继续处理');
  assert(d.actions.resume.enabled, '已停止且已收尾 → 继续处理可用');
  eq(d.actions.resume.label, '继续处理（剩 2 张）', '继续按钮带剩余数量');
})();

(function testDecisionInvalidatedRow() {
  var run = core.createImportRunState(core.createPagePlan(2));
  core.beginImportRun(run);
  core.invalidateImportRun(run);
  var d = core.importRunDecision(run);
  eq(d.phase, 'invalidated', '失效批次 → invalidated');
  eq(d.busy, false, '失效不再忙碌');
  assert(!d.actions.start.shown && !d.actions.stop.shown &&
    !d.actions.resume.shown && !d.actions.retry.shown, '失效批次无任何运行按钮');
})();

(function testDecisionInvalidatedKeepsFailedPage() {
  // Codex P2-2: 失效为终态。failed 页虽保留在计划里（清扫只动 running），
  // 但重试转换对失效批次是 no-op——按钮不得承诺一个不会发生的动作。
  var run = core.createImportRunState(core.createPagePlan(2));
  core.beginImportRun(run);
  run.plan = core.applyPageEvent(run.plan, 1, 'start');
  run.plan = core.applyPageEvent(run.plan, 1, 'fail', '超时');
  run.plan = core.applyPageEvent(run.plan, 2, 'start');
  core.invalidateImportRun(run); // 失效：failed 页保留
  eq(runStatuses(run), 'failed,pending', '失效保留 failed 页、只清扫 running');
  var d = core.importRunDecision(run);
  eq(d.phase, 'invalidated', '含失败页的失效批次仍为终态 invalidated');
  eq(JSON.stringify(d.failedPages), '[1]', '失败页仍是计划事实（供原因展示）');
  assert(!d.actions.retry.shown, '失效批次不显示重试按钮');
  assert(!d.actions.retry.enabled, '失效批次重试不可用');
  core.retryImportPage(run, 1); // 转换层面同样不得复活
  eq(runStatuses(run), 'failed,pending', '失效批次重试是 no-op');
  eq(run.executing, false, '失效批次重试不产生执行');
})();

(function testDecisionNounLabel() {
  var run = core.createImportRunState(core.createPagePlan(2));
  core.beginImportRun(run);
  core.stopImportRun(run);
  core.settleImportRun(run);
  var d = core.importRunDecision(run, { noun: '页' });
  eq(d.actions.resume.label, '继续处理（剩 2 页）', 'PDF 的名词是页');
})();

if (failures) {
  console.error('test-parent-import-core: ' + failures + ' of ' + checks + ' checks failed');
  process.exit(1);
}
console.log('test-parent-import-core: all ' + checks + ' checks passed');
