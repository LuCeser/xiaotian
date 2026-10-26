// tests/test-parent-browse-lesson-group.js — Issue #89.
// The browse page groups by textbook lesson ownership (the record
// dictation reads), not by the legacy unit tag. A word held because the
// lesson is not yet reached says so on the row.
// `node tests/test-parent-browse-lesson-group.js`
'use strict';

const assert = require('node:assert');
const path = require('node:path');

global.window = global;
require(path.resolve(__dirname, '..', 'pkg', 'web', 'static', 'js', 'parent-browse.js'));

const {
  groupBrowseWords,
  orderBrowseGroups,
  dictationStatusText,
} = global;

const books = [{
  id: 1,
  name: '英语',
  lessons: ['unit 1', 'unit 3'],
  learnedLessons: 1,
}];

// The production shape: evening import left the unit tag empty, afternoon
// wrote unit 1, and 指定课次 set both lessons to unit 3. Progress has
// reached only unit 1. The page must show one unit 3 group and the hold
// line — not 未分组 / unit 1, and not a silent row.
const evening = {
  id: 1,
  word: 'evening',
  unit: '',
  participates: true,
  scope_reason: 'unlearned',
  group_lesson: 'unit 3',
  textbooks: [{ id: 1, name: '英语', lesson: 'unit 3', includeInScope: false }],
};
const afternoon = {
  id: 2,
  word: 'afternoon',
  unit: 'unit 1',
  participates: true,
  scope_reason: 'unlearned',
  group_lesson: 'unit 3',
  textbooks: [{ id: 1, name: '英语', lesson: 'unit 3', includeInScope: false }],
};
const learned = {
  id: 3,
  word: 'learned',
  unit: 'unit 1',
  participates: true,
  scope_reason: 'learned',
  group_lesson: 'unit 1',
  textbooks: [{ id: 1, name: '英语', lesson: 'unit 1', includeInScope: false }],
};
const extra = {
  id: 4,
  word: 'extra',
  unit: 'unit 1',
  participates: true,
  scope_reason: 'supplementary',
  group_lesson: '',
  textbooks: [],
};

const groups = orderBrowseGroups(
  groupBrowseWords([evening, afternoon, learned, extra]),
  books,
);

assert.deepStrictEqual(
  groups.map((g) => ({ kind: g.kind, label: g.label, words: g.rows.map((w) => w.word) })),
  [
    { kind: 'lesson', label: 'unit 1', words: ['learned'] },
    { kind: 'lesson', label: 'unit 3', words: ['evening', 'afternoon'] },
    { kind: 'unit', label: 'unit 1', words: ['extra'] },
  ],
);

assert.strictEqual(dictationStatusText(evening), '尚未学到，学到后自动加入');
assert.strictEqual(dictationStatusText(afternoon), '尚未学到，学到后自动加入');
assert.strictEqual(dictationStatusText(learned), '');
assert.strictEqual(dictationStatusText(extra), '');

// A word the page groups under the learned lesson is one dictation can
// pick; a word it groups under unit 3 carries the hold. The two answers
// are the same gate (scope_reason), not the unit tag.
for (const word of [evening, afternoon]) {
  const group = groups.find((g) => g.rows.some((row) => row.word === word.word));
  assert.strictEqual(group.label, 'unit 3');
  assert.notStrictEqual(dictationStatusText(word), '');
}
const learnedGroup = groups.find((g) => g.rows.some((row) => row.word === 'learned'));
assert.strictEqual(learnedGroup.label, 'unit 1');
assert.strictEqual(dictationStatusText(learned), '');

// 待确认 is its own group, not 未分组, and says how to move the word.
const pending = {
  id: 5,
  word: 'pending',
  unit: 'unit 1',
  participates: true,
  scope_reason: 'pending',
  group_lesson: '',
  textbooks: [{ id: 1, name: '英语', lesson: '', includeInScope: false }],
};
const pendingGroups = groupBrowseWords([pending]);
assert.strictEqual(pendingGroups.length, 1);
assert.strictEqual(pendingGroups[0].kind, 'lesson');
assert.strictEqual(pendingGroups[0].label, '待确认');
assert.strictEqual(dictationStatusText(pending), '课次待确认，指定后自动加入');

// Participation off on a learned word is visible; the lesson hold still
// wins when both apply, because the checkbox already shows the switch.
assert.strictEqual(
  dictationStatusText({ participates: false, scope_reason: 'learned' }),
  '已移出默写范围',
);
assert.strictEqual(
  dictationStatusText({ participates: false, scope_reason: 'unlearned' }),
  '尚未学到，学到后自动加入',
);

// Without a server verdict, a textbook assignment still beats the unit tag
// so 指定课次 moves the row on the next refresh.
const assignedOnly = {
  id: 6,
  word: 'assigned',
  unit: '',
  textbooks: [{ id: 1, name: '英语', lesson: 'unit 3' }],
};
const fallback = groupBrowseWords([assignedOnly]);
assert.strictEqual(fallback[0].kind, 'lesson');
assert.strictEqual(fallback[0].label, 'unit 3');

console.log('test-parent-browse-lesson-group: all assertions passed');
