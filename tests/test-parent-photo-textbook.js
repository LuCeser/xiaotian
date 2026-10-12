/**
 * Node unit tests for the Issue #5 photo-import textbook pure functions
 * (window.* exports of parent-photo.js — window.parseBatch precedent).
 *
 * Covered contracts:
 *   - classifyPhotoEntries carries each entry's lesson into the row (the
 *     Go pipeline already applied page-level inheritance).
 *   - matchTextbookForMeta preselects an existing textbook only when the
 *     recognized meta ACTUALLY matches it — never a guess (AC2's
 *     不编造 rule starts at the preselect).
 *   - suggestTextbookName builds the create-form name from the readable
 *     fields only.
 *   - nextLessonList appends a missing lesson at the END (teaching order
 *     is explicit; never sorted) and is a no-op for an existing one.
 *   - planAssignments groups the committable rows into assign calls by
 *     (lesson, includeInScope): checked new/exists rows only — conflict
 *     and invalid rows never reach a textbook.
 */
'use strict';

const path = require('node:path');

// Minimal window shim, then load parent-photo.js the way
// tests/test-parent-photo-classify.js does.
global.window = global;
require(path.resolve(__dirname, '..', 'pkg', 'web', 'static', 'js', 'parent-photo.js'));

const {
  classifyPhotoEntries,
  matchTextbookForMeta,
  suggestTextbookName,
  nextLessonList,
  planAssignments,
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

// ----------------------------------------------------------------------
// classifyPhotoEntries carries the entry lesson into the row
// ----------------------------------------------------------------------

(function () {
  const rows = classifyPhotoEntries([
    { word: 'apple', meaning: '苹果', lesson: '第5课' },
    { word: 'river', meaning: '河流' },
  ], []);
  eq(rows[0].lesson, '第5课', 'classify carries an explicit entry lesson');
  eq(rows[1].lesson, '', 'classify leaves an absent lesson empty');
})();

// ----------------------------------------------------------------------
// matchTextbookForMeta
// ----------------------------------------------------------------------

const BOOKS = [
  { id: 1, name: 'A', edition: '人教版PEP', stage: '', grade: '三年级', volume: '上册', module: '' },
  { id: 2, name: 'B', edition: '人教版PEP', stage: '', grade: '三年级', volume: '上册', module: 'Unit 1-3' },
  { id: 3, name: 'C', edition: '外研版', stage: '', grade: '四年级', volume: '下册', module: '' },
];

(function () {
  // Every non-empty meta field must agree with the candidate; more matched
  // fields wins (B beats A on module).
  eq(matchTextbookForMeta(BOOKS, {
    edition: '人教版PEP', stage: '', grade: '三年级', volume: '上册', module: 'Unit 1-3',
  }).id, 2, 'exact meta match with the most matched fields wins');

  // A meta without module matches A, not B (B's module disagrees with '').
  eq(matchTextbookForMeta(BOOKS, {
    edition: '人教版PEP', stage: '', grade: '三年级', volume: '上册', module: '',
  }).id, 1, 'a disagreeing module disqualifies the candidate');

  // No honest match → null (never a guess).
  eq(matchTextbookForMeta(BOOKS, {
    edition: '译林版', stage: '', grade: '三年级', volume: '上册', module: '',
  }), null, 'unmatched meta returns null, not a lookalike');

  // Empty meta never preselects anything.
  eq(matchTextbookForMeta(BOOKS, null), null, 'no meta → no preselect');
  eq(matchTextbookForMeta(BOOKS, {
    edition: '', stage: '', grade: '', volume: '', module: '',
  }), null, 'all-empty meta → no preselect');

  // Tie on matched fields → the earliest (creation order) wins.
  eq(matchTextbookForMeta([
    { id: 9, name: 'X', edition: '人教版', stage: '', grade: '', volume: '', module: '' },
    { id: 10, name: 'Y', edition: '人教版', stage: '', grade: '', volume: '', module: '' },
  ], { edition: '人教版', stage: '', grade: '', volume: '', module: '' }).id, 9,
    'tie resolves to creation order');
})();

// ----------------------------------------------------------------------
// suggestTextbookName
// ----------------------------------------------------------------------

(function () {
  eq(suggestTextbookName({ edition: '人教版PEP', stage: '', grade: '三年级', volume: '上册', module: '', lesson: '第5课' }),
    '人教版PEP·三年级·上册', 'name joins the readable fields with ·');
  eq(suggestTextbookName(null), '', 'no meta → empty suggestion');
  eq(suggestTextbookName({ edition: '', stage: '', grade: '', volume: '', module: '', lesson: '第9课' }),
    '', 'a lesson alone suggests nothing');
})();

// ----------------------------------------------------------------------
// nextLessonList
// ----------------------------------------------------------------------

(function () {
  eq(nextLessonList(['第1课', '第2课'], '第3课'), ['第1课', '第2课', '第3课'],
    'a missing lesson appends at the end (never sorted)');
  eq(nextLessonList(['第2课', '第1课'], '第1课'), ['第2课', '第1课'],
    'an existing lesson is a no-op — order untouched');
  eq(nextLessonList([], '第5课'), ['第5课'], 'an empty list gains the lesson');
  eq(nextLessonList(['第1课'], ''), ['第1课'], 'an empty lesson never enters the list');
})();

// ----------------------------------------------------------------------
// planAssignments
// ----------------------------------------------------------------------

const ROWS = [
  { index: 0, word: 'apple', meaning: '苹果', lesson: '第5课', status: 'new', checked: true },
  { index: 1, word: 'river', meaning: '河流', lesson: '第5课', status: 'new', checked: true },
  { index: 2, word: 'sun', meaning: '太阳', lesson: '', status: 'new', checked: true },
  { index: 3, word: 'moon', meaning: '月亮', lesson: '第6课', status: 'new', checked: false },
  { index: 4, word: 'lake', meaning: '湖', lesson: '第7课', status: 'exists', checked: true },
  { index: 5, word: 'star', meaning: '星星', lesson: '第7课', status: 'conflict', checked: false },
  { index: 6, word: 'dust', meaning: '', lesson: '第7课', status: 'invalid', reason: 'empty-word', checked: false },
  { index: 7, word: 'cloud', meaning: '云', lesson: '第5课', status: 'exists', checked: false },
];

const IDS = { apple: 11, river: 12, sun: 13, moon: 14, lake: 15, cloud: 17 };

(function () {
  const groups = planAssignments(ROWS, IDS, false);
  // Groups keyed by lesson in first-appearance order; unchecked/conflict/
  // invalid rows never assigned; exists rows DO get their lesson.
  eq(groups.map(function (g) { return g.lesson; }), ['第5课', '', '第7课'],
    'groups appear in first-appearance lesson order');
  eq(groups[0].cardIds, [11, 12], 'checked new rows with the lesson group together');
  eq(groups[0].words, ['apple', 'river'], 'group carries its words for the receipt');
  eq(groups[1].cardIds, [13], 'an empty lesson forms the 待确认 group');
  eq(groups[2].cardIds, [15], 'an exists row is assignable, conflict/invalid/unchecked are not');

  // includePending lifts ONLY the 待确认 group into scope.
  const withInclude = planAssignments(ROWS, IDS, true);
  const pending = withInclude.filter(function (g) { return g.lesson === ''; })[0];
  eq(pending.includeInScope, true, 'includePending sets includeInScope on the 待确认 group');
  const lessonGroups = withInclude.filter(function (g) { return g.lesson !== ''; });
  assert(lessonGroups.every(function (g) { return g.includeInScope === false; }),
    'rows with a real lesson never ride the include override');

  // A word missing from the snapshot (id unknown) is skipped, not guessed.
  const partial = planAssignments([
    { index: 0, word: 'ghost', meaning: '', lesson: '第1课', status: 'new', checked: true },
  ], {}, false);
  eq(partial, [], 'a word without a resolved id forms no group');

  // Nothing committable → no calls at all.
  eq(planAssignments([], IDS, true), [], 'no rows → no assignment calls');
})();

// ----------------------------------------------------------------------

if (failures) {
  console.error('parent-photo-textbook: ' + failures + ' of ' + checks + ' checks failed');
  process.exit(1);
}
console.log('parent-photo-textbook: all ' + checks + ' checks passed');
