// tests/test-parent-photo-classify.js — headless verification of the
// photo-import classification contract (Issue #3): pure
// classifyPhotoEntries(entries, existingWords) → rows with status
// new/exists/conflict/invalid. The Go server would silently skip every
// duplicate (UNIQUE(word, category)); this classification is what lets the
// preview show 已有 / 释义不同 BEFORE the parent commits, and pins the
// keep-the-original rule for conflicting meanings.
// `node tests/test-parent-photo-classify.js`
'use strict';

const assert = require('node:assert');

// The IIFE assigns onto its `global` arg — in Node's CommonJS that is the
// module's top-level `this` (module.exports).
const { classifyPhotoEntries } = require('../pkg/web/static/js/parent-photo.js');

// 1. New words classify as new; trim applies to both fields; phrases with
//    spaces stay one entry.
{
  const rows = classifyPhotoEntries(
    [{ word: ' ice cream ', meaning: ' 冰激凌 ' }, { word: 'apple', meaning: '' }],
    [],
  );
  assert.deepStrictEqual(rows.map((r) => [r.status, r.word, r.meaning]), [
    ['new', 'ice cream', '冰激凌'],
    ['new', 'apple', ''],
  ]);
}

// 2. Existing word with the SAME meaning → exists (not submittable; the
//    library already has exactly this entry and history is never reset).
{
  const rows = classifyPhotoEntries(
    [{ word: 'apple', meaning: '苹果' }],
    [{ word: 'apple', meaning: ' 苹果 ' }],
  );
  assert.strictEqual(rows[0].status, 'exists');
  assert.strictEqual(rows[0].existingMeaning, '苹果');
}

// 3. Existing word with a DIFFERENT meaning → conflict: the row carries the
//    library's meaning, is not submittable in this flow (keep-the-original).
{
  const rows = classifyPhotoEntries(
    [{ word: 'apple', meaning: '苹果一种水果' }],
    [{ word: 'apple', meaning: '苹果' }],
  );
  assert.strictEqual(rows[0].status, 'conflict');
  assert.strictEqual(rows[0].existingMeaning, '苹果');
}

// 4. Empty meaning on BOTH sides is still "same meaning" → exists.
{
  const rows = classifyPhotoEntries(
    [{ word: 'apple', meaning: '' }],
    [{ word: 'apple', meaning: '' }],
  );
  assert.strictEqual(rows[0].status, 'exists');
}

// 5. Invalid rules mirror the add path: empty word, comma, over-cap word,
//    over-cap meaning, in-photo duplicate referencing the first row.
{
  const long = 'x'.repeat(201);
  const longMeaning = 'y'.repeat(1001);
  const rows = classifyPhotoEntries(
    [
      { word: '', meaning: '' },
      { word: 'a,b', meaning: '' },
      { word: long, meaning: '' },
      { word: 'ok', meaning: longMeaning },
      { word: 'apple', meaning: '' },
      { word: 'apple', meaning: 'again' },
    ],
    [],
  );
  assert.deepStrictEqual(rows.map((r) => [r.status, r.reason]), [
    ['invalid', 'empty-word'],
    ['invalid', 'word-comma'],
    ['invalid', 'word-too-long'],
    ['invalid', 'meaning-too-long'],
    ['new', ''],
    ['invalid', 'duplicate:5'],
  ]);
}

// 6. Guard clauses: null-ish inputs behave as empty (no throw).
{
  assert.deepStrictEqual(classifyPhotoEntries(null, null), []);
  assert.deepStrictEqual(classifyPhotoEntries([null, undefined]), [
    { index: 0, word: '', meaning: '', status: 'invalid', reason: 'empty-word', existingMeaning: '' },
    { index: 1, word: '', meaning: '', status: 'invalid', reason: 'empty-word', existingMeaning: '' },
  ]);
}

// 7. Word comparison is exact-after-trim (the server's UNIQUE semantics):
//    case and internal spacing differences are DIFFERENT words.
{
  const rows = classifyPhotoEntries(
    [{ word: 'Apple', meaning: '' }, { word: 'ice  cream', meaning: '' }],
    [{ word: 'apple', meaning: '' }, { word: 'ice cream', meaning: '' }],
  );
  assert.deepStrictEqual(rows.map((r) => r.status), ['new', 'new']);
}

console.log('test-parent-photo-classify: all assertions passed');
