/**
 * Parent photo-import E2E — 课文完整导入并选择整篇或指定段落 (Issue #13):
 * the SAME 语文 photo entry recognizes a MIXED page (词语 + a whole
 * passage with 自然段 boundaries and per-paragraph marks + an exercise
 * block), the parent verifies/corrects, picks the practice range (整篇 or
 * 指定自然段) and confirms — and the structured original lands in the
 * unified content library with the range stored SEPARATELY from the text,
 * adjustable later without re-importing — against the ISOLATED real
 * service.
 *
 * Test boundary (the suite's shared rule): ONLY the external recognition
 * service is replaced — the local mimo stub answers the vision call with
 * the fixture's canned reply (photo-zh-passage.reply.json, a real
 * rendered mixed page, see scripts/gen-passage-fixture.py). Everything
 * else is the real stack: real browser → real Go service → real SQLite
 * (per-run temp DB).
 *
 * Per-test identity: each import EDITS the passage's title to a unique
 * value before confirming (核对/纠错 is itself an acceptance criterion,
 * and the full-text fingerprint then keeps every test — and every retry —
 * additive-free in the per-run DB).
 *
 * Flows:
 *   1. import — mixed page 区分展示 (词语 table rows, ONE passage card
 *      with the page's own per-paragraph 背诵 mark default-checking
 *      exactly that paragraph, display-only exercise), 纠错 edit, one
 *      confirm commits BOTH kinds; GET /passages holds the whole original
 *      (title, ordered paragraphs) AND its own row for the paragraph
 *      range (原文和练习范围分别表达 — the other paragraphs stay, no
 *      copy of the passage); the word library gains only the 词语 and the
 *      chinese classroom selects the 词语, never a passage line (未接入
 *      类型不误入词语课堂).
 *   2. repeat import — the same work re-recognizes as 已有 and a second
 *      confirm reports existing for the passage AND the range without
 *      duplicating either (重复确认不重复建立相同范围).
 *   3. unified content view — the 类型 pill 课文 lists the original with
 *      its paragraphs; range management ADDS the whole range beside the
 *      paragraph one (以后练习全文仍使用同一份原文 — one passage row,
 *      never a copy), re-adding the same selection stays idempotent,
 *      removing one range keeps the other; 归属管理 assigns a textbook +
 *      lesson and the scope verdict follows the learned progress (练习
 *      目标与教材进度可共同查询).
 */
// @ts-check
const { test, expect } = require('@playwright/test');
const WebSocket = require('ws');

const CAT_ZH = 'chinese';
const CAT_EN = 'english';
const UNIT_IMPORT = 'e2e-zh-passage-import';
const BOOK_NAME = 'e2e-zh-passage-book';

const FIXTURE = require('node:path').join(__dirname, 'fixtures', 'photo-zh-passage.png');

const PARAGRAPHS = [
  '秋天的雨，是一把钥匙。',
  '秋天的雨，有一盒五彩缤纷的颜料。',
  '秋天的雨，藏着非常好闻的气味。',
  '秋天的雨，吹起了金色的小喇叭。',
];

/** A per-test unique passage title (纠错 edit + fingerprint identity). */
function passageTitle() {
  return '秋天的雨-e2e' + Date.now() + Math.floor(Math.random() * 1000);
}

/** DELETE one whole unit through the real API. A 404 is fine. */
async function deleteUnit(request, category, unit) {
  await request.delete('/api/v1/words', { data: { category, unit } }).catch(() => {});
}

/** Real passage rows (GET /passages) filtered by exact title. */
async function passageRows(request, title) {
  const resp = await request.get('/api/v1/passages?category=' + CAT_ZH);
  const data = await resp.json();
  return (data.passages || []).filter((p) => p.title === title);
}

/** Open the parent page's 照片导入 tab of the add card. */
async function openPhotoTab(page) {
  await page.goto('/parent.html');
  await page.getByRole('tab', { name: '词库' }).click();
  await expect(page.locator('#addSection')).toBeVisible();
  await page.getByRole('tab', { name: '照片导入' }).click();
  const panel = page.locator('#add-panel-photo');
  await expect(panel).toBeVisible();
  return panel;
}

/**
 * Recognize the mixed fixture as 中文, wait for the passage card, EDIT the
 * title to the unique value (纠错), pin the SUPPLEMENTARY path (不指定教
 * 材 — the recognized header would otherwise preselect 新建教材), and
 * return the panel. The textbook path rides its own assertions in test 1.
 *
 * Issue #89 order: pick 不指定教材 FIRST — the recognized header
 * preselects 新建教材, and syncUnitField hides the 单元 input while any
 * textbook is chosen, so the unit can only be filled on the 'none' branch.
 */
async function recognizeMixedAndCorrect(page, title) {
  const panel = page.locator('#add-panel-photo');
  await panel.locator('input[name="parent-category-photo"][value="chinese"]').check();
  await panel.locator('#parent-photo-file').setInputFiles(FIXTURE);
  await panel.getByRole('button', { name: '开始识别' }).click();
  const card = panel.locator('.photo-passage-card[data-passage-index="0"]');
  await expect(card).toBeVisible({ timeout: 30_000 });
  await panel.locator('select[aria-label="教材归属"]').selectOption('none');
  await panel.getByRole('combobox', { name: '单元', exact: true }).fill(UNIT_IMPORT);
  await card.locator('input[aria-label="课文标题 第 1 篇"]').fill(title);
  return panel;
}

/** Raw /rt session_init → session_ready (the classroom probe). */
function rtSessionInit(category) {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket('ws://127.0.0.1:18080/rt', {
      origin: 'http://127.0.0.1:18080',
    });
    const fail = setTimeout(() => reject(new Error('session_ready timeout')), 30_000);
    ws.on('error', (err) => { clearTimeout(fail); reject(err); });
    ws.on('open', () => {
      ws.send(JSON.stringify({
        type: 'session_init',
        audio: { sampleRate: 16000, format: 's16le', channels: 1 },
        mode: 'test',
        category,
      }));
    });
    ws.on('message', (data) => {
      const msg = JSON.parse(String(data));
      if (msg.type === 'session_ready') {
        clearTimeout(fail);
        ws.close();
        resolve(msg);
      } else if (msg.type === 'error') {
        clearTimeout(fail);
        ws.close();
        reject(new Error('session error: ' + JSON.stringify(msg)));
      }
    });
  });
}

test('mixed page: 区分展示, 纠错确认 stores the whole passage with its paragraph range; passages never enter the word flow', async ({ page, request }) => {
  await deleteUnit(request, CAT_ZH, UNIT_IMPORT);
  try {
    const title = passageTitle();
    const panel = await openPhotoTab(page);
    await panel.locator('input[name="parent-category-photo"][value="chinese"]').check();
    await panel.locator('#parent-photo-file').setInputFiles(FIXTURE);
    await panel.getByRole('button', { name: '开始识别' }).click();

    // 区分展示: the 词语 table keeps its rows; the exercise block stays
    // display-only (练习题不自动选入 — never inside the passage either).
    await expect(panel.locator('.photo-row').first()).toBeVisible({ timeout: 30_000 });
    await expect(panel.locator('.photo-row')).toHaveCount(2);
    const others = panel.locator('.photo-other-row');
    await expect(others).toHaveCount(1);
    await expect(others.locator('.photo-other-kind')).toHaveText('练习题');
    await expect(others.locator('input')).toHaveCount(0);

    // ONE passage card: the page's own whole-text mark rides the badge;
    // the per-paragraph mark (背诵课文第2自然段) default-checks exactly
    // paragraph 2 as the practice range — the other paragraphs stay
    // present and unchecked (有背诵标记的段落默认勾选, 其他正文可按篇或
    // 段选择).
    const card = panel.locator('.photo-passage-card[data-passage-index="0"]');
    await expect(card).toBeVisible();
    await expect(card.locator('.photo-passage-required')).toHaveText('要求背诵');
    await expect(card.locator('.photo-passage-check')).toBeChecked();
    await expect(card.locator('.photo-passage-status')).toHaveText('新课文');
    await expect(card.locator('.photo-passage-paragraph')).toHaveCount(4);
    for (let i = 0; i < PARAGRAPHS.length; i++) {
      await expect(card.locator('textarea[aria-label="第 1 篇第 ' + (i + 1) + ' 段"]'))
        .toHaveValue(PARAGRAPHS[i]);
    }
    await expect(card.locator('input[aria-label="练习第 1 篇第 2 段"]')).toBeChecked();
    await expect(card.locator('input[aria-label="练习第 1 篇第 1 段"]')).not.toBeChecked();
    await expect(card.locator('input[aria-label="练习第 1 篇第 3 段"]')).not.toBeChecked();
    await expect(card.locator('input[aria-label="整篇练习 第 1 篇"]')).not.toBeChecked();
    await expect(card.locator('.photo-passage-scope-note')).toHaveText('练习范围：第 2 段');
    // The marked paragraph carries its own badge on the row.
    await expect(card.locator('.photo-passage-paragraph-required')).toHaveCount(1);

    // 纠错 + confirm: the edited title rides the commit; both kinds go in
    // ONE confirm (the supplementary path — 不指定教材). Issue #89: the
    // unit input only exists on the 'none' branch, so pick it first.
    await panel.locator('select[aria-label="教材归属"]').selectOption('none');
    await panel.getByRole('combobox', { name: '单元', exact: true }).fill(UNIT_IMPORT);
    await card.locator('input[aria-label="课文标题 第 1 篇"]').fill(title);
    await panel.getByRole('button', { name: /确认添加/ }).click();
    await expect(panel.locator('.result-banner .result-summary'))
      .toContainText('已添加 2 个', { timeout: 30_000 });
    await expect(panel.locator('.result-banner .result-summary')).toContainText('已添加课文 1 篇');
    await expect(panel.locator('.result-banner .result-detail', { hasText: '新增课文' }))
      .toContainText('新增课文：' + title);

    // Real storage: the whole original kept — title, ordered 自然段 — and
    // the range stored as its OWN row beside the text (原文和练习范围分
    // 别表达): exactly one passage, one range, paragraphs 2 only, and the
    // other paragraphs NOT deleted.
    const rows = await passageRows(request, title);
    expect(rows).toHaveLength(1);
    expect(rows[0].paragraphs).toEqual(PARAGRAPHS);
    expect(rows[0].category).toBe(CAT_ZH);
    expect(rows[0].scopes).toHaveLength(1);
    expect(rows[0].scopes[0].whole).toBe(false);
    expect(rows[0].scopes[0].paragraphs).toEqual([2]);

    // 未接入类型不误入词语课堂: the word library holds ONLY the 词语;
    // the chinese classroom selects 词语 cards and never a passage line.
    const wordsResp = await request.get('/api/v1/words?category=' + CAT_ZH);
    const words = (await wordsResp.json()).words || [];
    const surfaces = words.map((w) => w.word);
    expect(surfaces).toContain('钥匙');
    expect(surfaces.some((w) => w.includes('秋天的雨') || w.includes(title))).toBe(false);

    // The pinned /rt chinese lesson row (e2e-parent-photo-chinese) is
    // created by whichever spec probes /rt first in this run and then
    // REUSED verbatim — membership in it is creation-order-dependent, so
    // the probe asserts this AC's actual invariant: whatever 词语 the
    // classroom holds, no passage surface (title or paragraph text) can
    // appear among them (passages are not cards; the due fetch reads only
    // the cards table).
    const ready = await rtSessionInit(CAT_ZH);
    expect(ready.category).toBe(CAT_ZH);
    const lessonSurfaces = ready.lesson.words.join('\n');
    expect(lessonSurfaces).not.toContain('秋天的雨');
    expect(lessonSurfaces).not.toContain(title);
    expect(lessonSurfaces).not.toContain('一把钥匙');
    expect(lessonSurfaces).not.toContain('五彩缤纷');
  } finally {
    await deleteUnit(request, CAT_ZH, UNIT_IMPORT);
  }
});

test('repeat import: the same full work and range report 已有 and never duplicate', async ({ page, request }) => {
  await deleteUnit(request, CAT_ZH, UNIT_IMPORT);
  try {
    const title = passageTitle();

    // First confirm stores the original with its paragraph range.
    await openPhotoTab(page);
    await recognizeMixedAndCorrect(page, title);
    await page.getByRole('button', { name: /确认添加/ }).click();
    await expect(page.locator('.result-banner .result-summary'))
      .toContainText('已添加课文 1 篇', { timeout: 30_000 });

    // Re-recognize the same page with the SAME corrected title: the
    // preview classifies the whole text against the real library — 已有,
    // checkable (range and attribution still apply), never a second copy.
    await recognizeMixedAndCorrect(page, title);
    const card = page.locator('.photo-passage-card[data-passage-index="0"]');
    await expect(card).toHaveClass(/photo-passage-exists/);
    await expect(card.locator('.photo-passage-status')).toHaveText('已有（不会重复建立）');
    await expect(card.locator('.photo-passage-check')).toBeChecked();

    await page.getByRole('button', { name: /确认添加/ }).click();
    await expect(page.locator('.result-banner .result-summary'))
      .toContainText('课文已有 1 篇', { timeout: 30_000 });
    await expect(page.locator('.result-banner .result-detail', { hasText: '课文已有' }))
      .toContainText('课文已有（不会重复建立）：' + title);

    // Real storage: exactly ONE passage row and ONE range row — the repeat
    // confirmation duplicated neither the original nor the range.
    const rows = await passageRows(request, title);
    expect(rows).toHaveLength(1);
    expect(rows[0].scopes).toHaveLength(1);
  } finally {
    await deleteUnit(request, CAT_ZH, UNIT_IMPORT);
  }
});

test('unified content view: 课文 type, range switching and progress scope together', async ({ page, request }) => {
  await deleteUnit(request, CAT_ZH, UNIT_IMPORT);
  try {
    const title = passageTitle();
    await openPhotoTab(page);
    await recognizeMixedAndCorrect(page, title);
    await page.getByRole('button', { name: /确认添加/ }).click();
    await expect(page.locator('.result-banner .result-summary'))
      .toContainText('已添加课文 1 篇', { timeout: 30_000 });

    // The unified content view: the 类型 pill 课文 shows the original.
    await page.getByRole('tab', { name: '词库' }).click();
    await page.locator('.type-pills button', { hasText: '课文' }).click();
    const card = page.locator('.passage-card', { hasText: title });
    await expect(card).toBeVisible({ timeout: 15_000 });
    await expect(card.locator('.badge-chinese')).toHaveText('中文');
    await expect(card.locator('.passage-paragraph')).toHaveCount(4);
    await expect(card.locator('.passage-paragraph').nth(0))
      .toHaveText(/秋天的雨，是一把钥匙。/);
    await expect(card.locator('.passage-scope')).toHaveText('无教材归属（补充内容）');

    // 选择第 2、3 段不删除其他段落: paragraph 2 reads marked (inside the
    // range), the other three plainly visible and unmarked.
    await expect(card.locator('.passage-paragraph-in-scope')).toHaveCount(1);
    await expect(card.locator('.passage-paragraph').nth(1)).toHaveClass(/passage-paragraph-in-scope/);

    // 范围切换 WITHOUT re-import: add the whole range beside the
    // paragraph one — the SAME original gains a second range row (以后练
    // 习全文仍使用同一份原文), never a copy of the passage.
    await card.locator('input[aria-label="添加整篇练习范围"]').check();
    await card.getByRole('button', { name: '添加练习范围' }).click();
    await expect(page.locator('.passage-card', { hasText: title })
      .locator('.passage-scope-row-text')).toHaveText(['第 2 段', '整篇'], { timeout: 15_000 });
    const twoRanges = await passageRows(request, title);
    expect(twoRanges).toHaveLength(1); // one passage, never a copy
    expect(twoRanges[0].scopes).toHaveLength(2);
    // The whole range covers every paragraph — all four read marked.
    await expect(page.locator('.passage-card', { hasText: title })
      .locator('.passage-paragraph-in-scope')).toHaveCount(4);

    // Re-adding the SAME selection stays idempotent — one row, no
    // duplicate (重复确认不重复建立相同范围).
    const fresh = page.locator('.passage-card', { hasText: title });
    await fresh.locator('input[aria-label="添加第 2 段练习范围"]').check();
    await fresh.getByRole('button', { name: '添加练习范围' }).click();
    await expect(page.locator('.passage-card', { hasText: title })
      .locator('.passage-scope-row-text')).toHaveText(['第 2 段', '整篇'], { timeout: 15_000 });

    // Removing ONE range keeps the passage, its paragraphs and the other
    // range (范围可调整, 不删原文).
    await page.locator('.passage-card', { hasText: title })
      .getByRole('button', { name: '移除练习范围 第 2 段' }).click();
    await expect(page.locator('.passage-card', { hasText: title })
      .locator('.passage-scope-row-text')).toHaveText(['整篇'], { timeout: 15_000 });
    await expect(page.locator('.passage-card', { hasText: title })
      .locator('.passage-paragraph')).toHaveCount(4);
    const afterRemove = await passageRows(request, title);
    expect(afterRemove).toHaveLength(1);
    expect(afterRemove[0].scopes).toHaveLength(1);
    expect(afterRemove[0].scopes[0].whole).toBe(true);

    // Subject filter: the 英语 pill hides the chinese passage.
    await page.locator('.category-pills button', { hasText: '英语' }).click();
    await expect(page.locator('.passage-card', { hasText: title })).toHaveCount(0);
    await page.locator('.category-pills button', { hasText: '中文' }).click();
    await expect(page.locator('.passage-card', { hasText: title })).toBeVisible({ timeout: 15_000 });

    // 练习目标与教材进度可共同查询: attribution lands with the passage's
    // own lesson; 已学进度规则仍适用 (progress-pending → learned).
    const book = await request.post('/api/v1/textbooks', {
      data: { name: BOOK_NAME, category: CAT_ZH, lessons: ['第6课'] },
    });
    const bookId = (await book.json()).id;
    expect(bookId).toBeTruthy();

    await page.locator('.passage-card', { hasText: title })
      .getByRole('button', { name: '管理归属' }).click();
    const select = page.locator('select[aria-label="教材"]');
    // selectOption auto-waits until the async textbook list lands.
    await select.selectOption({ label: BOOK_NAME });
    await page.locator('#passage-lesson-input').fill('第6课');
    await page.getByRole('button', { name: '保存归属' }).click();
    await expect(page.locator('.passage-card', { hasText: title })
      .locator('.textbook-chip', { hasText: BOOK_NAME })).toBeVisible({ timeout: 15_000 });

    // Lesson 1 of 1 unlearned → progress-pending; the ranges stay
    // readable beside the verdict.
    const scoped = page.locator('.passage-card', { hasText: title });
    await expect(scoped.locator('.passage-scope')).toHaveText('课次尚未学到，暂不在学习范围');
    await expect(scoped.locator('.passage-scope-row-text')).toHaveText(['整篇']);

    // Move the book's progress to 已学第6课 → the passage enters the scope.
    const progress = await request.put('/api/v1/textbooks/' + bookId, {
      data: { learnedLessons: 1 },
    });
    expect(progress.ok()).toBeTruthy();
    // A type toggle away and back refetches the list with the new verdict.
    await page.locator('.type-pills button', { hasText: '词语' }).click();
    await page.locator('.type-pills button', { hasText: '课文' }).click();
    await expect(page.locator('.passage-card', { hasText: title }).locator('.passage-scope'))
      .toHaveText('已学课次内，随进度在范围内', { timeout: 15_000 });
  } finally {
    await deleteUnit(request, CAT_ZH, UNIT_IMPORT);
  }
});
