// tests/test-parent-photo-poem.js — headless verification of the
// whole-poem draft contract (Issue #11): the pure functions behind the
// mixed 语文 page's 古诗 preview. A poem is ONE work — classification
// identities the COMPLETE normalized text (never the title alone), the
// page's own explicit 背诵/默写 mark drives the default selection, and
// the PDF merge helpers replace one page's segment so a retry never
// duplicates. The Go side re-verifies everything at commit; these are
// the preview's honest previews.
// `node tests/test-parent-photo-poem.js`
'use strict';

const assert = require('node:assert');

// The IIFE assigns onto its `global` arg — in Node's CommonJS that is
// the module's top-level `this` (module.exports).
const {
  normalizePoemText,
  classifyPoemDrafts,
  describePoemInvalidReason,
  mergePagePoems,
  mergePageOthers,
} = require('../pkg/web/static/js/parent-photo.js');

// 1. normalizePoemText mirrors the server fingerprint: trimmed fields,
//    empty lines dropped, order kept — the FULL work, title included
//    only as one part of the identity.
{
  const a = normalizePoemText(' 静夜思 ', ' 李白 ', [' 床前明月光，疑是地上霜。 ', '', '举头望明月，低头思故乡。']);
  const b = normalizePoemText('静夜思', '李白', ['床前明月光，疑是地上霜。', '举头望明月，低头思故乡。']);
  assert.strictEqual(a, b);
  assert.ok(a.startsWith('静夜思\n李白\n'));
}

// 2. Identity is the complete text: same title + DIFFERENT lines stays
//    'new' (完整文本身份不能只以标题判断); the identical full work is
//    'exists' (重复确认不复制作品).
{
  const library = [
    { title: '静夜思', author: '李白', lines: ['床前明月光，疑是地上霜。', '举头望明月，低头思故乡。'] },
  ];
  const rows = classifyPoemDrafts([
    { title: '静夜思', author: '李白', lines: ['床前明月光，疑是地上霜。'] },
    { title: '静夜思', author: '李白', lines: ['床前明月光，疑是地上霜。', '举头望明月，低头思故乡。'] },
  ], library);
  assert.strictEqual(rows[0].status, 'new');
  assert.strictEqual(rows[1].status, 'exists');
  // author participates in the identity too — an unattributed lookalike
  // is a different draft until the parent resolves it by editing.
  const anonymous = classifyPoemDrafts(
    [{ title: '静夜思', author: '', lines: ['床前明月光，疑是地上霜。', '举头望明月，低头思故乡。'] }],
    library,
  );
  assert.strictEqual(anonymous[0].status, 'new');
}

// 3. Default selection follows the page's own explicit mark: 背诵/默写
//    check the poem, an unmarked (or unknown) requirement leaves it
//    unchecked for the parent to choose (明确背诵/默写要求影响默认选择).
{
  const rows = classifyPoemDrafts([
    { title: '咏鹅', author: '骆宾王', lines: ['鹅，鹅，鹅，'], required: '背诵' },
    { title: '悯农', author: '李绅', lines: ['锄禾日当午，'], required: '默写' },
    { title: '画', author: '王维', lines: ['远看山有色，'], required: '' },
    { title: '风', author: '李峤', lines: ['解落三秋叶，'], required: '熟读' },
    { title: '江南', author: '汉乐府', lines: ['江南可采莲，'] },
  ], []);
  assert.deepStrictEqual(
    rows.map((r) => [r.title, r.status, r.checked]),
    [
      ['咏鹅', 'new', true],
      ['悯农', 'new', true],
      ['画', 'new', false],
      ['风', 'new', false], // 熟读 is honest-but-unspecified: not auto-checked
      ['江南', 'new', false],
    ],
  );
}

// 4. Validation mirrors the server gate: empty title / no lines / over
//    the caps are 'invalid' with a reason; duplicates INSIDE one batch
//    flag the later copy.
{
  const long = 'x'.repeat(201);
  const rows = classifyPoemDrafts([
    { title: '', lines: ['一'] },
    { title: long, lines: ['一'] },
    { title: '登鹳雀楼', author: 'y'.repeat(101), lines: ['白日依山尽，'] },
    { title: '凉州词', lines: [] },
    { title: '出塞', lines: ['n'.repeat(201)] },
    { title: '长诗', lines: Array.from({ length: 65 }, (_, i) => String(i)) },
    { title: '春晓', author: '孟浩然', lines: ['春眠不觉晓，'] },
    { title: '春晓', author: '孟浩然', lines: ['春眠不觉晓，'] },
  ], []);
  assert.deepStrictEqual(rows.map((r) => [r.status, r.reason]), [
    ['invalid', 'empty-title'],
    ['invalid', 'title-too-long'],
    ['invalid', 'author-too-long'],
    ['invalid', 'no-lines'],
    ['invalid', 'line-too-long'],
    ['invalid', 'too-many-lines'],
    ['new', ''],
    ['invalid', 'duplicate:7'],
  ]);
  // Invalid rows are never checked — they cannot join the commit.
  rows.forEach((r, i) => {
    if (r.status === 'invalid') {
      assert.strictEqual(r.checked, false, `row ${i}`);
    }
  });
}

// 5. describePoemInvalidReason maps every machine reason to parent copy
//    (an unknown reason passes through unchanged — no silent guessing).
{
  assert.strictEqual(describePoemInvalidReason('empty-title'), '诗题不能为空');
  assert.strictEqual(describePoemInvalidReason('duplicate:3'), '与第 3 首重复');
  assert.strictEqual(describePoemInvalidReason('something-else'), 'something-else');
}

// 6. mergePagePoems replaces ONE page's poem segment (a retry never
//    duplicates) and re-classifies the fresh drafts against the library
//    so a cross-page repeat shows as 已有.
{
  const library = [{ title: '静夜思', author: '李白', lines: ['床前明月光，'] }];
  // The runner stamps pages via mergePagePoems itself (the photo path
  // never merges — its drafts all share page 0).
  const page1 = mergePagePoems([], 1, [
    { title: '静夜思', author: '李白', lines: ['床前明月光，'] },
    { title: '咏鹅', author: '骆宾王', lines: ['鹅，鹅，鹅，'] },
  ], library);
  assert.strictEqual(page1.length, 2);
  assert.strictEqual(page1[0].status, 'exists');
  const page2 = mergePagePoems(page1, 2, [{ title: '春晓', author: '孟浩然', lines: ['春眠不觉晓，'] }], library);
  assert.strictEqual(page2.length, 3);
  const merged = mergePagePoems(page2, 1, [{ title: '风', author: '李峤', lines: ['解落三秋叶，'] }], library);
  // BOTH page-1 poems (静夜思 + 咏鹅) were replaced by the retry's one
  // fresh draft; page 2 survives untouched.
  assert.deepStrictEqual(merged.map((r) => [r.title, r.page]), [
    ['春晓', 2],
    ['风', 1],
  ]);
  // The re-run of the SAME page's poems replaces (no duplication).
  const again = mergePagePoems(merged, 1, [{ title: '风', author: '李峤', lines: ['解落三秋叶，'] }], library);
  assert.strictEqual(again.length, 2);
}

// 7. mergePageOthers: display-only blocks are page-tagged, unknown kinds
//    normalize to text, and a retry replaces the page's segment. Nothing
//    here is ever committable — the shape carries no checked field.
{
  const first = mergePageOthers([], 1, [
    { kind: 'exercise', summary: '读一读，记一记' },
    { kind: 'mystery', summary: '一块正文' },
  ]);
  assert.deepStrictEqual(first, [
    { page: 1, kind: 'exercise', summary: '读一读，记一记' },
    { page: 1, kind: 'text', summary: '一块正文' },
  ]);
  const second = mergePageOthers(first.concat([{ page: 2, kind: 'text', summary: '旧页' }]), 1,
    [{ kind: 'text', summary: '新的正文' }]);
  assert.deepStrictEqual(second.map((o) => [o.page, o.summary]), [
    [2, '旧页'],
    [1, '新的正文'],
  ]);
}

// 8. An empty/absent poems array classifies to [] (an English page or a
//    词语-only photo never fabricates poem cards).
{
  assert.deepStrictEqual(classifyPoemDrafts([], []), []);
  assert.deepStrictEqual(classifyPoemDrafts(undefined, null), []);
}

console.log('test-parent-photo-poem: all assertions passed');
