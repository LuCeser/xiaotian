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
 *   - Issue #81 P2: summarizeAssignScope derives the 默写安排 receipt
 *     from the POST-SAVE word entries and every eligibility source
 *     (participates, include_in_scope on any textbook, learned prefixes
 *     from the fresh book read); scheduleHint never answers a direct
 *     join with the 全部未勾选 contradiction.
 *   - PR #87 复审 P2-1: a word whose post-save word read or textbook
 *     read is missing counts 资格待核实 — never a payload/cached-copy
 *     verdict (the payload knows neither participates nor the word's
 *     other sources).
 *   - PR #87 复审 P2-2: 「恢复参与后即可安排」 is promised (pausedReady)
 *     only for paused words some source ALREADY qualifies.
 *   - The renderTextbookCommitResult wiring: the banner's 默写安排 lines
 *     must come from receipt.postSave/receipt.booksAfter (dropping those
 *     args at the call site turns the assertions red).
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
  summarizeAssignScope,
  scheduleHint,
  PhotoAddForm,
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
// summarizeAssignScope — the 默写安排 receipt verdict from the POST-SAVE
// entries and every eligibility source (Issue #81 P2: the old count rode
// this import's group payloads alone, so a participates=0 word could show
// 可安排默写 and an explicitly included but unlearned word could show
// 暂不自动安排). PR #87 复审 P2-1/P2-2: a fact the post-save reads cannot
// deliver is never guessed from the payloads or the cached books — the
// word counts 资格待核实 — and 恢复参与后即可安排 is promised only when a
// source already qualifies the paused word (pausedReady).
// ----------------------------------------------------------------------

// The receipt's book view: lessons [第1课,第5课,第7课], learned 2 (第1课
// and 第5课 are inside the learned prefix, 第7课 is not).
const SCOPE_BOOK = { id: 9, name: '书A', lessons: ['第1课', '第5课', '第7课'], learnedLessons: 2 };
const SCOPE_BOOKS_AFTER = [
  SCOPE_BOOK,
  // A second textbook whose FIRST lesson is learned (US-33's other
  // source: one learned assignment among several admits the word).
  { id: 2, name: '书B', lessons: ['B课', 'A课'], learnedLessons: 1 },
];
const scopeGroupResults = function (lesson, includeInScope, words) {
  return [{ group: { lesson: lesson, includeInScope: includeInScope, words: words }, ok: true }];
};
// Expected summarizeAssignScope shape with zeroed buckets — keys in the
// function's own return order so eq's JSON comparison stays exact.
const scopeExpect = function (overrides) {
  return Object.assign({
    schedulable: 0, joined: 0, heldUnlearned: 0, heldPending: 0, paused: 0,
    pausedReady: 0, unverified: 0, unlearnedNames: [],
  }, overrides || {});
};

(function () {
  // Post-save baseline: a word whose saved entry sits in the target
  // book's learned prefix is schedulable, not joined/held.
  eq(summarizeAssignScope(SCOPE_BOOK, scopeGroupResults('第5课', false, ['apple']), [
    { word: 'apple', participates: true, textbooks: [{ id: 9, name: '书A', lesson: '第5课', includeInScope: false }] },
  ], SCOPE_BOOKS_AFTER),
    scopeExpect({ schedulable: 1 }),
    'a saved entry inside the learned prefix counts 可安排默写');

  // P2 headline: an explicit includeInScope on an UNLEARNED lesson joins
  // the range (the pre-fix code filed it under 暂不自动安排).
  eq(summarizeAssignScope(SCOPE_BOOK, scopeGroupResults('第7课', true, ['lake']), [
    { word: 'lake', participates: true, textbooks: [{ id: 9, name: '书A', lesson: '第7课', includeInScope: true }] },
  ], SCOPE_BOOKS_AFTER),
    scopeExpect({ joined: 1 }),
    'an explicit include on an unlearned lesson counts 已加入范围');

  // All sources: the target book's lesson is unlearned, but the word's
  // entry on 书B sits inside 书B's learned prefix → schedulable.
  eq(summarizeAssignScope(SCOPE_BOOK, scopeGroupResults('第7课', false, ['river']), [
    {
      word: 'river', participates: true,
      textbooks: [
        { id: 9, name: '书A', lesson: '第7课', includeInScope: false },
        { id: 2, name: '书B', lesson: 'B课', includeInScope: false },
      ],
    },
  ], SCOPE_BOOKS_AFTER),
    scopeExpect({ schedulable: 1 }),
    'a learned entry on ANOTHER textbook admits the word');

  // participates composes with scope (AND): a moved-out word is paused,
  // never 可安排默写, even with a learned lesson.
  eq(summarizeAssignScope(SCOPE_BOOK, scopeGroupResults('第5课', false, ['sun']), [
    { word: 'sun', participates: false, textbooks: [{ id: 9, name: '书A', lesson: '第5课', includeInScope: false }] },
  ], SCOPE_BOOKS_AFTER),
    scopeExpect({ paused: 1, pausedReady: 1 }),
    'a participates=0 word counts 已移出课堂参与, not 可安排默写');

  // Fresh progress beats the client's stale book copy: the book object
  // still claims learned=2, the post-save read says 1 — 第5课 moved out
  // of the learned prefix, so the word is held, not schedulable.
  const staleBook = { id: 9, name: '书A', lessons: ['第1课', '第5课', '第7课'], learnedLessons: 2 };
  eq(summarizeAssignScope(staleBook, scopeGroupResults('第5课', false, ['moon']), [
    { word: 'moon', participates: true, textbooks: [{ id: 9, name: '书A', lesson: '第5课', includeInScope: false }] },
  ], [
    { id: 9, name: '书A', lessons: ['第1课', '第5课', '第7课'], learnedLessons: 1 },
  ]),
    scopeExpect({ heldUnlearned: 1, unlearnedNames: ['第5课'] }),
    'the fresh learned prefix decides, not the stale client book');

  // A saved empty lesson without an include stays 待确认.
  eq(summarizeAssignScope(SCOPE_BOOK, scopeGroupResults('', false, ['star']), [
    { word: 'star', participates: true, textbooks: [{ id: 9, name: '书A', lesson: '', includeInScope: false }] },
  ], SCOPE_BOOKS_AFTER),
    scopeExpect({ heldPending: 1 }),
    'an empty saved lesson without an include stays 课次待确认');

  // PR #87 复审 P2-1 — the word-read degraded path: the assign payload
  // CANNOT decide (it knows neither participates nor the word's other
  // sources). The review's repro: a moved-out LEARNED word must not show
  // 可安排默写 after a failed readback; an include payload must not claim
  // 已加入范围 (assign never restores participation); a payload hold can
  // miss another textbook's eligibility. Everything counts 待核实.
  eq(summarizeAssignScope(SCOPE_BOOK, scopeGroupResults('第5课', false, ['apple']), null, null),
    scopeExpect({ unverified: 1 }),
    'P2-1 word read failed: a learned-lesson payload never asserts 可安排默写');
  eq(summarizeAssignScope(SCOPE_BOOK, scopeGroupResults('第7课', true, ['lake']), null, null),
    scopeExpect({ unverified: 1 }),
    'P2-1 word read failed: an include payload never asserts 已加入范围');
  eq(summarizeAssignScope(SCOPE_BOOK, scopeGroupResults('', false, ['star']), null, null),
    scopeExpect({ unverified: 1 }),
    'P2-1 word read failed: an empty-lesson payload never asserts 待确认');

  // The word IS in the post-save read, but the read lacks it (a good
  // fetch that somehow does not carry the word) — same unknown, same
  // 待核实, never a payload verdict.
  eq(summarizeAssignScope(SCOPE_BOOK, scopeGroupResults('第5课', false, ['apple']), [
    { word: 'other', participates: true, textbooks: [] },
  ], SCOPE_BOOKS_AFTER),
    scopeExpect({ unverified: 1 }),
    'P2-1 a word missing from a good read is 待核实, not judged by payload');

  // PR #87 复审 P2-1 — the textbook-read degraded path: words are saved
  // but no fresh book prefix is known, so eligibility is 待核实 — the
  // client's cached 书A copy (learned 2) must not stand in for the read
  // and hand out 可安排默写.
  eq(summarizeAssignScope(SCOPE_BOOK, scopeGroupResults('第5课', false, ['apple']), [
    { word: 'apple', participates: true, textbooks: [{ id: 9, name: '书A', lesson: '第5课', includeInScope: false }] },
  ], null),
    scopeExpect({ unverified: 1 }),
    'P2-1 textbook read failed: 待核实, never the cached prefix\'s 可安排默写');

  // One source covered and unlearned, another on a textbook the read
  // missed: that missing source may admit the word (missed-other-
  // textbook eligibility), so it cannot be held either → 待核实.
  eq(summarizeAssignScope(SCOPE_BOOK, scopeGroupResults('第7课', false, ['river']), [
    {
      word: 'river', participates: true,
      textbooks: [
        { id: 9, name: '书A', lesson: '第7课', includeInScope: false },
        { id: 3, name: '书C', lesson: 'C课', includeInScope: false },
      ],
    },
  ], SCOPE_BOOKS_AFTER),
    scopeExpect({ unverified: 1 }),
    'P2-1 an attribution on an unread textbook is 待核实, never 暂不自动安排');

  // PR #87 复审 P2-2 — paused words: pausedReady separates 「恢复参与后
  // 即可安排」 (a source already qualifies) from the honest 「还需课次已学
  // 或加入默写范围」.
  eq(summarizeAssignScope(SCOPE_BOOK, scopeGroupResults('第7课', true, ['lake']), [
    { word: 'lake', participates: false, textbooks: [{ id: 9, name: '书A', lesson: '第7课', includeInScope: true }] },
  ], SCOPE_BOOKS_AFTER),
    scopeExpect({ paused: 1, pausedReady: 1 }),
    'P2-2 a paused word with an include is pausedReady (restore suffices)');
  eq(summarizeAssignScope(SCOPE_BOOK, scopeGroupResults('', false, ['star']), [
    { word: 'star', participates: false, textbooks: [] },
  ], SCOPE_BOOKS_AFTER),
    scopeExpect({ paused: 1, pausedReady: 1 }),
    'P2-2 an unattributed paused word is pausedReady (supplementary default)');
  eq(summarizeAssignScope(SCOPE_BOOK, scopeGroupResults('第7课', false, ['river']), [
    {
      word: 'river', participates: false,
      textbooks: [
        { id: 9, name: '书A', lesson: '第7课', includeInScope: false },
        { id: 2, name: '书B', lesson: 'B课', includeInScope: false },
      ],
    },
  ], SCOPE_BOOKS_AFTER),
    scopeExpect({ paused: 1, pausedReady: 1 }),
    'P2-2 a paused word with a learned entry on 书B is pausedReady');
  // The review's repro: moved out AND the only source lesson unlearned —
  // restoring participation alone still does not schedule the word.
  eq(summarizeAssignScope(SCOPE_BOOK, scopeGroupResults('第7课', false, ['lake']), [
    { word: 'lake', participates: false, textbooks: [{ id: 9, name: '书A', lesson: '第7课', includeInScope: false }] },
  ], SCOPE_BOOKS_AFTER),
    scopeExpect({ paused: 1, pausedReady: 0 }),
    'P2-2 a paused word whose only lesson is unlearned is NOT pausedReady');
  // A paused word with a source on an UNREAD textbook: that source may or
  // may not qualify it — the promise cannot be made.
  eq(summarizeAssignScope(SCOPE_BOOK, scopeGroupResults('第7课', false, ['river']), [
    {
      word: 'river', participates: false,
      textbooks: [{ id: 3, name: '书C', lesson: 'C课', includeInScope: false }],
    },
  ], SCOPE_BOOKS_AFTER),
    scopeExpect({ paused: 1, pausedReady: 0 }),
    'P2-2 a paused word qualified only by an unread textbook is NOT pausedReady');

  // Failed groups are never counted (they are 失败 under 保存结果).
  eq(summarizeAssignScope(SCOPE_BOOK, [
    { group: { lesson: '第5课', includeInScope: false, words: ['apple'] }, ok: false, error: '网络错误' },
  ], [
    { word: 'apple', participates: true, textbooks: [{ id: 9, name: '书A', lesson: '第5课', includeInScope: false }] },
  ], SCOPE_BOOKS_AFTER),
    scopeExpect(),
    'a failed assign group is never counted as scheduled');
})();

// ----------------------------------------------------------------------
// scheduleHint — the state line's ONE scheduling note (Issue #81 P2: with
// 待确认 words lifted by the 直接加入默写范围 checkbox, the line used to
// fall through to the 「全部未勾选……不会保存」 contradiction).
// ----------------------------------------------------------------------

(function () {
  // The P2 scenario: a textbook WITH progress, every checked word an
  // empty-lesson direct-join (includePending on) — a save WILL land them
  // in the scope, so the all-unchecked error must not fire.
  const joined = scheduleHint({
    mode: 9, book: SCOPE_BOOK, learned: 2,
    schedulable: 0, joined: 1, heldUnlearned: 0, heldPending: 0,
    unlearnedNames: [], withinNames: [],
  }, true);
  assert(joined.tone === 'good', 'the direct-join hint is good, not an error');
  assert(joined.text.indexOf('全部未勾选') < 0,
    'the direct-join hint never claims 全部未勾选');
  assert(joined.text.indexOf('直接加入默写范围') >= 0 && joined.text.indexOf('1 词') >= 0,
    'the direct-join hint names the joined count');

  // Regression guard: zero counts with word rows really are all
  // unchecked — the honest error keeps firing.
  const unchecked = scheduleHint({
    mode: 9, book: SCOPE_BOOK, learned: 2,
    schedulable: 0, joined: 0, heldUnlearned: 0, heldPending: 0,
    unlearnedNames: [], withinNames: [],
  }, true);
  assert(unchecked.text.indexOf('全部未勾选') >= 0,
    'zero counts with word rows still report 全部未勾选');

  // The pre-existing verdicts keep their exact sentences.
  eq(scheduleHint({ mode: 'none' }, false),
    { tone: 'good', text: '补充词汇保存后默认加入默写范围。' },
    'supplementary vocabulary keeps its note');
  eq(scheduleHint({
    mode: 9, book: SCOPE_BOOK, learned: 2,
    schedulable: 3, heldUnlearned: 0, heldPending: 0,
    withinNames: ['第5课'], unlearnedNames: [],
  }, true),
    { tone: 'good', text: '课次「第5课」已学到，保存后即可安排默写，无需再确认进度。' },
    'a fully schedulable batch keeps the no-re-ask sentence');
  eq(scheduleHint({
    mode: 9, book: SCOPE_BOOK, learned: 1,
    schedulable: 0, joined: 0, heldUnlearned: 2, heldPending: 0,
    withinNames: [], unlearnedNames: ['第5课'],
  }, true),
    { tone: 'warn', text: '课次「第5课」尚未学到：保存后这部分词暂不自动安排默写，进度不受影响。' },
    'an unlearned batch keeps the warn sentence');
})();

// ----------------------------------------------------------------------

if (failures) {
  console.error('parent-photo-textbook: ' + failures + ' of ' + checks + ' checks failed');
  process.exit(1);
}
console.log('parent-photo-textbook: all ' + checks + ' checks passed');
