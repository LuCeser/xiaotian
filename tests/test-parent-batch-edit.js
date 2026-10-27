/**
 * Node unit tests for the Issue #80 batch-edit preview pure functions
 * (window.* exports of parent-batch-edit.js — window.parseBatch precedent).
 *
 * Covered contracts (AC-09: 批量改词面和释义、预览、取消不写库):
 *   - Alignment is positional; a count mismatch refuses the whole preview.
 *   - An unchanged row reports unchanged, never as an edit.
 *   - A face change onto an existing face at another id is BLOCKED
 *     (ADR-0003 identity: never overwrites the other card) — including
 *     the rename-onto-another-selected-word case.
 *   - A rename onto the row's OWN face (whitespace-only face change is
 *     impossible — the parser trims) and meaning-only edits pass.
 *   - The meaning cap mirrors the API (1000 runes).
 */
'use strict';

const path = require('node:path');

global.window = global;
require(path.resolve(__dirname, '..', 'pkg', 'web', 'static', 'js', 'parent-batch-parser.js'));
require(path.resolve(__dirname, '..', 'pkg', 'web', 'static', 'js', 'parent-batch-edit.js'));

const {
  parseBatch, alignPastedEdits, previewBatchEdits, blockedEdits,
  batchEditPrefillLine,
} = global.window;

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
  assert(JSON.stringify(actual) === JSON.stringify(expected),
    label + ' — got ' + JSON.stringify(actual) + ', want ' + JSON.stringify(expected));
}

// The selected browse rows (GET /words shape) and the library snapshot it
// lives in — the snapshot INCLUDES the selected rows by construction.
const SELECTED = [
  { id: 11, word: 'tiger', meaning: '老虎' },
  { id: 12, word: 'panda', meaning: '熊猫' },
  { id: 13, word: 'grape', meaning: '葡萄' },
];
const LIBRARY = SELECTED.concat([
  { id: 20, word: 'horse', meaning: '马' },
]);

// ----------------------------------------------------------------------
// alignPastedEdits — positional alignment
// ----------------------------------------------------------------------

(function () {
  const parsed = parseBatch('tiger\t老虎（修订）\npanda\t熊猫\ngrape\t葡萄');
  const aligned = alignPastedEdits(SELECTED, parsed.rows);
  assert(aligned.ok, 'equal counts align');
  eq(aligned.edits.map(function (e) { return e.id; }), [11, 12, 13],
    'the k-th pasted row edits the k-th selected word');
  eq(aligned.edits[0].wordTo, 'tiger', 'the paste side carries the (possibly corrected) face');

  // Count mismatch: the whole preview refuses — positional edits over a
  // miscounted paste would land on the wrong cards.
  const short = alignPastedEdits(SELECTED, parseBatch('tiger\t老虎').rows);
  eq(short, { ok: false, code: 'count-mismatch', pasted: 1, selected: 3 },
    'a short paste refuses with both counts');
  const long = alignPastedEdits(SELECTED, parseBatch('a\t1\nb\t2\nc\t3\nd\t4').rows);
  eq(long.code, 'count-mismatch', 'a long paste refuses too');
  eq(long.pasted, 4, 'the refusal reports the pasted count');

  eq(alignPastedEdits([], []).edits, [], 'an empty selection and empty paste align to nothing');
})();

// ----------------------------------------------------------------------
// previewBatchEdits — kinds, blocks, and the unchanged count
// ----------------------------------------------------------------------

(function () {
  // One meaning edit, one face+meaning correction, one untouched row.
  const preview = previewBatchEdits(SELECTED, parseBatch(
    'tiger\t老虎（修订）\ntigr\t老虎\ngrape\t葡萄').rows, LIBRARY);
  assert(preview.ok, 'the preview builds');
  eq(preview.edits.map(function (e) { return e.kind; }), ['edit', 'edit', 'unchanged'],
    'an unchanged row reports unchanged');
  eq(preview.unchanged, 1, 'the unchanged count is separate from the edits');
  eq(preview.edits[0].meaningTo, '老虎（修订）', 'a meaning-only edit carries the new meaning');
  eq(preview.edits[1].wordFrom, 'panda', 'a face correction keeps the from-side honest');
  eq(preview.edits[1].wordTo, 'tigr', 'a face correction carries the new face');
  eq(blockedEdits(preview), [], 'nothing blocks a clean preview');

  // Identity conflict: renaming panda onto horse's face (id 20) — blocked,
  // never an overwrite of horse's card and history.
  const conflict = previewBatchEdits(SELECTED, parseBatch(
    'tiger\t老虎（修订）\nhorse\t马\ngrape\t葡萄').rows, LIBRARY);
  const blocked = blockedEdits(conflict);
  eq(blocked.length, 1, 'the conflicting edit is reported');
  eq(blocked[0].id, 12, 'the block names the edited card');
  eq(blocked[0].blocked, 'identity-conflict', 'the block reason is the identity rule');
  eq(blocked[0].conflictId, 20, 'the block names the face\'s real owner');
  eq(conflict.edits[0].kind, 'edit', 'the other edits are untouched by the block');
  eq(conflict.edits[2].kind, 'unchanged', 'an untouched row stays unchanged beside a block');

  // Renaming onto ANOTHER SELECTED word's face is the same conflict (the
  // library snapshot includes the selected rows).
  const swap = previewBatchEdits(SELECTED, parseBatch(
    'tiger\t老虎\ngrape\t熊猫\ngrape\t葡萄').rows, LIBRARY);
  // (the duplicate grape line is a parse error — use distinct faces)
  eq(swap.ok, false, 'sanity: the duplicate paste itself refuses to align');

  const cross = previewBatchEdits(SELECTED, parseBatch(
    'tiger\t老虎\ngrape\t熊猫\ngrape2\t葡萄').rows, LIBRARY);
  eq(blockedEdits(cross).map(function (e) { return e.id; }), [12],
    'a rename onto another selected word\'s face blocks');
  eq(blockedEdits(cross)[0].conflictId, 13, 'the owner is the selected word itself');

  // A rename onto the row's OWN face is not a conflict (it is not a change
  // at all after the parser's trim).
  const self = previewBatchEdits([{ id: 11, word: 'tiger', meaning: '老虎' }],
    parseBatch('tiger\t虎').rows, [{ id: 11, word: 'tiger', meaning: '老虎' }]);
  eq(self.edits[0].kind, 'edit', 'a meaning-only edit on an owned face is a plain edit');
  eq(blockedEdits(self), [], 'own face never conflicts with itself');

  // ADR-0003: identity is per category — English colour → color must not
  // block on a Chinese homograph at another id.
  const crossCat = previewBatchEdits(
    [{ id: 30, word: 'colour', meaning: '颜色', category: 'english' }],
    parseBatch('color\t颜色').rows,
    [
      { id: 30, word: 'colour', meaning: '颜色', category: 'english' },
      { id: 40, word: 'color', meaning: '颜色', category: 'chinese' },
    ]);
  eq(blockedEdits(crossCat), [], 'a cross-category homograph is not an identity conflict');

  // Prefill round-trip: a spaced word with empty meaning must not split on
  // the space when the parent confirms other rows unchanged.
  const line = batchEditPrefillLine('ice cream', '');
  eq(parseBatch(line).rows, [{ word: 'ice cream', meaning: '' }],
    'batchEditPrefillLine keeps spaced words intact');

  // The meaning cap mirrors the API (1000 runes — code points).
  const longMeaning = '长'.repeat(1001);
  const capped = previewBatchEdits([{ id: 11, word: 'tiger', meaning: '老虎' }],
    parseBatch('tiger\t' + longMeaning).rows, LIBRARY);
  eq(capped.edits[0].blocked, 'meaning-too-long', 'a 1001-rune meaning is blocked');
  const fitting = previewBatchEdits([{ id: 11, word: 'tiger', meaning: '老虎' }],
    parseBatch('tiger\t' + '长'.repeat(1000)).rows, LIBRARY);
  eq(fitting.edits[0].blocked, undefined, 'a 1000-rune meaning passes');

  // A count mismatch carries the same refusal through the preview shape.
  const mismatch = previewBatchEdits(SELECTED, [], LIBRARY);
  eq(mismatch.ok, false, 'a mismatched preview refuses');
  eq(mismatch.code, 'count-mismatch', 'the refusal code rides along');
  eq(mismatch.edits, [], 'a refused preview carries no edits');
})();

// ----------------------------------------------------------------------

if (failures) {
  console.error('parent-batch-edit: ' + failures + ' of ' + checks + ' checks failed');
  process.exit(1);
}
console.log('parent-batch-edit: all ' + checks + ' checks passed');
