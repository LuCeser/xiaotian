// tests/test-parent-photo-passages.js — headless verification of the
// whole-passage draft contract (Issue #13): the pure functions behind the
// mixed 语文 page's 课文 preview. A passage is ONE structured original —
// classification identities the COMPLETE normalized text (never the title
// alone), the page's own marks drive the default practice range (marked
// paragraphs default to exactly those paragraphs, a whole-text mark to
// 整篇, unmarked prose stays the parent's choice), and the PDF merge
// helper replaces one page's segment so a retry never duplicates. The Go
// side re-verifies everything at commit; these are the preview's honest
// previews.
// `node tests/test-parent-photo-passages.js`
'use strict';

const assert = require('node:assert');

// The IIFE assigns onto its `global` arg — in Node's CommonJS that is
// the module's top-level `this` (module.exports).
const {
  normalizePassageText,
  classifyPassageDrafts,
  describePassageInvalidReason,
  mergePagePassages,
} = require('../pkg/web/static/js/parent-photo.js');

// 1. normalizePassageText mirrors the server fingerprint: trimmed fields,
//    empty paragraphs dropped, order kept — the FULL original, title
//    included only as one part of the identity.
{
  const a = normalizePassageText(' 秋天的雨 ', '', [' 一段。 ', '', '二段。']);
  const b = normalizePassageText('秋天的雨', '', ['一段。', '二段。']);
  assert.strictEqual(a, b);
  assert.ok(a.startsWith('秋天的雨\n\n'));
}

// 2. Identity is the complete text: same title + a DIFFERENT paragraph
//    (a correction) stays 'new' — 原文修正不得使已有范围指向错误段落,
//    so a corrected original is its own passage until confirmed; the
//    identical full text is 'exists' (重复确认不复制课文).
{
  const library = [
    { title: '秋天的雨', author: '', paragraphs: ['一段。', '二段。', '三段。'] },
  ];
  const rows = classifyPassageDrafts([
    { title: '秋天的雨', author: '', paragraphs: ['一段。', '二段。', '三段。'] },
    { title: '秋天的雨', author: '', paragraphs: ['一段。', '二段合并三段。'] },
  ], library);
  assert.strictEqual(rows[0].status, 'exists');
  assert.strictEqual(rows[1].status, 'new');
}

// 3. The default practice range follows the page's own marks: marked
//    paragraphs default to exactly those paragraphs (有背诵标记的段落
//    默认勾选), a whole-text 背诵/默写 mark selects 整篇, unmarked (or
//    熟读-style unspecified) prose stays unchecked with NO range (其他
//    正文可按篇或段选择).
{
  const rows = classifyPassageDrafts([
    { title: '秋天的雨', paragraphs: ['一段。', '二段。', '三段。'], required: '背诵课文第2自然段', required_paragraphs: [2] },
    { title: '植物妈妈有办法', paragraphs: ['一段。', '二段。'], required: '背诵课文' },
    { title: '雪孩子', paragraphs: ['一段。', '二段。'], required: '默写课文' },
    { title: '黄山奇石', paragraphs: ['一段。', '二段。'], required: '' },
    { title: '坐井观天', paragraphs: ['一段。', '二段。'], required: '朗读课文' },
  ], []);
  assert.deepStrictEqual(
    rows.map((r) => [r.title, r.checked, r.scopeWhole, r.scopeParagraphs]),
    [
      ['秋天的雨', true, false, [2]],
      ['植物妈妈有办法', true, true, []],
      ['雪孩子', true, true, []],
      ['黄山奇石', false, false, []],
      ['坐井观天', false, false, []], // 朗读 is honest-but-unspecified: the parent chooses
    ],
  );
}

// 4. Marked paragraph numbers outside the real text are noise, never a
//    default-selection hint pointing at a paragraph that does not exist;
//    the kept set is sorted and deduplicated.
{
  const rows = classifyPassageDrafts([
    { title: '雾在哪里', paragraphs: ['一段。', '二段。'], required_paragraphs: [2, 0, 5, 2, 1] },
  ], []);
  assert.deepStrictEqual(rows[0].requiredParagraphs, [1, 2]);
  assert.deepStrictEqual(rows[0].scopeParagraphs, [1, 2]);
  assert.strictEqual(rows[0].checked, true);
}

// 5. Validation mirrors the server gate: empty title / no paragraphs /
//    over the caps are 'invalid' with a reason; duplicates INSIDE one
//    batch flag the later copy; invalid rows are never checked.
{
  const long = 'x'.repeat(201);
  const rows = classifyPassageDrafts([
    { title: '', paragraphs: ['一'] },
    { title: long, paragraphs: ['一'] },
    { title: '太阳', author: 'y'.repeat(101), paragraphs: ['一'] },
    { title: '影子', paragraphs: [] },
    { title: '比尾巴', paragraphs: Array.from({ length: 61 }, (_, i) => String(i)) },
    { title: '青蛙写诗', paragraphs: ['n'.repeat(1001)] },
    { title: '明天要远足', paragraphs: ['一'] },
    { title: '明天要远足', paragraphs: ['一'] },
  ], []);
  assert.deepStrictEqual(rows.map((r) => [r.status, r.reason]), [
    ['invalid', 'empty-title'],
    ['invalid', 'title-too-long'],
    ['invalid', 'author-too-long'],
    ['invalid', 'no-paragraphs'],
    ['invalid', 'too-many-paragraphs'],
    ['invalid', 'paragraph-too-long'],
    ['new', ''],
    ['invalid', 'duplicate:7'],
  ]);
  rows.forEach((r, i) => {
    if (r.status === 'invalid') {
      assert.strictEqual(r.checked, false, `row ${i}`);
    }
  });
}

// 6. describePassageInvalidReason maps every machine reason to parent
//    copy (an unknown reason passes through unchanged — no silent
//    guessing).
{
  assert.strictEqual(describePassageInvalidReason('empty-title'), '课文标题不能为空');
  assert.strictEqual(describePassageInvalidReason('duplicate:3'), '与第 3 篇重复');
  assert.strictEqual(describePassageInvalidReason('something-else'), 'something-else');
}

// 7. mergePagePassages replaces ONE page's passage segment (a retry never
//    duplicates) and re-classifies the fresh drafts against the library
//    so a cross-page repeat shows as 已有; other pages survive untouched.
{
  const library = [{ title: '秋天的雨', author: '', paragraphs: ['一段。'] }];
  const page1 = mergePagePassages([], 1, [
    { title: '秋天的雨', author: '', paragraphs: ['一段。'] },
    { title: '雪孩子', author: '', paragraphs: ['一段。'] },
  ], library);
  assert.strictEqual(page1.length, 2);
  assert.strictEqual(page1[0].status, 'exists');
  const page2 = mergePagePassages(page1, 2, [{ title: '影子', author: '', paragraphs: ['一段。'] }], library);
  assert.strictEqual(page2.length, 3);
  const merged = mergePagePassages(page2, 1, [{ title: '明天要远足', author: '', paragraphs: ['一段。'] }], library);
  assert.deepStrictEqual(merged.map((r) => [r.title, r.page]), [
    ['影子', 2],
    ['明天要远足', 1],
  ]);
  const again = mergePagePassages(merged, 1, [{ title: '明天要远足', author: '', paragraphs: ['一段。'] }], library);
  assert.strictEqual(again.length, 2);
}

// 8. An empty/absent passages array classifies to [] (an English page or
//    a 词语-only photo never fabricates passage cards).
{
  assert.deepStrictEqual(classifyPassageDrafts([], []), []);
  assert.deepStrictEqual(classifyPassageDrafts(undefined, null), []);
}

console.log('test-parent-photo-passages: all assertions passed');
