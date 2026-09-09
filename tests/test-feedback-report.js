// tests/test-feedback-report.js — headless verification of the feedback
// report client contract (Ticket 06): server diff ops → char-highlight spans
// on both sides of the word, and the score-tier emoji. `node tests/test-feedback-report.js`.
'use strict';

const assert = require('node:assert');

const { FeedbackReport } = require('../pkg/web/static/js/ui-components.js');

// 1. Substitution: 香蕉 written as 香焦 — expected side highlights the wrong
//    char, heard side shows what was written instead.
{
  const ops = [
    { expected: '香', heard: '香', kind: 'match' },
    { expected: '蕉', heard: '焦', kind: 'sub' }
  ];
  assert.deepStrictEqual(FeedbackReport.buildCharSpans(ops, 'expected'), [
    { ch: '香', cls: 'ok' },
    { ch: '蕉', cls: 'bad' }
  ]);
  assert.deepStrictEqual(FeedbackReport.buildCharSpans(ops, 'heard'), [
    { ch: '香', cls: 'dim' },
    { ch: '焦', cls: 'bad' }
  ]);
}

// 2. Deletion + insertion: word→wrld is one deleted 'o' plus an extra 'd';
//    expected side carries the miss, heard side carries the extra.
{
  const ops = [
    { expected: 'w', heard: 'w', kind: 'match' },
    { expected: 'o', heard: '', kind: 'del' },
    { expected: 'r', heard: 'r', kind: 'match' },
    { expected: 'd', heard: 'd', kind: 'match' },
    { expected: '', heard: 'd', kind: 'ins' }
  ];
  assert.deepStrictEqual(FeedbackReport.buildCharSpans(ops, 'expected'), [
    { ch: 'w', cls: 'ok' },
    { ch: 'o', cls: 'miss' },
    { ch: 'r', cls: 'ok' },
    { ch: 'd', cls: 'ok' }
  ]);
  assert.deepStrictEqual(FeedbackReport.buildCharSpans(ops, 'heard'), [
    { ch: 'w', cls: 'dim' },
    { ch: 'r', cls: 'dim' },
    { ch: 'd', cls: 'dim' },
    { ch: 'd', cls: 'extra' }
  ]);
}

// 3. Transposition: both crossed runes render bad on both sides.
{
  const ops = [
    { expected: 'e', heard: 'i', kind: 'swap' },
    { expected: 'i', heard: 'e', kind: 'swap' }
  ];
  assert.deepStrictEqual(FeedbackReport.buildCharSpans(ops, 'expected'), [
    { ch: 'e', cls: 'bad' },
    { ch: 'i', cls: 'bad' }
  ]);
  assert.deepStrictEqual(FeedbackReport.buildCharSpans(ops, 'heard'), [
    { ch: 'i', cls: 'bad' },
    { ch: 'e', cls: 'bad' }
  ]);
}

// 4. Degenerate inputs: no ops / null ops → no spans on either side.
{
  assert.deepStrictEqual(FeedbackReport.buildCharSpans([], 'expected'), []);
  assert.deepStrictEqual(FeedbackReport.buildCharSpans(null, 'heard'), []);
}

// 5. Score tiers map to escalating emoji.
{
  assert.strictEqual(FeedbackReport.scoreEmoji(100), '🏆');
  assert.strictEqual(FeedbackReport.scoreEmoji(85), '🎉');
  assert.strictEqual(FeedbackReport.scoreEmoji(70), '👍');
  assert.strictEqual(FeedbackReport.scoreEmoji(40), '💪');
  assert.strictEqual(FeedbackReport.scoreEmoji(0), '💪');
}

console.log('test-feedback-report.js: all assertions passed');
