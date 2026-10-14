// Node unit test for the Issue #8 supplement pure functions
// (parent-meaning-core.js). Same harness shape as test-parent-photo-pdf.js:
// global.window shim, require the IIFE module, hand-rolled asserts.
'use strict';

const path = require('path');
const assert = require('assert');

global.window = global;
require(path.resolve(__dirname, '../pkg/web/static/js/parent-meaning-core.js'));

const {
  isSupplementTarget,
  supplementWords,
  applyGeneratedMeanings,
  cancelGeneratedMeaning,
  meaningMarkerVisible,
  genFailedVisible,
  meaningGenSummary,
} = global.window;

let failures = 0;
function eq(actual, expected, label) {
  const a = JSON.stringify(actual);
  const b = JSON.stringify(expected);
  if (a !== b) {
    failures++;
    console.error(`FAIL ${label}\n  actual   ${a}\n  expected ${b}`);
  }
}

function row(over) {
  return Object.assign(
    { index: 0, word: 'apple', meaning: '', lesson: '', status: 'new', reason: '', existingMeaning: '', resolution: '', checked: true },
    over || {}
  );
}

// --- isSupplementTarget: only add-path rows with an empty meaning -------
eq(isSupplementTarget(row()), true, 'new empty checked row is a target');
eq(isSupplementTarget(row({ meaning: '苹果' })), false, 'row with material meaning is not a target');
eq(isSupplementTarget(row({ status: 'exists' })), false, 'exists row never supplements (library governs)');
eq(isSupplementTarget(row({ status: 'conflict' })), false, 'conflict row never supplements (#6 semantics)');
eq(isSupplementTarget(row({ status: 'invalid' })), false, 'invalid row never supplements');
eq(isSupplementTarget(row({ checked: false })), false, 'unchecked row is not worth a call');
eq(isSupplementTarget(null), false, 'null row is not a target');

// --- supplementWords: row order, dedup, trim ---------------------------
eq(
  supplementWords([
    row({ word: 'apple' }),
    row({ word: ' banana ', meaning: '有释义' }),
    row({ index: 1, word: 'apple' }),
    row({ index: 2, word: 'cherry', status: 'exists' }),
  ]),
  ['apple'],
  'supplementWords: only eligible, trimmed, distinct'
);
eq(supplementWords([]), [], 'supplementWords: empty rows');

// --- applyGeneratedMeanings: AC-1 material priority is structural -------
const rows = [
  row({ index: 0, word: 'apple' }),                     // eligible, answered
  row({ index: 1, word: 'banana' }),                    // eligible, missing
  row({ index: 2, word: 'cat', meaning: '猫' }),        // material meaning: untouched
  row({ index: 3, word: 'dog', status: 'exists' }),     // exists: untouched
];
const after = applyGeneratedMeanings(rows, {
  meanings: { apple: 'n. 苹果', cat: '黑客猫' },
  missing: ['banana'],
});
eq(after[0].meaning, 'n. 苹果', 'answered word filled');
eq(after[0].meaningSource, 'generated', 'answered word marked generated');
eq(after[0].genFailed, false, 'answered word failure cleared');
eq(after[1].meaning, '', 'unanswered word stays empty');
eq(after[1].meaningSource, undefined, 'unanswered word keeps no marker');
eq(after[1].genFailed, true, 'unanswered word flagged failed');
eq(after[2].meaning, '猫', 'AC-1: material meaning never overwritten');
eq(after[2].meaningSource, undefined, 'material row gains no marker');
eq(after[3].meaning, '', 'exists row untouched');
eq(rows[0].meaning, '', 'pure: input rows not mutated');

// Parent-typed text between the click and the reply also outranks the
// model (the eligibility check runs at APPLY time, not click time).
const raced = applyGeneratedMeanings(
  [row({ word: 'apple', meaning: '家长刚填' })],
  { meanings: { apple: 'n. 苹果' }, missing: [] }
);
eq(raced[0].meaning, '家长刚填', 'parent text typed during the call wins');

// --- cancelGeneratedMeaning --------------------------------------------
const cancelled = cancelGeneratedMeaning(after, 0);
eq(cancelled[0].meaning, '', 'cancel empties the supplement');
eq(cancelled[0].meaningSource, 'material', 'cancel resets the source');
eq(cancelled[0].genFailed, false, 'cancel clears the failure flag');
eq(cancelled[1].genFailed, true, 'cancel leaves other rows alone');
eq(cancelGeneratedMeaning(after, 2)[2].meaning, '猫', 'cancelling a non-generated row is a no-op');

// --- display rules ------------------------------------------------------
eq(meaningMarkerVisible(after[0]), true, 'marker: generated + non-empty');
eq(meaningMarkerVisible({ meaning: '', meaningSource: 'generated' }), false, 'marker: empty gloss never marked');
eq(meaningMarkerVisible({ meaning: '猫' }), false, 'marker: material never marked');
eq(genFailedVisible(after[1]), true, 'genFailed: empty + flagged');
eq(genFailedVisible({ meaning: '手填', genFailed: true }), false, 'genFailed: manual text resolves the note');
eq(genFailedVisible({ meaning: '', genFailed: false }), false, 'genFailed: unflagged');

// --- meaningGenSummary drives the affordance label ----------------------
eq(meaningGenSummary(rows), { target: 2, failed: 0 }, 'summary counts targets');
// A failed row is STILL a target (retry re-sends exactly the missing set)
// and additionally counts as failed — the label switches to 重试生成.
eq(meaningGenSummary(after), { target: 1, failed: 1 }, 'summary: banana is target AND failed');
// A cancelled row becomes a target again (re-supplement allowed).
eq(meaningGenSummary(cancelled), { target: 2, failed: 1 }, 'summary: cancelled row re-eligible');

if (failures) {
  console.error(`\n${failures} check(s) failed`);
  process.exit(1);
}
console.log('test-parent-photo-meaning: all checks passed');
