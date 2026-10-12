// tests/test-parent-textbook-form.js — headless verification of the
// 教材管理 pure helpers (Issue #4): the lesson-list textarea parser
// (order-preserving, never sorted), the metadata summary and the
// progress label copy. `node tests/test-parent-textbook-form.js`
'use strict';

const assert = require('node:assert');

// The IIFE assigns onto its `global` arg — in Node's CommonJS that is
// the module's top-level `this` (module.exports).
const {
  parseLessonsText, lessonsText, textbookMetaSummary, progressLabel, assignLabel,
} = require('../pkg/web/static/js/parent-textbook.js');

// 1. parseLessonsText: line order IS the teaching order (验收 3 — never
//    dictionary order), blank lines skipped, ends trimmed.
{
  const parsed = parseLessonsText('第3课\r\n  \n  第1课\n第2课\n');
  assert.deepStrictEqual(parsed, { lessons: ['第3课', '第1课', '第2课'], error: '' });
  assert.strictEqual(parseLessonsText('').error, '');
  assert.deepStrictEqual(parseLessonsText('').lessons, []);
}

// 2. Duplicates are rejected with the offending name.
{
  const parsed = parseLessonsText('A课\nB课\nA课');
  assert.ok(parsed.error.includes('A课'), parsed.error);
  assert.deepStrictEqual(parsed.lessons, []);
}

// 3. Length caps: lesson names ≤ 100 runes, list ≤ 200 entries.
{
  assert.ok(parseLessonsText('x'.repeat(101)).error.includes('100'));
  const long = Array.from({ length: 201 }, (_, i) => '第' + (i + 1) + '课').join('\n');
  assert.ok(parseLessonsText(long).error.includes('200'));
}

// 4. lessonsText round-trips parseLessonsText (the edit modal prefill).
{
  const lessons = ['第3课', '第1课', '第2课'];
  assert.strictEqual(lessonsText(lessons), '第3课\n第1课\n第2课');
  assert.deepStrictEqual(parseLessonsText(lessonsText(lessons)).lessons, lessons);
}

// 5. textbookMetaSummary: non-empty fields joined with ·, empties skipped.
{
  assert.strictEqual(
    textbookMetaSummary({ edition: '人教版', stage: '', grade: '三年级', volume: '上册', module: '' }),
    '人教版·三年级·上册',
  );
  assert.strictEqual(textbookMetaSummary({}), '');
  assert.strictEqual(textbookMetaSummary(null), '');
}

// 6. progressLabel: N includes lesson N (验收 4 的文案面).
{
  const book = { lessons: ['第3课', '第1课', '第2课'], learnedLessons: 2 };
  assert.strictEqual(progressLabel(book), '已学完第 2 课「第1课」（共 3 课）');
  assert.strictEqual(progressLabel({ lessons: ['A'], learnedLessons: 0 }), '尚未开始（共 1 课）');
  assert.strictEqual(progressLabel({ lessons: [], learnedLessons: 0 }), '还没有课次表');
}

// 7. assignLabel: chip copy for learned-lesson / 待确认 / include rows.
{
  assert.strictEqual(assignLabel({ name: '主教材', lesson: '第1课' }), '主教材·第1课');
  assert.strictEqual(assignLabel({ name: '主教材', lesson: '' }), '主教材·待确认');
  assert.strictEqual(
    assignLabel({ name: '主教材', lesson: '', includeInScope: true }),
    '主教材·待确认（已加入范围）',
  );
}

console.log('test-parent-textbook-form: all assertions passed');
