/**
 * Node unit tests for the Issue #5 + #6 photo-import textbook pure
 * functions (window.* exports of parent-photo.js — window.parseBatch
 * precedent).
 *
 * Covered contracts:
 *   - classifyPhotoEntries carries each entry's lesson into the row (the
 *     Go pipeline already applied page-level inheritance).
 *   - matchTextbookForMeta preselects an existing textbook only when the
 *     recognized meta ACTUALLY matches it — never a guess (AC2's
 *     不编造 rule starts at the preselect).
 *   - planAssignments groups the committable rows into assign calls by
 *     (lesson, includeInScope): checked new/exists rows only — conflict
 *     and invalid rows never reach a textbook.
 *   - Issue #6: a conflict row the parent RESOLVED (保留/采用) joins the
 *     assignment groups; an unresolved one never does (AC-3).
 *   - Issue #6: an existing include_in_scope=1 on the TARGET textbook
 *     survives re-import (sticky include — AC-4's import-never-reopens
 *     mirror: import never silently undoes an explicit parent choice).
 *   - Issue #6: buildTextbookReceipt buckets every row into exactly
 *     新增/合并/待处理差异/失败 (+未核对/未提交 honesty buckets) and the
 *     summary counts are the tally of the item list (AC-6).
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
  distinctLessons,
  distinctRowLessons,
  isCommittable,
  planAssignments,
  buildTextbookReceipt,
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
// distinctLessons / distinctRowLessons — the append-shape the commit path
// builds a book's next lesson list from, and the preview-row shape. The
// two SHAPES must never be confused (a regression here once emptied a
// textbook's lesson list).
// ----------------------------------------------------------------------

(function () {
  // Plain string list: the book's lessons + append candidates.
  eq(distinctLessons(['第1课', ' 第2课 ', '第1课', '', '第3课', undefined]),
    ['第1课', '第2课', '第3课'], 'distinctLessons trims, drops empties, keeps order');
  eq(distinctLessons(['第1课'].concat(['第2课'])), ['第1课', '第2课'],
    'distinctLessons over a book-list + missing concat keeps both');

  // Row list: preview rows carry {lesson}.
  eq(distinctRowLessons([
    { lesson: '第1课' }, { lesson: '第1课' }, { lesson: '' }, { lesson: ' 第2课 ' }, {},
  ]), ['第1课', '第2课'], 'distinctRowLessons reads row.lesson, dedupes in order');

  // The shapes must never cross-wire: strings into the row variant come
  // back EMPTY (no .lesson to read) — pinned so the mistake is loud.
  eq(distinctRowLessons(['第1课', '第2课']), [],
    'distinctRowLessons over bare strings is empty (shape guard)');
})();

// ----------------------------------------------------------------------
// isCommittable — the conflict-resolution gate (Issue #6 AC-3)
// ----------------------------------------------------------------------

(function () {
  const conflict = { status: 'conflict', checked: false, resolution: '' };
  assert(!isCommittable(conflict, true), 'an unresolved conflict is never committable');
  assert(isCommittable({ status: 'conflict', resolution: 'keep' }, true),
    'a conflict resolved as 保留词库释义 becomes committable');
  assert(isCommittable({ status: 'conflict', resolution: 'adopt' }, true),
    'a conflict resolved as 采用照片释义 becomes committable');
  assert(!isCommittable({ status: 'conflict', resolution: 'adopt' }, false),
    'conflict resolution only applies in textbook mode');
})();

// ----------------------------------------------------------------------
// planAssignments — snapshot rows + target book (Issue #6 signature)
// ----------------------------------------------------------------------

const ROWS = [
  { index: 0, word: 'apple', meaning: '苹果', lesson: '第5课', status: 'new', checked: true },
  { index: 1, word: 'river', meaning: '河流', lesson: '第5课', status: 'new', checked: true },
  { index: 2, word: 'sun', meaning: '太阳', lesson: '', status: 'new', checked: true },
  { index: 3, word: 'moon', meaning: '月亮', lesson: '第6课', status: 'new', checked: false },
  { index: 4, word: 'lake', meaning: '湖', lesson: '第7课', status: 'exists', checked: true },
  { index: 5, word: 'star', meaning: '星星', lesson: '第7课', status: 'conflict', checked: false, resolution: '' },
  { index: 6, word: 'dust', meaning: '', lesson: '第7课', status: 'invalid', reason: 'empty-word', checked: false },
  { index: 7, word: 'cloud', meaning: '云', lesson: '第5课', status: 'exists', checked: false },
];

// The post-commit GET /words snapshot shape: id, word and the attribution
// projection (the sticky-include source).
const SNAP = [
  { id: 11, word: 'apple', textbooks: [] },
  { id: 12, word: 'river', textbooks: [] },
  { id: 13, word: 'sun', textbooks: [] },
  { id: 14, word: 'moon', textbooks: [] },
  { id: 15, word: 'lake', textbooks: [{ id: 9, name: '书A', lesson: '第7课', includeInScope: false }] },
  { id: 16, word: 'star', textbooks: [{ id: 9, name: '书A', lesson: '第7课', includeInScope: false }] },
  { id: 17, word: 'cloud', textbooks: [] },
];

const BOOK = 9;

(function () {
  const groups = planAssignments(ROWS, SNAP, BOOK, false);
  // Groups keyed by lesson in first-appearance order; unchecked/conflict/
  // invalid rows never assigned; exists rows DO get their lesson.
  eq(groups.map(function (g) { return g.lesson; }), ['第5课', '', '第7课'],
    'groups appear in first-appearance lesson order');
  eq(groups[0].cardIds, [11, 12], 'checked new rows with the lesson group together');
  eq(groups[0].words, ['apple', 'river'], 'group carries its words for the receipt');
  eq(groups[1].cardIds, [13], 'an empty lesson forms the 待确认 group');
  eq(groups[2].cardIds, [15], 'an exists row is assignable, unresolved conflict/invalid/unchecked are not');

  // includePending lifts ONLY the 待确认 group into scope.
  const withInclude = planAssignments(ROWS, SNAP, BOOK, true);
  const pending = withInclude.filter(function (g) { return g.lesson === ''; })[0];
  eq(pending.includeInScope, true, 'includePending sets includeInScope on the 待确认 group');
  const lessonGroups = withInclude.filter(function (g) { return g.lesson !== ''; });
  assert(lessonGroups.every(function (g) { return g.includeInScope === false; }),
    'rows with a real lesson never ride the include override');

  // A word missing from the snapshot (id unknown) is skipped, not guessed.
  const partial = planAssignments([
    { index: 0, word: 'ghost', meaning: '', lesson: '第1课', status: 'new', checked: true },
  ], SNAP, BOOK, false);
  eq(partial, [], 'a word without a resolved id forms no group');

  // Nothing committable → no calls at all.
  eq(planAssignments([], SNAP, BOOK, true), [], 'no rows → no assignment calls');
})();

// ----------------------------------------------------------------------
// planAssignments — resolved conflicts join the merge (Issue #6 AC-3)
// ----------------------------------------------------------------------

(function () {
  // Resolving a conflict enables (and default-checks) the row — the UI
  // contract the commit rides on.
  const rows = ROWS.map(function (r) {
    return r.word === 'star'
      ? Object.assign({}, r, { resolution: 'adopt', checked: true })
      : r;
  });
  const groups = planAssignments(rows, SNAP, BOOK, false);
  const lesson7 = groups.filter(function (g) { return g.lesson === '第7课'; })[0];
  eq(lesson7.cardIds, [15, 16], 'a resolved conflict row merges its source with exists rows');
  eq(lesson7.words, ['lake', 'star'], 'the group words carry the resolved conflict');
})();

// ----------------------------------------------------------------------
// planAssignments — sticky include on the TARGET book (Issue #6 AC-4)
// ----------------------------------------------------------------------

(function () {
  // lake's assignment on book 9 already carries the parent's explicit
  // include_in_scope=1; a re-import without the 待确认 checkbox must not
  // silently clear it.
  const snapIncluded = SNAP.map(function (w) {
    return w.word === 'lake'
      ? { id: w.id, word: w.word, textbooks: [{ id: 9, name: '书A', lesson: '第7课', includeInScope: true }] }
      : w;
  });
  const groups = planAssignments(ROWS, snapIncluded, BOOK, false);
  const lesson7 = groups.filter(function (g) { return g.lesson === '第7课'; })[0];
  eq(lesson7.includeInScope, true, 'an existing explicit include survives re-import');

  // The same include on a DIFFERENT book is not this import's business.
  const otherBook = planAssignments(ROWS, snapIncluded, 8, false);
  const other7 = otherBook.filter(function (g) { return g.lesson === '第7课'; })[0];
  eq(other7.includeInScope, false, 'another book\'s include does not leak into this import');

  // Sticky include also applies to a resolved conflict row.
  const resolvedRows = ROWS.map(function (r) {
    return r.word === 'star'
      ? Object.assign({}, r, { resolution: 'keep', checked: true })
      : r;
  });
  const starIncluded = SNAP.map(function (w) {
    return w.word === 'star'
      ? { id: w.id, word: w.word, textbooks: [{ id: 9, name: '书A', lesson: '第7课', includeInScope: true }] }
      : w;
  });
  const viaConflict = planAssignments(resolvedRows, starIncluded, BOOK, false);
  const conflictGroup = viaConflict.filter(function (g) {
    return g.lesson === '第7课' && g.words.indexOf('star') >= 0;
  })[0];
  eq(conflictGroup.includeInScope, true, 'a resolved conflict keeps an existing explicit include');
  const lakeGroup = viaConflict.filter(function (g) {
    return g.lesson === '第7课' && g.words.indexOf('lake') >= 0;
  })[0];
  eq(lakeGroup.includeInScope, false, 'the sticky flag splits groups only for the words that carry it');
})();

// ----------------------------------------------------------------------
// buildTextbookReceipt — the four per-item categories (Issue #6 AC-6)
// ----------------------------------------------------------------------

/** Category of the first item matching word, for compact assertions. */
function catOf(receipt, word) {
  const item = receipt.items.filter(function (i) { return i.word === word; })[0];
  return item ? item.category : '(missing)';
}

(function () {
  // The plain re-import: everything already in the library, same meanings.
  const reRows = [
    { index: 0, word: 'apple', meaning: '苹果', lesson: '第5课', status: 'exists', checked: true },
    { index: 1, word: 'river', meaning: '河流', lesson: '第5课', status: 'new', checked: true },
    { index: 2, word: 'star', meaning: '新星', lesson: '第5课', status: 'conflict', checked: false, resolution: '' },
    { index: 3, word: 'dust', meaning: '', lesson: '第5课', status: 'invalid', reason: 'empty-word', checked: false },
  ];
  const groups = [{ lesson: '第5课', includeInScope: false, cardIds: [11, 12], words: ['apple', 'river'] }];
  const receipt = buildTextbookReceipt({
    rows: reRows,
    addRows: [reRows[1]],
    addOutcome: { added: 1, skipped: 0 },
    fresh: SNAP,
    editResults: [],
    groups: groups,
    groupResults: [{ group: groups[0], ok: true, result: { updated: 2 } }],
    unassigned: [],
  });
  eq(catOf(receipt, 'river'), 'added', 'a verified new word reports 新增');
  eq(catOf(receipt, 'apple'), 'merged', 'an exists word that merged its source reports 合并');
  eq(catOf(receipt, 'star'), 'pendingDiff', 'an unresolved conflict reports 待处理差异');
  eq(catOf(receipt, 'dust'), 'excluded', 'an invalid row reports 未提交, not a failure');
  eq(receipt.summary, { added: 1, merged: 1, pendingDiff: 1, failed: 0, unverified: 0, excluded: 1 },
    'the summary is the tally of the item list');

  // Resolution 采用照片释义: the edit landed, the source merged.
  const adoptRows = [
    { index: 0, word: 'star', meaning: '新星', lesson: '第5课', status: 'conflict', checked: true, resolution: 'adopt' },
  ];
  const adoptGroups = [{ lesson: '第5课', includeInScope: false, cardIds: [16], words: ['star'] }];
  const adoptReceipt = buildTextbookReceipt({
    rows: adoptRows,
    addRows: [],
    addOutcome: { added: 0, skipped: 0 },
    fresh: SNAP,
    editResults: [{ word: 'star', ok: true }],
    groups: adoptGroups,
    groupResults: [{ group: adoptGroups[0], ok: true, result: { updated: 1 } }],
    unassigned: [],
  });
  eq(catOf(adoptReceipt, 'star'), 'merged', 'an adopted conflict reports 合并');
  const adoptItem = adoptReceipt.items[0];
  assert(/采用照片释义|采用/.test(adoptItem.note || ''), 'the merged note names the adoption');

  // Resolution 保留词库释义: no edit call, source merged.
  const keepReceipt = buildTextbookReceipt({
    rows: [{ index: 0, word: 'star', meaning: '新星', lesson: '第5课', status: 'conflict', checked: true, resolution: 'keep' }],
    addRows: [],
    addOutcome: { added: 0, skipped: 0 },
    fresh: SNAP,
    editResults: [],
    groups: adoptGroups,
    groupResults: [{ group: adoptGroups[0], ok: true, result: { updated: 1 } }],
    unassigned: [],
  });
  eq(catOf(keepReceipt, 'star'), 'merged', 'a kept-original conflict reports 合并');
  assert(/保留/.test(keepReceipt.items[0].note || ''), 'the merged note names the keep');

  // The adopt edit failed → the word is a failure, not a merge.
  const editFail = buildTextbookReceipt({
    rows: adoptRows,
    addRows: [],
    addOutcome: { added: 0, skipped: 0 },
    fresh: SNAP,
    editResults: [{ word: 'star', ok: false, error: '词条不存在' }],
    groups: [],
    groupResults: [],
    unassigned: [],
  });
  eq(catOf(editFail, 'star'), 'failed', 'a failed adoption edit reports 失败');
  assert(/释义更新失败/.test(editFail.items[0].note || ''), 'the failure note names the edit step');

  // An assignment group failure fails exactly its words, with the save
  // still acknowledged in the note.
  const assignFailRows = [
    { index: 0, word: 'river', meaning: '河流', lesson: '第5课', status: 'new', checked: true },
    { index: 1, word: 'apple', meaning: '苹果', lesson: '第5课', status: 'new', checked: true },
  ];
  const failGroups = [{ lesson: '第5课', includeInScope: false, cardIds: [11, 12], words: ['apple', 'river'] }];
  const assignFail = buildTextbookReceipt({
    rows: assignFailRows,
    addRows: assignFailRows,
    addOutcome: { added: 2, skipped: 0 },
    fresh: SNAP,
    editResults: [],
    groups: failGroups,
    groupResults: [{ group: failGroups[0], ok: false, error: '网络错误' }],
    unassigned: [],
  });
  eq(catOf(assignFail, 'apple'), 'failed', 'an assign-failed word reports 失败');
  eq(catOf(assignFail, 'river'), 'failed', 'every word of the failed group reports 失败');
  assert(/已保存/.test(assignFail.items[0].note || ''), 'the note admits the word was saved');

  // A raced duplicate (someone else added between preview and commit).
  const racedReceipt = buildTextbookReceipt({
    rows: [{ index: 0, word: 'river', meaning: '河流', lesson: '第5课', status: 'new', checked: true }],
    addRows: [{ word: 'river', meaning: '河流', lesson: '第5课', status: 'new', checked: true }],
    addOutcome: { added: 0, skipped: 1 },
    fresh: [{ id: 12, word: 'river', meaning: '河流', textbooks: [] }],
    editResults: [],
    groups: [{ lesson: '第5课', includeInScope: false, cardIds: [12], words: ['river'] }],
    groupResults: [{ group: { lesson: '第5课', cardIds: [12], words: ['river'] }, ok: true, result: { updated: 1 } }],
    unassigned: [],
  });
  eq(catOf(racedReceipt, 'river'), 'added', 'raced to an identical entry reports 新增');

  const racedDiff = buildTextbookReceipt({
    rows: [{ index: 0, word: 'river', meaning: '河流', lesson: '第5课', status: 'new', checked: true }],
    addRows: [{ word: 'river', meaning: '河流', lesson: '第5课', status: 'new', checked: true }],
    addOutcome: { added: 0, skipped: 1 },
    fresh: [{ id: 12, word: 'river', meaning: '江', textbooks: [] }],
    editResults: [],
    groups: [{ lesson: '第5课', includeInScope: false, cardIds: [12], words: ['river'] }],
    groupResults: [{ group: { lesson: '第5课', cardIds: [12], words: ['river'] }, ok: true, result: { updated: 1 } }],
    unassigned: [],
  });
  eq(catOf(racedDiff, 'river'), 'merged', 'raced to a different meaning reports 合并 (original kept)');

  // Snapshot unavailable + skips: unverified, never guessed.
  const unverified = buildTextbookReceipt({
    rows: [{ index: 0, word: 'river', meaning: '河流', lesson: '第5课', status: 'new', checked: true }],
    addRows: [{ word: 'river', meaning: '河流', lesson: '第5课', status: 'new', checked: true }],
    addOutcome: { added: 0, skipped: 1 },
    fresh: null,
    editResults: [],
    groups: [],
    groupResults: [],
    unassigned: [],
  });
  eq(catOf(unverified, 'river'), 'unverified', 'an unverifiable raced word reports 未核对');
})();

// ----------------------------------------------------------------------

if (failures) {
  console.error('parent-photo-textbook: ' + failures + ' of ' + checks + ' checks failed');
  process.exit(1);
}
console.log('parent-photo-textbook: all ' + checks + ' checks passed');
