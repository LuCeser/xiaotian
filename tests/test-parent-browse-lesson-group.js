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
  supplementaryDeletePlan,
  unitRenameTouchesLesson,
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
  category: 'english',
  unit: '',
  participates: true,
  scope_reason: 'unlearned',
  group_lesson: 'unit 3',
  textbooks: [{ id: 1, name: '英语', lesson: 'unit 3', includeInScope: false }],
};
const afternoon = {
  id: 2,
  word: 'afternoon',
  category: 'english',
  unit: 'unit 1',
  participates: true,
  scope_reason: 'unlearned',
  group_lesson: 'unit 3',
  textbooks: [{ id: 1, name: '英语', lesson: 'unit 3', includeInScope: false }],
};
const learned = {
  id: 3,
  word: 'learned',
  category: 'english',
  unit: 'unit 1',
  participates: true,
  scope_reason: 'learned',
  group_lesson: 'unit 1',
  textbooks: [{ id: 1, name: '英语', lesson: 'unit 1', includeInScope: false }],
};
const extra = {
  id: 4,
  word: 'extra',
  category: 'english',
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
    { kind: 'unit', label: '补充 · unit 1', words: ['extra'] },
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

// Participation off says so FIRST, even when a lesson hold also applies:
// eligibility is participates AND 课次门槛, so 「学到后自动加入」 would be
// a false promise — learning the lesson alone would not bring the word
// back while the parent's removal stands.
assert.strictEqual(
  dictationStatusText({ participates: false, scope_reason: 'learned' }),
  '已移出默写范围',
);
assert.strictEqual(
  dictationStatusText({ participates: false, scope_reason: 'unlearned' }),
  '已移出默写范围',
);
assert.strictEqual(
  dictationStatusText({ participates: false, scope_reason: 'off_list' }),
  '已移出默写范围',
);

// 课表外 (off_list): the lesson is no longer on the book's list — it never
// becomes learned, so the hold line must not promise 学到后自动加入.
const stray = {
  id: 8,
  word: 'stray',
  category: 'english',
  unit: '',
  participates: true,
  scope_reason: 'off_list',
  group_lesson: '第9课',
  group_textbook_id: 1,
  textbooks: [{ id: 1, name: '英语', lesson: '第9课', includeInScope: false }],
};
assert.strictEqual(
  dictationStatusText(stray),
  '课次不在教材课次表里，不会随进度自动加入',
);
// It still groups under the stray lesson (the row explains itself there).
const strayGroups = groupBrowseWords([stray]);
assert.strictEqual(strayGroups[0].kind, 'lesson');
assert.strictEqual(strayGroups[0].label, '第9课');

// Two textbooks can both carry 「第1课」 — the lessons are independent
// (课次属于教材): separate groups even under one shared name, each book's
// own teaching order, and both same-named headers say which book is which.
const twoBooks = [
  { id: 1, name: '课内英语', lessons: ['第1课', '第2课'] },
  { id: 2, name: '课外英语', lessons: ['第1课'] },
];
const inClassFirst = {
  id: 9,
  word: 'ant',
  category: 'english',
  unit: '',
  participates: true,
  scope_reason: 'learned',
  group_lesson: '第1课',
  group_textbook_id: 1,
  textbooks: [{ id: 1, name: '课内英语', lesson: '第1课', includeInScope: false }],
};
const inClassSecond = {
  id: 10,
  word: 'apple',
  category: 'english',
  unit: '',
  participates: true,
  scope_reason: 'unlearned',
  group_lesson: '第2课',
  group_textbook_id: 1,
  textbooks: [{ id: 1, name: '课内英语', lesson: '第2课', includeInScope: false }],
};
const afterClass = {
  id: 11,
  word: 'ape',
  category: 'english',
  unit: '',
  participates: true,
  scope_reason: 'unlearned',
  group_lesson: '第1课',
  group_textbook_id: 2,
  textbooks: [{ id: 2, name: '课外英语', lesson: '第1课', includeInScope: false }],
};
// First-seen order deliberately interleaves the books; the sort must still
// keep each book's lessons together in that book's own order.
const two = orderBrowseGroups(
  groupBrowseWords([inClassSecond, afterClass, inClassFirst]),
  twoBooks,
);
assert.deepStrictEqual(
  two.map((g) => ({ label: g.label, words: g.rows.map((w) => w.word) })),
  [
    { label: '课内英语·第1课', words: ['ant'] },
    // 第2课 is worn by ONE book only — the plain name stays.
    { label: '第2课', words: ['apple'] },
    { label: '课外英语·第1课', words: ['ape'] },
  ],
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

// A supplementary unit that is not a lesson name keeps its own label.
const plain = orderBrowseGroups(groupBrowseWords([
  { id: 7, word: 'plain', unit: 'week-1', participates: true, scope_reason: 'supplementary', textbooks: [] },
]), books);
assert.strictEqual(plain[0].label, 'week-1');

// The supplementary header that shares a lesson name must count and
// delete only its own row. GET /units would say 3 (afternoon, learned,
// extra all wear unit 1) and a {category, unit} delete would remove the
// lesson-owned words the page shows under unit 1 and unit 3.
const extraGroup = groups.find((g) => g.label === '补充 · unit 1');
assert.deepStrictEqual(supplementaryDeletePlan(extraGroup), {
  count: 1,
  requests: [{ category: 'english', words: ['extra'] }],
});
assert.strictEqual(
  unitRenameTouchesLesson([evening, afternoon, learned, extra], 'unit 1'),
  true,
);
assert.strictEqual(unitRenameTouchesLesson([extra], 'unit 1'), false);

// A supplementary-only unit still names every row, split at the 100-word
// delete cap, and does not look like a lesson rename.
const bulk = [];
for (let i = 0; i < 101; i++) {
  bulk.push({
    id: 100 + i,
    word: 'w' + i,
    category: i < 100 ? 'english' : 'chinese',
    unit: 'week-9',
    participates: true,
    scope_reason: 'supplementary',
    textbooks: [],
  });
}
const bulkGroup = groupBrowseWords(bulk)[0];
const bulkPlan = supplementaryDeletePlan(bulkGroup);
assert.strictEqual(bulkPlan.count, 101);
assert.deepStrictEqual(bulkPlan.requests, [
  { category: 'english', words: bulk.slice(0, 100).map((w) => w.word) },
  { category: 'chinese', words: ['w100'] },
]);
assert.strictEqual(unitRenameTouchesLesson(bulk, 'week-9'), false);

console.log('test-parent-browse-lesson-group: all assertions passed');
