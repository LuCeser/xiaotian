// tests/test-parent-batch-parser.js — headless verification of the batch
// paste parser contract (Phase 9, Plan 06, D-10): pure parseBatch(text) →
// {rows:[{word,meaning}], errors:[{line,reason}]}. One word per line;
// word<TAB>meaning preferred; otherwise split at the FIRST whitespace run;
// '#' comments and blank lines skipped; per-line problems collected —
// never aborting the batch; duplicate words within one paste flagged
// referencing the first occurrence. `node tests/test-parent-batch-parser.js`.
'use strict';

const assert = require('node:assert');

// The IIFE assigns onto its `global` arg — in Node's CommonJS that is the
// module's top-level `this` (module.exports).
const { parseBatch } = require('../pkg/web/static/js/parent-batch-parser.js');

// 1. Word-only line → one row with an empty meaning; the result shape is
//    exactly {rows, errors}.
{
  assert.deepStrictEqual(parseBatch('apple'), {
    rows: [{ word: 'apple', meaning: '' }],
    errors: [],
  });
}

// 2. Tab line: word<TAB>meaning is the preferred split.
{
  assert.deepStrictEqual(parseBatch('apple\t苹果'), {
    rows: [{ word: 'apple', meaning: '苹果' }],
    errors: [],
  });
}

// 3. Space line: split at the FIRST whitespace run only — remaining
//    spaces stay in the meaning.
{
  assert.deepStrictEqual(parseBatch('apple 一个 红色的'), {
    rows: [{ word: 'apple', meaning: '一个 红色的' }],
    errors: [],
  });
}

// 4. Comments (# …) and blank/whitespace-only lines are skipped —
//    neither rows nor errors; line numbers keep counting them so
//    error.line matches what the parent sees in the paste.
{
  assert.deepStrictEqual(parseBatch('\n# 课文第十课\n   \napple\n\nbanana'), {
    rows: [
      { word: 'apple', meaning: '' },
      { word: 'banana', meaning: '' },
    ],
    errors: [],
  });
  // A comment carrying a tab is still a comment (checked before splitting).
  assert.deepStrictEqual(parseBatch('# 注释\t带tab\napple'), {
    rows: [{ word: 'apple', meaning: '' }],
    errors: [],
  });
}

// 5. A line consisting only of separators (e.g. a lone tab) →
//    empty-word error, not a silent skip.
{
  assert.deepStrictEqual(parseBatch('apple\n\t\nbanana'), {
    rows: [
      { word: 'apple', meaning: '' },
      { word: 'banana', meaning: '' },
    ],
    errors: [{ line: 2, reason: 'empty-word' }],
  });
  assert.deepStrictEqual(parseBatch('  \t  '), {
    rows: [],
    errors: [{ line: 1, reason: 'empty-word' }],
  });
}

// 6. Word length is capped in RUNES (code points), not UTF-16 units:
//    exactly 200 runes is valid, 201 exceeds; an emoji word of 150 code
//    points (300 UTF-16 units) is valid.
{
  assert.deepStrictEqual(parseBatch('a'.repeat(200)), {
    rows: [{ word: 'a'.repeat(200), meaning: '' }],
    errors: [],
  });
  assert.deepStrictEqual(parseBatch('a'.repeat(201)), {
    rows: [],
    errors: [{ line: 1, reason: 'word-too-long' }],
  });
  assert.deepStrictEqual(parseBatch('🍎'.repeat(150)), {
    rows: [{ word: '🍎'.repeat(150), meaning: '' }],
    errors: [],
  });
  assert.deepStrictEqual(parseBatch('🍎'.repeat(201)), {
    rows: [],
    errors: [{ line: 1, reason: 'word-too-long' }],
  });
}

// 7. A word containing an ASCII comma → word-comma error (CR-01): the
//    delete API joins word lists with "," and Python splits back on ",",
//    so a comma word must be rejected at parse time, per line.
{
  assert.deepStrictEqual(parseBatch('a,b'), {
    rows: [],
    errors: [{ line: 1, reason: 'word-comma' }],
  });
  assert.deepStrictEqual(parseBatch('apple\t苹果\na,b\t坏的\nbanana'), {
    rows: [
      { word: 'apple', meaning: '苹果' },
      { word: 'banana', meaning: '' },
    ],
    errors: [{ line: 2, reason: 'word-comma' }],
  });
}

// 8. Duplicate word within one paste: the SECOND occurrence is flagged
//    referencing the first line number; the first stays a valid row.
{
  assert.deepStrictEqual(parseBatch('apple\nbanana\napple'), {
    rows: [
      { word: 'apple', meaning: '' },
      { word: 'banana', meaning: '' },
    ],
    errors: [{ line: 3, reason: 'duplicate:1' }],
  });
}

// 9. Mixed input: valid rows AND errors come back together — one bad
//    line never aborts the whole batch.
{
  assert.deepStrictEqual(
    parseBatch('apple\t苹果\n\n# 注释\n banana two words \n\t\n banana'),
    {
      rows: [
        { word: 'apple', meaning: '苹果' },
        { word: 'banana', meaning: 'two words' },
      ],
      errors: [
        { line: 5, reason: 'empty-word' },
        { line: 6, reason: 'duplicate:4' },
      ],
    },
  );
}

// 10. Windows paste: a trailing \r is trimmed before parsing.
{
  assert.deepStrictEqual(parseBatch('apple\t苹果\r\nbanana\r\n'), {
    rows: [
      { word: 'apple', meaning: '苹果' },
      { word: 'banana', meaning: '' },
    ],
    errors: [],
  });
}

console.log('test-parent-batch-parser.js: all assertions passed');
