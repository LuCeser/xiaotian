// tests/test-batch-form.js — headless verification of the batch-submission
// client contract (Ticket 05): the index→word answers mapping is pure and
// pinned here (trim, skip empties, all-empty → null = client-side validation
// before the wire). `node tests/test-batch-form.js`.
'use strict';

const assert = require('node:assert');

// The IIFE assigns onto its `global` arg — in Node's CommonJS that is the
// module's top-level `this` (module.exports).
const { BatchSubmissionForm } = require('../pkg/web/static/js/ui-components.js');

const words = ['apple', 'banana', 'cherry'];

// 1. Full mapping in queue order.
{
  const answers = BatchSubmissionForm.buildAnswers(words, ['apple', 'banane', 'cherry']);
  assert.deepStrictEqual(answers, { apple: 'apple', banana: 'banane', cherry: 'cherry' });
}

// 2. Values are trimmed.
{
  const answers = BatchSubmissionForm.buildAnswers(words, ['  apple  ', '', '\tcherry\n']);
  assert.deepStrictEqual(answers, { apple: 'apple', cherry: 'cherry' });
}

// 3. Empty rows are omitted (a blank grades incorrect server-side) — partial
//    submissions are allowed.
{
  const answers = BatchSubmissionForm.buildAnswers(words, ['', 'banana', '']);
  assert.deepStrictEqual(answers, { banana: 'banana' });
}

// 4. All-empty (or whitespace-only) → null: never send an empty answers map
//    (the server rejects it; the client validates first).
{
  assert.strictEqual(BatchSubmissionForm.buildAnswers(words, ['', '  ', '']), null);
  assert.strictEqual(BatchSubmissionForm.buildAnswers(words, []), null);
  assert.strictEqual(BatchSubmissionForm.buildAnswers([], ['x']), null);
}

// 5. Visibility gate (B1 fix): the form opens when the session is active,
//    in batch mode, at WAITING_PARENT, and not already open. Pure static —
//    the DOM half lives in app.js's subscriber + the form's top-level
//    index.html section.
{
  const waitingBatch = { status: 'WAITING_PARENT', batchMode: true, sessionActive: true };
  assert.strictEqual(BatchSubmissionForm.shouldOpenForState(waitingBatch, false), true);
  // Already open → do not re-open (show() rebuilds rows).
  assert.strictEqual(BatchSubmissionForm.shouldOpenForState(waitingBatch, true), false);
  // Per-word mode → the auto-ratings path owns WAITING_PARENT, not the form.
  assert.strictEqual(BatchSubmissionForm.shouldOpenForState({ status: 'WAITING_PARENT', batchMode: false, sessionActive: true }, false), false);
  // Session over → never.
  assert.strictEqual(BatchSubmissionForm.shouldOpenForState({ status: 'WAITING_PARENT', batchMode: true, sessionActive: false }, false), false);
  // Still dictating → not yet.
  assert.strictEqual(BatchSubmissionForm.shouldOpenForState({ status: 'DICTATING', batchMode: true, sessionActive: true }, false), false);
  // Degenerate snapshots.
  assert.strictEqual(BatchSubmissionForm.shouldOpenForState(null, false), false);
  assert.strictEqual(BatchSubmissionForm.shouldOpenForState({}, false), false);
}

// 6. WAITING_PARENT status text is mode-aware (B1/B2 fix): batch students
//    get the fill-the-form hint (the form is visible at this status now),
//    per-word students get the auto-completing hint. Other statuses keep
//    the static map; unknown statuses fall back to IDLE.
{
  const { ProgressDisplay } = require('../pkg/web/static/js/ui-components.js');
  assert.strictEqual(ProgressDisplay.statusMessage('WAITING_PARENT', true), ProgressDisplay.WAITING_PARENT_MESSAGES.batch);
  assert.strictEqual(ProgressDisplay.statusMessage('WAITING_PARENT', false), ProgressDisplay.WAITING_PARENT_MESSAGES.perWord);
  // The hint tells batch students to fill the form and tells per-word
  // students the summary is auto-generated (not a dead end).
  assert.ok(/请填写答案并提交/.test(ProgressDisplay.statusMessage('WAITING_PARENT', true)));
  assert.ok(/正在生成小结/.test(ProgressDisplay.statusMessage('WAITING_PARENT', false)));
  // Non-WAITING_PARENT statuses ignore the mode flag.
  assert.strictEqual(ProgressDisplay.statusMessage('DICTATING', true), ProgressDisplay.STATUS_MESSAGES.DICTATING);
  assert.strictEqual(ProgressDisplay.statusMessage('FEEDBACK', false), ProgressDisplay.STATUS_MESSAGES.FEEDBACK);
  assert.strictEqual(ProgressDisplay.statusMessage('UNKNOWN_STATUS', true), ProgressDisplay.STATUS_MESSAGES.IDLE);
}

// 7. Sub-hint line (2026-09-24 redesign): mode-aware whose-turn guidance.
//    Pure static — the DOM half lives in ProgressDisplay._updatePlayPhase.
{
  const { ProgressDisplay } = require('../pkg/web/static/js/ui-components.js');
  // Dictating: per-word students speak the answer; batch students write it.
  assert.ok(/说出/.test(ProgressDisplay.subHint('DICTATING', false)));
  assert.ok(/写在纸上/.test(ProgressDisplay.subHint('DICTATING', true)));
  assert.strictEqual(ProgressDisplay.subHint('LISTENING', true), ProgressDisplay.subHint('DICTATING', true));
  // Wrap-up: batch fills the form; per-word waits for the auto summary.
  assert.ok(/表格/.test(ProgressDisplay.subHint('WAITING_PARENT', true)));
  assert.ok(/稍等/.test(ProgressDisplay.subHint('WAITING_PARENT', false)));
  assert.ok(/批改/.test(ProgressDisplay.subHint('SUBMISSION', true)));
  assert.ok(/报告/.test(ProgressDisplay.subHint('FEEDBACK', false)));
  // Idle / unknown → no hint.
  assert.strictEqual(ProgressDisplay.subHint('IDLE', false), '');
  assert.strictEqual(ProgressDisplay.subHint('UNKNOWN', true), '');
}

console.log('test-batch-form.js: all assertions passed');
