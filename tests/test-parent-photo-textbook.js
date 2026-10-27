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
 *   - The real fetchWords → addAndAssign →
 *     renderTextbookCommitResult flow: the banner's 默写安排 lines must
 *     come from the post-save reads, and a pre-assignment
 *     textbooks:null must never emit an assign payload that can clear an
 *     existing explicit include.
 *   - PR #88 复盘路径 B: 合并类文案只认本次成功的 assign 响应（矩阵：
 *     {exists, keep, adopt, added, raced} × {无 assign, 成功, 失败}）；
 *     暂停词的来源教材不在保存后的教材读里时资格待核实，不写
 *     「恢复参与后还需课次已学或加入默写范围」。
 */
'use strict';

const path = require('node:path');

// Minimal window shim, then load parent-photo.js the way
// tests/test-parent-photo-classify.js does.
global.window = global;
require(path.resolve(__dirname, '..', 'pkg', 'web', 'static', 'js', 'parent-pdf-core.js'));
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

  // A resolved card whose attribution projection failed is equally
  // unsafe to assign: false could clear an existing explicit include,
  // while true could invent one. Wait for a verified source list.
  const unknownAttribution = planAssignments([
    { index: 0, word: 'lake', meaning: '湖', lesson: '第7课', status: 'exists', checked: true },
  ], [
    { id: 15, word: 'lake', textbooks: null },
  ], BOOK, false);
  eq(unknownAttribution, [],
    'a word with unknown attribution forms no assignment group');

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
// PR #88 复盘路径 B — 合并类文案（已合并来源/仅合并来源/仅指定课次）只认
// 本次成功的 assign 响应（ADR-0003：回执汇总用服务器实际响应拼装，不走
// else 兜底）。矩阵：每个会写出合并类文案的场景 × {无 assign 结果, assign
// 成功, assign 失败}。「无 assign 结果」列按真实管线构造：GET /words 答
// textbooks:null → planAssignments 跳过该词 → unassigned 携带它 —— 不是
// 清空 groupResults 却保留 unassigned:[] 的手工夹具（那正是三轮复审的
// 假绿来源）。
// ----------------------------------------------------------------------

(function () {
  const noteOf = function (receipt, word) {
    const item = receipt.items.filter(function (i) { return i.word === word; })[0];
    return item ? (item.note || '') : '(missing)';
  };
  const mergeWording = /已合并来源|仅合并来源|仅指定课次/;

  // 每个场景只变 assign 一条腿；freshKnown/freshUnknown 与 groups 都是该
  // 场景真实会出现的形状（只有 textbooks:null / 无 card id 才没有 assign）。
  const columns = function (s) {
    const build = function (kind) {
      return buildTextbookReceipt({
        rows: s.rows,
        addRows: s.addRows,
        addOutcome: s.addOutcome,
        fresh: kind === 'none' ? s.freshUnknown : s.freshKnown,
        editResults: s.editResults,
        groups: kind === 'none' ? [] : s.groups,
        groupResults: kind === 'ok'
          ? [{ group: s.groups[0], ok: true, result: { updated: 1 } }]
          : kind === 'fail'
            ? [{ group: s.groups[0], ok: false, error: '网络错误' }]
            : [],
        unassigned: kind === 'none' ? [s.rows[0].word] : [],
      });
    };
    return { ok: build('ok'), fail: build('fail'), none: build('none') };
  };

  const scenarios = [
    {
      name: 'exists',
      rows: [{ index: 0, word: 'apple', meaning: '苹果', lesson: '第5课', status: 'exists', checked: true }],
      addRows: [],
      addOutcome: { added: 0, skipped: 0 },
      editResults: [],
      freshKnown: [{ id: 11, word: 'apple', meaning: '苹果', textbooks: [] }],
      freshUnknown: [{ id: 11, word: 'apple', meaning: '苹果', textbooks: null }],
      groups: [{ lesson: '第5课', includeInScope: false, cardIds: [11], words: ['apple'] }],
      okCategory: 'merged',
      okNote: '词库已有，仅指定课次',
      failNote: '课次「第5课」指定失败：网络错误——可在词库中重试',
      noneNote: '词库读取失败，未能指定课次——可在词库中补指定',
    },
    {
      name: 'keep',
      rows: [{ index: 0, word: 'star', meaning: '新星', lesson: '第5课', status: 'conflict', checked: true, resolution: 'keep' }],
      addRows: [],
      addOutcome: { added: 0, skipped: 0 },
      editResults: [],
      freshKnown: [{ id: 16, word: 'star', meaning: '新星', textbooks: [] }],
      freshUnknown: [{ id: 16, word: 'star', meaning: '新星', textbooks: null }],
      groups: [{ lesson: '第5课', includeInScope: false, cardIds: [16], words: ['star'] }],
      okCategory: 'merged',
      okNote: '保留词库释义，仅合并来源',
      failNote: '课次「第5课」指定失败：网络错误——可在词库中重试',
      noneNote: '词库读取失败，未能指定课次——可在词库中补指定',
    },
    {
      name: 'adopt',
      rows: [{ index: 0, word: 'star', meaning: '新星', lesson: '第5课', status: 'conflict', checked: true, resolution: 'adopt' }],
      addRows: [],
      addOutcome: { added: 0, skipped: 0 },
      editResults: [{ word: 'star', ok: true }],
      freshKnown: [{ id: 16, word: 'star', meaning: '新星', textbooks: [] }],
      freshUnknown: [{ id: 16, word: 'star', meaning: '新星', textbooks: null }],
      groups: [{ lesson: '第5课', includeInScope: false, cardIds: [16], words: ['star'] }],
      okCategory: 'merged',
      okNote: '采用照片释义更新，已合并来源',
      failNote: '释义已按照片更新；课次「第5课」指定失败：网络错误——可在词库中重试',
      noneNote: '释义已按照片更新；词库读取失败，未能指定课次——可在词库中补指定',
    },
    {
      name: 'added',
      rows: [{ index: 0, word: 'river', meaning: '河流', lesson: '第5课', status: 'new', checked: true }],
      addRows: [{ index: 0, word: 'river', meaning: '河流', lesson: '第5课', status: 'new', checked: true }],
      addOutcome: { added: 0, skipped: 1 },
      editResults: [],
      freshKnown: [{ id: 12, word: 'river', meaning: '河流', textbooks: [] }],
      freshUnknown: [{ id: 12, word: 'river', meaning: '河流', textbooks: null }],
      groups: [{ lesson: '第5课', includeInScope: false, cardIds: [12], words: ['river'] }],
      okCategory: 'added',
      okNote: '已添加并指定课次',
      failNote: '词条已保存；课次「第5课」指定失败：网络错误——可在词库中重试',
      noneNote: '词条已保存，但词库读取失败，未能指定课次——可在词库中补指定',
    },
    {
      name: 'raced',
      rows: [{ index: 0, word: 'river', meaning: '河流', lesson: '第5课', status: 'new', checked: true }],
      addRows: [{ index: 0, word: 'river', meaning: '河流', lesson: '第5课', status: 'new', checked: true }],
      addOutcome: { added: 0, skipped: 1 },
      editResults: [],
      freshKnown: [{ id: 12, word: 'river', meaning: '江', textbooks: [] }],
      freshUnknown: [{ id: 12, word: 'river', meaning: '江', textbooks: null }],
      groups: [{ lesson: '第5课', includeInScope: false, cardIds: [12], words: ['river'] }],
      okCategory: 'merged',
      okNote: '并发已存在不同释义，保留原释义，仅合并来源',
      failNote: '并发已存在不同释义，保留原释义；课次「第5课」指定失败：网络错误——可在词库中重试',
      noneNote: '并发已存在不同释义，保留原释义；词库读取失败，未能指定课次——可在词库中补指定',
    },
  ];

  scenarios.forEach(function (s) {
    const cols = columns(s);
    const word = s.rows[0].word;

    // assign 成功列：快乐路径不得被收窄误伤，类别与文案原样保留。
    eq(catOf(cols.ok, word), s.okCategory, '矩阵 ' + s.name + ' + assign 成功：类别 ' + s.okCategory);
    eq(noteOf(cols.ok, word), s.okNote, '矩阵 ' + s.name + ' + assign 成功：文案保持');
    // assign 失败列：失败从来是失败，钉住文案与「不写合并」。
    eq(catOf(cols.fail, word), 'failed', '矩阵 ' + s.name + ' + assign 失败：失败');
    eq(noteOf(cols.fail, word), s.failNote, '矩阵 ' + s.name + ' + assign 失败：文案保持');
    assert(!mergeWording.test(noteOf(cols.fail, word)), '矩阵 ' + s.name + ' + assign 失败：无合并类文案');
    // 无 assign 结果列（textbooks:null）：失败，保留已落库的部分，不写合并。
    eq(catOf(cols.none, word), 'failed', '矩阵 ' + s.name + ' + 无 assign：失败，不是合并');
    eq(noteOf(cols.none, word), s.noneNote, '矩阵 ' + s.name + ' + 无 assign：文案点名已保存部分 + 未能指定课次');
    assert(!mergeWording.test(noteOf(cols.none, word)), '矩阵 ' + s.name + ' + 无 assign：无合并类文案');
    eq(cols.none.summary, { added: 0, merged: 0, pendingDiff: 0, failed: 1, unverified: 0, excluded: 0 },
      '矩阵 ' + s.name + ' + 无 assign：单条批次不得汇总成成功');
  });
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

  // PR #88 P1 — the REAL degraded readback, not a hand-built one: the
  // post-save GET /words answers 200 with its attribution projection
  // failed server-side, so each word carries textbooks:null (the Go
  // handler's failure branch, pinned in textbooks_test.go). The pre-fix
  // client read that null as 「无归属」 and guessed the supplementary
  // default / paused-ready promises; now it is 待核实.
  eq(summarizeAssignScope(SCOPE_BOOK, scopeGroupResults('第7课', false, ['apple']), [
    { word: 'apple', participates: true, textbooks: null },
  ], SCOPE_BOOKS_AFTER),
    scopeExpect({ unverified: 1 }),
    'P1 attribution degraded (textbooks null): never guessed 已加入范围');
  eq(summarizeAssignScope(SCOPE_BOOK, scopeGroupResults('', false, ['star']), [
    { word: 'star', participates: true, textbooks: null },
  ], SCOPE_BOOKS_AFTER),
    scopeExpect({ unverified: 1 }),
    'P1 attribution degraded: the lesson-less supplementary default never fires');
  eq(summarizeAssignScope(SCOPE_BOOK, scopeGroupResults('第7课', false, ['lake']), [
    { word: 'lake', participates: false, textbooks: null },
  ], SCOPE_BOOKS_AFTER),
    scopeExpect({ unverified: 1 }),
    'P1 attribution degraded: a paused word never gets the pausedReady guess');
  // The other side of the distinction: a SUCCEEDED projection that
  // verified no attribution ([] , not null) keeps its meaning.
  eq(summarizeAssignScope(SCOPE_BOOK, scopeGroupResults('', false, ['star']), [
    { word: 'star', participates: true, textbooks: [] },
  ], SCOPE_BOOKS_AFTER),
    scopeExpect({ joined: 1 }),
    'a VERIFIED empty attribution ([]) still joins as 补充词汇');

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
  // PR #88 复盘：暂停词的来源教材不在保存后的教材读里（书C 未读）——那个
  // 来源可能已经满足资格，「恢复参与后还需课次已学」既承诺不了也否定不了。
  // 资格待核实，而不是确定的 pausedReady=0（旧期望把错误句子写成了规格）。
  eq(summarizeAssignScope(SCOPE_BOOK, scopeGroupResults('第7课', false, ['river']), [
    {
      word: 'river', participates: false,
      textbooks: [{ id: 3, name: '书C', lesson: 'C课', includeInScope: false }],
    },
  ], SCOPE_BOOKS_AFTER),
    scopeExpect({ unverified: 1 }),
    'PR88 a paused word qualified only by an unread textbook is 资格待核实');
  // 同一规则的一半覆盖一半未读：未读的那个来源可能已经满足资格。
  eq(summarizeAssignScope(SCOPE_BOOK, scopeGroupResults('第7课', false, ['lake']), [
    {
      word: 'lake', participates: false,
      textbooks: [
        { id: 9, name: '书A', lesson: '第7课', includeInScope: false },
        { id: 3, name: '书C', lesson: 'C课', includeInScope: false },
      ],
    },
  ], SCOPE_BOOKS_AFTER),
    scopeExpect({ unverified: 1 }),
    'PR88 a paused word with one covered-unlearned and one unread source is 资格待核实');

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
// renderTextbookCommitResult — the WIRING regression (PR #87 复审): the
// banner's 默写安排 lines must come from receipt.postSave and
// receipt.booksAfter (the post-save reads). The review found that deleting
// those two args at the summarizeAssignScope call site kept every check
// green — the assertions below turn that removal red, and pin the P2-1
// degraded banner and the P2-2 paused next-step at the render level.
// ----------------------------------------------------------------------

// Minimal DOM shim (tests/test-parent-category-error.js precedent) —
// the renderer only creates nodes, sets className/textContent and
// appends. `document` is assigned late: parent-photo.js looks it up at
// call time, and the pure sections above never touch it.
function makeScopeNode(tag) {
  return {
    tagName: tag,
    className: '',
    textContent: '',
    children: [],
    attrs: {},
    setAttribute: function (name, value) {
      this.attrs[name] = value;
    },
    appendChild: function (child) {
      this.children.push(child);
      return child;
    },
  };
}

const scopeTextOf = function (node) {
  const parts = [node.textContent || ''];
  (node.children || []).forEach(function (child) {
    parts.push(scopeTextOf(child));
  });
  return parts.join('\n');
};

const renderScopeBanner = function (overrides) {
  global.document = { createElement: makeScopeNode };
  const form = Object.create(PhotoAddForm.prototype);
  form.rows = [{ index: 0, word: 'apple', meaning: '苹果', lesson: '第5课', status: 'exists', checked: true }];
  form.banner = makeScopeNode('div');
  const receipt = Object.assign({
    book: SCOPE_BOOK,
    submittedRows: [],
    addOutcome: { added: 0, skipped: 0 },
    fresh: [],
    editResults: [],
    groups: [],
    groupResults: scopeGroupResults('第5课', false, ['apple']),
    unassigned: [],
    postSave: null,
    booksAfter: null,
  }, overrides);
  form.renderTextbookCommitResult(receipt);
  return scopeTextOf(form.banner);
};

(function () {
  // The wiring: the verdict text must track receipt.postSave/booksAfter.
  // Deleting those args at the summarizeAssignScope call site drops the
  // word to 资格待核实 and every assertion in this first case fails.
  const postSaveWords = [
    { word: 'apple', participates: true, textbooks: [{ id: 9, name: '书A', lesson: '第5课', includeInScope: false }] },
  ];
  const good = renderScopeBanner({ postSave: postSaveWords, booksAfter: SCOPE_BOOKS_AFTER });
  assert(good.indexOf('可安排默写 1 词（课次已学）') >= 0,
    'the banner schedules from receipt.postSave/booksAfter (wiring guard)');
  assert(good.indexOf('资格待核实') < 0,
    'a fully read-back save never claims 资格待核实');
  assert(good.indexOf('合并 1 个') >= 0,
    'the real 保存结果 stays beside the 默写安排 block');

  // P2-1 at the render level: the word read failed — the payload's
  // learned lesson must not become 可安排默写; the honest banner says
  // 资格待核实 and still reports the real save result above it.
  const degradedWords = renderScopeBanner({ postSave: null, booksAfter: SCOPE_BOOKS_AFTER });
  assert(degradedWords.indexOf('资格待核实 1 词') >= 0,
    'a failed word read renders 资格待核实');
  assert(degradedWords.indexOf('可安排默写') < 0,
    'a failed word read never renders 可安排默写');
  assert(degradedWords.indexOf('合并 1 个') >= 0,
    'P2-1: 资格待核实 replaces only the 默写安排 verdict, not the save result');

  // P2-1's other half at the render level: words read back, textbooks
  // not — eligibility is 待核实, not the cached prefix's verdict.
  const degradedBooks = renderScopeBanner({ postSave: postSaveWords, booksAfter: null });
  assert(degradedBooks.indexOf('资格待核实 1 词') >= 0,
    'a failed textbook read renders 资格待核实');
  assert(degradedBooks.indexOf('可安排默写') < 0,
    'a failed textbook read never renders 可安排默写');

  // P2-2 at the render level: the next step matches the real eligibility
  // condition of the paused words.
  const pausedRows = [{ index: 0, word: 'lake', meaning: '湖', lesson: '第7课', status: 'exists', checked: true }];
  const pausedSaved = [
    { word: 'lake', participates: false, textbooks: [{ id: 9, name: '书A', lesson: '第7课', includeInScope: false }] },
  ];
  const pausedNotReady = renderScopeBanner({
    rows: pausedRows,
    groupResults: scopeGroupResults('第7课', false, ['lake']),
    postSave: pausedSaved,
    booksAfter: SCOPE_BOOKS_AFTER,
  });
  assert(pausedNotReady.indexOf('恢复参与后还需课次已学或加入默写范围') >= 0,
    'a paused+unlearned word states the full condition (not just 恢复参与)');
  assert(pausedNotReady.indexOf('恢复参与后即可安排默写。') < 0,
    'a paused+unlearned word never promises 恢复参与后即可安排默写');

  const pausedReadySaved = [
    { word: 'lake', participates: false, textbooks: [{ id: 9, name: '书A', lesson: '第7课', includeInScope: true }] },
  ];
  const pausedReady = renderScopeBanner({
    rows: pausedRows,
    groupResults: scopeGroupResults('第7课', true, ['lake']),
    postSave: pausedReadySaved,
    booksAfter: SCOPE_BOOKS_AFTER,
  });
  assert(pausedReady.indexOf('恢复参与后即可安排默写') >= 0,
    'a paused word an include already qualifies keeps the restore-and-schedule promise');

  // PR #88 复盘 at the render level: a paused word whose only source sits
  // on a textbook the post-save read missed (书C is not in booksAfter) —
  // 资格待核实, never the definite「恢复参与后还需课次已学或加入默写范围」.
  const pausedUnreadSaved = [
    { word: 'river', participates: false, textbooks: [{ id: 3, name: '书C', lesson: 'C课', includeInScope: false }] },
  ];
  const pausedUnread = renderScopeBanner({
    rows: [{ index: 0, word: 'river', meaning: '河', lesson: '第7课', status: 'exists', checked: true }],
    groupResults: scopeGroupResults('第7课', false, ['river']),
    postSave: pausedUnreadSaved,
    booksAfter: SCOPE_BOOKS_AFTER,
  });
  assert(pausedUnread.indexOf('资格待核实 1 词') >= 0,
    'a paused word on an unread textbook renders 资格待核实');
  assert(pausedUnread.indexOf('恢复参与后还需课次已学或加入默写范围') < 0,
    'an unread-textbook paused word never states the 还需课次已学 condition');

  // PR #88 P1 at the render level: the post-save read SUCCEEDED but its
  // attribution projection failed server-side (the real degraded GET /words
  // — 200 with textbooks:null). The banner must say 资格待核实, never the
  // joined 可安排默写 nor the paused 恢复参与后即可安排默写 guess.
  const p1Saved = [
    { word: 'apple', participates: true, textbooks: null },
    { word: 'lake', participates: false, textbooks: null },
  ];
  const p1Banner = renderScopeBanner({
    rows: [
      { index: 0, word: 'apple', meaning: '苹果', lesson: '第5课', status: 'exists', checked: true },
      { index: 1, word: 'lake', meaning: '湖', lesson: '第7课', status: 'exists', checked: true },
    ],
    groupResults: [
      { group: { lesson: '第5课', includeInScope: false, words: ['apple'] }, ok: true },
      { group: { lesson: '第7课', includeInScope: false, words: ['lake'] }, ok: true },
    ],
    postSave: p1Saved,
    booksAfter: SCOPE_BOOKS_AFTER,
  });
  assert(p1Banner.indexOf('资格待核实 2 词') >= 0,
    'a degraded attribution read renders 资格待核实 for every word');
  assert(p1Banner.indexOf('可安排默写') < 0,
    'a degraded attribution read never renders 可安排默写');
  assert(p1Banner.indexOf('恢复参与后即可安排默写。') < 0,
    'a degraded attribution read never promises 恢复参与后即可安排默写');
})();

// ----------------------------------------------------------------------

// addAndAssign → renderTextbookCommitResult — run the real async method
// chain rather than hand-building its receipt. This is deliberately a
// tiny in-memory server: assignWords mutates the stored include flag, so
// an unsafe includeInScope:false request is observable on the second
// GET /words and in the rendered result.
async function testCommitDataFlow() {
  global.document = { createElement: makeScopeNode };

  const knownRow = {
    index: 0, word: 'apple', meaning: '苹果', lesson: '第5课', status: 'exists', checked: true,
  };
  const knownForm = Object.create(PhotoAddForm.prototype);
  knownForm.rows = [knownRow];
  knownForm.banner = makeScopeNode('div');
  knownForm.draftCategory = 'english';
  knownForm.includePending = false;
  knownForm.fetchWords = function () {
    return Promise.resolve([
      { id: 11, word: 'apple', meaning: '苹果', participates: true,
        textbooks: [{ id: 9, name: '书A', lesson: '第5课', includeInScope: false }] },
    ]);
  };
  knownForm.fetchTextbooks = function () { return Promise.resolve(SCOPE_BOOKS_AFTER); };
  knownForm.assignWords = function () { return Promise.resolve({ updated: 1 }); };
  knownForm.editWord = null;

  const knownReceipt = await knownForm.addAndAssign(SCOPE_BOOK, [knownRow]);
  knownForm.renderTextbookCommitResult(knownReceipt);
  const knownBanner = scopeTextOf(knownForm.banner);
  assert(knownBanner.indexOf('可安排默写 1 词（课次已学）') >= 0,
    'real data flow renders scheduling from addAndAssign post-save reads');

  const degradedRow = {
    index: 0, word: 'lake', meaning: '湖', lesson: '第7课', status: 'exists', checked: true,
  };
  let storedInclude = true;
  let wordReads = 0;
  const assignPayloads = [];
  const degradedForm = Object.create(PhotoAddForm.prototype);
  degradedForm.rows = [degradedRow];
  degradedForm.banner = makeScopeNode('div');
  degradedForm.draftCategory = 'english';
  degradedForm.includePending = false;
  degradedForm.fetchWords = function () {
    wordReads++;
    if (wordReads === 1) {
      // Exact degraded GET /words wire shape from textbooks_test.go:
      // the card is known, its attribution is not.
      return Promise.resolve([
        { id: 15, word: 'lake', meaning: '湖', participates: true, textbooks: null },
      ]);
    }
    // The projection recovers for the post-save read, exposing whether
    // the intervening assignment silently cleared the stored decision.
    return Promise.resolve([
      { id: 15, word: 'lake', meaning: '湖', participates: true,
        textbooks: [{ id: 9, name: '书A', lesson: '第7课', includeInScope: storedInclude }] },
    ]);
  };
  degradedForm.fetchTextbooks = function () { return Promise.resolve(SCOPE_BOOKS_AFTER); };
  degradedForm.assignWords = function (bookID, payload) {
    assignPayloads.push({ bookID: bookID, payload: payload });
    storedInclude = payload.includeInScope;
    return Promise.resolve({ updated: 1 });
  };
  degradedForm.editWord = null;

  const degradedReceipt = await degradedForm.addAndAssign(SCOPE_BOOK, [degradedRow]);
  degradedForm.renderTextbookCommitResult(degradedReceipt);
  const degradedBanner = scopeTextOf(degradedForm.banner);
  eq(assignPayloads, [],
    'real degraded flow sends no assignment when existing attribution is unknown');
  eq(storedInclude, true,
    'real degraded flow preserves the parent explicit include decision');
  eq(degradedReceipt.unassigned, ['lake'],
    'real degraded flow reports the source as unassigned');
  assert(degradedBanner.indexOf('失败 1 个') >= 0 &&
    degradedBanner.indexOf('词库读取失败，未能指定课次') >= 0,
    'real degraded flow renders an honest assignment failure');
}

// PR #88 复盘路径 B 的数据流验证：conflict+adopt + 第一次 GET /words 即
// textbooks:null（真实降级线体）→ editWord 成功、一个 assign payload 都不
// 发、横幅不写「已合并来源」。只留 status:'exists' 的旧数据流测不出这条
// （Codex @80e3977：新增流程测试只覆盖 exists，因此仍会假绿）。
async function testAdoptDegradedFlow() {
  global.document = { createElement: makeScopeNode };

  const row = {
    index: 0, word: 'star', meaning: '新星', lesson: '第5课',
    status: 'conflict', checked: true, resolution: 'adopt',
  };
  const assignPayloads = [];
  const editCalls = [];
  const form = Object.create(PhotoAddForm.prototype);
  form.rows = [row];
  form.banner = makeScopeNode('div');
  form.draftCategory = 'english';
  form.includePending = false;
  form.fetchWords = function () {
    // The degraded wire shape from textbooks_test.go: the card is known,
    // its attribution is not — every read of this flow.
    return Promise.resolve([
      { id: 16, word: 'star', meaning: '旧义', participates: true, textbooks: null },
    ]);
  };
  form.fetchTextbooks = function () { return Promise.resolve(SCOPE_BOOKS_AFTER); };
  form.assignWords = function (bookID, payload) {
    assignPayloads.push({ bookID: bookID, payload: payload });
    return Promise.resolve({ updated: 1 });
  };
  form.editWord = function (id, patch) {
    editCalls.push({ id: id, patch: patch });
    return Promise.resolve({});
  };

  const receipt = await form.addAndAssign(SCOPE_BOOK, [row]);
  form.renderTextbookCommitResult(receipt);
  const banner = scopeTextOf(form.banner);
  eq(editCalls.length, 1, 'adopt over unknown attribution still runs the meaning edit');
  eq(assignPayloads, [], 'adopt over unknown attribution sends NO assign payload');
  eq(receipt.groups, [], 'planAssignments skips the adopted word entirely');
  eq(receipt.unassigned, ['star'], 'the receipt carries the word as unassigned');
  assert(banner.indexOf('合并 1 个') < 0 && banner.indexOf('已合并来源') < 0,
    'the adopt banner never claims a merged source without a successful assign');
  assert(banner.indexOf('失败 1 个') >= 0,
    'the adopt banner reports the batch as a failure, not a success');
  assert(banner.indexOf('释义已按照片更新') >= 0 && banner.indexOf('未能指定课次') >= 0,
    'the adopt banner keeps what did persist (edit) + the missing step (assign)');

  // @80e3977 P3 的保护：待确认词 + includePending（归属已知时 payload 会带
  // includeInScope:true）在 textbooks:null 下同样一个 payload 都不发。
  const pendingRow = { index: 0, word: 'lake', meaning: '湖', lesson: '', status: 'exists', checked: true };
  const pendingPayloads = [];
  const pendingForm = Object.create(PhotoAddForm.prototype);
  pendingForm.rows = [pendingRow];
  pendingForm.banner = makeScopeNode('div');
  pendingForm.draftCategory = 'english';
  pendingForm.includePending = true;
  pendingForm.fetchWords = function () {
    return Promise.resolve([
      { id: 15, word: 'lake', meaning: '湖', participates: true, textbooks: null },
    ]);
  };
  pendingForm.fetchTextbooks = function () { return Promise.resolve(SCOPE_BOOKS_AFTER); };
  pendingForm.assignWords = function (bookID, payload) {
    pendingPayloads.push({ bookID: bookID, payload: payload });
    return Promise.resolve({ updated: 1 });
  };
  pendingForm.editWord = null;

  const pendingReceipt = await pendingForm.addAndAssign(SCOPE_BOOK, [pendingRow]);
  eq(pendingPayloads, [],
    'a would-be includeInScope:true payload is suppressed when attribution is unknown');
  eq(pendingReceipt.unassigned, ['lake'],
    'the suppressed include flow still reports the word as unassigned');
}

// ----------------------------------------------------------------------

testCommitDataFlow().then(testAdoptDegradedFlow).then(function () {
  if (failures) {
    console.error('parent-photo-textbook: ' + failures + ' of ' + checks + ' checks failed');
    process.exit(1);
  }
  console.log('parent-photo-textbook: all ' + checks + ' checks passed');
}).catch(function (err) {
  console.error(err && err.stack || err);
  process.exit(1);
});
