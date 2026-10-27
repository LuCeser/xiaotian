/**
 * Parent photo-import E2E — 整首古诗 (Issue #11): the SAME 语文 photo
 * entry recognizes a MIXED page (词语 + a whole poem + body text +
 * exercises), the parent verifies/corrects and confirms, and the whole
 * work lands in the unified content library with attribution and
 * progress-scope management — against the ISOLATED real service.
 *
 * Test boundary (the suite's shared rule): ONLY the external recognition
 * service is replaced — the local mimo stub answers the vision call with
 * the fixture's canned reply (photo-zh-poem.reply.json, a real rendered
 * mixed page, see scripts/gen-poem-fixture.py). Everything else is the
 * real stack: real browser → real Go service → real SQLite (per-run
 * temp DB).
 *
 * Per-test identity: each import EDITS the poem's title to a unique
 * value before confirming (核对/纠错 is itself an acceptance criterion,
 * and the fingerprint identity then keeps every test — and every retry —
 * additive-free in the per-run DB).
 *
 * Flows:
 *   1. import — mixed page 区分展示 (词语 table rows, ONE poem card with
 *      the page's own 背诵 badge default-checking it, display-only 其他
 *      blocks with no commit surface), 纠错 edit, one confirm commits
 *      BOTH kinds; GET /poems holds the whole work (title, author,
 *      ordered lines); the word library gains only the 词语 and the
 *      chinese classroom selects the 词语, never a poem line (尚未支持
 *      的类型不误入单词报读).
 *   2. repeat import — the same work re-recognizes as 已有 and a second
 *      confirm reports existing without duplicating (重复确认不复制;
 *      identity is the full text).
 *   3. unified content view — the 类型 pill 古诗 lists the work with its
 *      lines and NO dictation surfaces; 归属管理 assigns a textbook +
 *      lesson; the scope verdict follows the textbook's learned progress
 *      (课次尚未学到 → 已学课次内), and the subject pill filters (英语
 *      hides the chinese poem).
 */
// @ts-check
const { test, expect } = require('@playwright/test');
const WebSocket = require('ws');

const CAT_ZH = 'chinese';
const CAT_EN = 'english';
const UNIT_IMPORT = 'e2e-zh-poem-import';
const BOOK_NAME = 'e2e-zh-poem-book';

const FIXTURE = require('node:path').join(__dirname, 'fixtures', 'photo-zh-poem.png');

/** A per-test unique poem title (纠错 edit + fingerprint identity). */
function poemTitle() {
  return '静夜思-e2e' + Date.now() + Math.floor(Math.random() * 1000);
}

/** DELETE one whole unit through the real API. A 404 is fine. */
async function deleteUnit(request, category, unit) {
  await request.delete('/api/v1/words', { data: { category, unit } }).catch(() => {});
}

/** Real poem rows (GET /poems) filtered by title prefix. */
async function poemRows(request, title) {
  const resp = await request.get('/api/v1/poems?category=' + CAT_ZH);
  const data = await resp.json();
  return (data.poems || []).filter((p) => p.title === title);
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
 * Recognize the mixed fixture as 中文, wait for the poem card, EDIT the
 * title to the unique value (纠错), pin the SUPPLEMENTARY path (不指定教
 * 材 — the recognized header would otherwise preselect 新建教材), and
 * return the panel. The textbook path has its own test.
 */
async function recognizeMixedAndCorrect(page, title) {
  const panel = page.locator('#add-panel-photo');
  await panel.locator('input[name="parent-category-photo"][value="chinese"]').check();
  await panel.locator('#parent-photo-file').setInputFiles(FIXTURE);
  await panel.getByRole('button', { name: '开始识别' }).click();
  const card = panel.locator('.photo-poem-card[data-poem-index="0"]');
  await expect(card).toBeVisible({ timeout: 30_000 });
  await panel.getByRole('combobox', { name: '单元', exact: true }).fill(UNIT_IMPORT);
  await panel.locator('select[aria-label="教材归属"]').selectOption('none');
  await card.locator('input[aria-label="诗题 第 1 首"]').fill(title);
  // The 纠错 re-classification keeps the row committable — 'new' for a
  // first import (test 1 pins that), 'exists' when the same full text is
  // already stored (the repeat-import test's very subject).
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

test('mixed page: 区分展示, 纠错确认 stores the whole poem; poems never enter the word flow', async ({ page, request }) => {
  await deleteUnit(request, CAT_ZH, UNIT_IMPORT);
  try {
    const title = poemTitle();
    const panel = await openPhotoTab(page);
    await panel.locator('input[name="parent-category-photo"][value="chinese"]').check();
    await panel.locator('#parent-photo-file').setInputFiles(FIXTURE);
    await panel.getByRole('button', { name: '开始识别' }).click();

    // 区分展示: the 词语 table keeps its rows (美丽 exists-check against
    // the empty fresh library? no — fresh DB, both words are new here).
    await expect(panel.locator('.photo-row').first()).toBeVisible({ timeout: 30_000 });
    await expect(panel.locator('.photo-row')).toHaveCount(2);

    // ONE poem card: the page's own 背诵 mark rides the badge and
    // default-checks the poem (明确背诵/默写要求影响默认选择).
    const card = panel.locator('.photo-poem-card[data-poem-index="0"]');
    await expect(card).toBeVisible();
    await expect(card.locator('.photo-poem-required')).toHaveText('要求背诵');
    await expect(card.locator('.photo-poem-check')).toBeChecked();
    await expect(card.locator('.photo-poem-status')).toHaveText('新古诗');
    await expect(card.locator('input[aria-label="作者 第 1 首"]')).toHaveValue('李白');
    await expect(card.locator('textarea[aria-label="诗句 第 1 首（每行一句）"]'))
      .toHaveValue('床前明月光，疑是地上霜。\n举头望明月，低头思故乡。');

    // The 其他内容 blocks are display-only: both kinds show, and NOTHING
    // in that section is selectable (练习题不自动选入).
    const others = panel.locator('.photo-other-row');
    await expect(others).toHaveCount(2);
    await expect(others.nth(0).locator('.photo-other-kind')).toHaveText('课文正文');
    await expect(others.nth(1).locator('.photo-other-kind')).toHaveText('练习题');
    await expect(others.nth(1).locator('.photo-other-note')).toHaveText('不导入');
    await expect(others.locator('input')).toHaveCount(0);

    // 纠错 + confirm: the edited title rides the commit; both kinds go in
    // ONE confirm (the supplementary path — 不指定教材).
    await panel.getByRole('combobox', { name: '单元', exact: true }).fill(UNIT_IMPORT);
    await panel.locator('select[aria-label="教材归属"]').selectOption('none');
    await card.locator('input[aria-label="诗题 第 1 首"]').fill(title);
    await panel.getByRole('button', { name: /确认添加/ }).click();
    await expect(panel.locator('.result-banner .result-summary'))
      .toContainText('已添加 2 个', { timeout: 30_000 });
    await expect(panel.locator('.result-banner .result-summary')).toContainText('已添加古诗 1 首');
    await expect(panel.locator('.result-banner .result-detail', { hasText: '新增古诗' }))
      .toContainText('新增古诗：' + title);

    // Real storage: the whole work kept — title, author, 分行和顺序.
    const rows = await poemRows(request, title);
    expect(rows).toHaveLength(1);
    expect(rows[0].author).toBe('李白');
    expect(rows[0].lines).toEqual(['床前明月光，疑是地上霜。', '举头望明月，低头思故乡。']);
    expect(rows[0].category).toBe(CAT_ZH);
    expect(rows[0].in_scope).toBe(true); // 补充内容 — management state only

    // 尚未支持的类型不误入单词报读: the word library holds ONLY the 词语;
    // the chinese classroom selects the 词语 and never a poem line.
    const wordsResp = await request.get('/api/v1/words?category=' + CAT_ZH);
    const words = (await wordsResp.json()).words || [];
    const surfaces = words.map((w) => w.word);
    expect(surfaces).toContain('世界');
    expect(surfaces.some((w) => w.includes('床前明月光') || w.includes(title))).toBe(false);

    const ready = await rtSessionInit(CAT_ZH);
    expect(ready.category).toBe(CAT_ZH);
    expect(ready.lesson.words).toContain('世界');
    expect(ready.lesson.words.join('\n')).not.toContain('床前明月光');
  } finally {
    await deleteUnit(request, CAT_ZH, UNIT_IMPORT);
  }
});

test('repeat import: the same full work reports 已有 and never duplicates', async ({ page, request }) => {
  await deleteUnit(request, CAT_ZH, UNIT_IMPORT);
  try {
    const title = poemTitle();

    // First confirm stores the work.
    await openPhotoTab(page);
    await recognizeMixedAndCorrect(page, title);
    await page.getByRole('button', { name: /确认添加/ }).click();
    await expect(page.locator('.result-banner .result-summary'))
      .toContainText('已添加古诗 1 首', { timeout: 30_000 });

    // Re-recognize the same page with the SAME corrected title: the
    // preview classifies the whole text against the real library —
    // 已有, checkable (attribution only), never a second copy.
    await recognizeMixedAndCorrect(page, title);
    const card = page.locator('.photo-poem-card[data-poem-index="0"]');
    await expect(card).toHaveClass(/photo-poem-exists/);
    await expect(card.locator('.photo-poem-status')).toHaveText('已有（不会重复建立）');
    await expect(card.locator('.photo-poem-check')).toBeChecked();

    await page.getByRole('button', { name: /确认添加/ }).click();
    await expect(page.locator('.result-banner .result-summary'))
      .toContainText('古诗已有 1 首', { timeout: 30_000 });
    await expect(page.locator('.result-banner .result-detail', { hasText: '古诗已有' }))
      .toContainText('古诗已有（不会重复建立）：' + title);

    // Real storage: exactly ONE poem with this full text.
    expect(await poemRows(request, title)).toHaveLength(1);
  } finally {
    await deleteUnit(request, CAT_ZH, UNIT_IMPORT);
  }
});

test('unified content view: 古诗 type filter, attribution and progress scope management', async ({ page, request }) => {
  await deleteUnit(request, CAT_ZH, UNIT_IMPORT);
  try {
    const title = poemTitle();
    await openPhotoTab(page);
    await recognizeMixedAndCorrect(page, title);
    await page.getByRole('button', { name: /确认添加/ }).click();
    await expect(page.locator('.result-banner .result-summary'))
      .toContainText('已添加古诗 1 首', { timeout: 30_000 });

    // The unified content view: the 类型 pill 古诗 shows the whole work.
    await page.getByRole('tab', { name: '词库' }).click();
    await page.locator('.type-pills button', { hasText: '古诗' }).click();
    const poemCard = page.locator('.poem-card', { hasText: title });
    await expect(poemCard).toBeVisible({ timeout: 15_000 });
    await expect(poemCard.locator('.badge-chinese')).toHaveText('中文');
    await expect(poemCard.locator('.poem-line')).toHaveCount(2);
    await expect(poemCard.locator('.poem-line').nth(0)).toHaveText('床前明月光，疑是地上霜。');
    await expect(poemCard.locator('.poem-scope')).toHaveText('无教材归属（补充内容）');

    // Poems carry NO dictation surfaces: no participation toggle, no
    // selection checkbox, the bulk bar hidden — and the poems page keeps its
    // whole-work note (Issue #12 replaced the 后续更新开放 placeholder: the
    // 背默这首 entry on each in-scope card is the classroom door now).
    await expect(poemCard.locator('input')).toHaveCount(0);
    await expect(page.locator('.participation-bulk')).toBeHidden();
    await expect(page.locator('.list-note')).toContainText('进入范围后可在卡片上开始整首背默');

    // Subject filter: the 英语 pill hides the chinese poem.
    await page.locator('.category-pills button', { hasText: '英语' }).click();
    await expect(page.locator('.poem-card', { hasText: title })).toHaveCount(0);
    await page.locator('.category-pills button', { hasText: '中文' }).click();
    await expect(poemCard).toBeVisible({ timeout: 15_000 });

    // 归属管理: a real chinese textbook, the poem's own lesson.
    const book = await request.post('/api/v1/textbooks', {
      data: { name: BOOK_NAME, category: CAT_ZH, lessons: ['第5课'] },
    });
    const bookId = (await book.json()).id;
    expect(bookId).toBeTruthy();

    await poemCard.getByRole('button', { name: '管理归属' }).click();
    const select = page.locator('select[aria-label="教材"]');
    // selectOption auto-waits until the async textbook list lands.
    await select.selectOption({ label: BOOK_NAME });
    await page.locator('#poem-lesson-input').fill('第5课');
    await page.getByRole('button', { name: '保存归属' }).click();
    await expect(page.locator('.poem-card', { hasText: title })
      .locator('.textbook-chip', { hasText: BOOK_NAME })).toBeVisible({ timeout: 15_000 });

    // 已学进度规则仍适用: lesson 1 of 1 unlearned → progress-pending.
    const scoped = page.locator('.poem-card', { hasText: title });
    await expect(scoped.locator('.poem-scope')).toHaveText('课次尚未学到，暂不在学习范围');

    // Move the book's progress to 已学第5课 → the poem enters the scope.
    const progress = await request.put('/api/v1/textbooks/' + bookId, {
      data: { learnedLessons: 1 },
    });
    expect(progress.ok()).toBeTruthy();
    // A type toggle away and back refetches the list with the new verdict.
    await page.locator('.type-pills button', { hasText: '词语' }).click();
    await page.locator('.type-pills button', { hasText: '古诗' }).click();
    await expect(page.locator('.poem-card', { hasText: title }).locator('.poem-scope'))
      .toHaveText('已学课次内，随进度在范围内', { timeout: 15_000 });
  } finally {
    await deleteUnit(request, CAT_ZH, UNIT_IMPORT);
  }
});

test('textbook commit: poems ride the 新建教材 path with their own lesson and progress gate', async ({ page, request }) => {
  await deleteUnit(request, CAT_ZH, UNIT_IMPORT);
  try {
    const title = poemTitle();
    const panel = await openPhotoTab(page);
    await panel.locator('input[name="parent-category-photo"][value="chinese"]').check();
    await panel.locator('#parent-photo-file').setInputFiles(FIXTURE);
    await panel.getByRole('button', { name: '开始识别' }).click();
    const card = panel.locator('.photo-poem-card[data-poem-index="0"]');
    await expect(card).toBeVisible({ timeout: 30_000 });

    // The recognized header lands the panel on 新建教材 (name prefilled
    // from the meta; Issue #80 §2.2: with other 语文 books already on the
    // shared DB it stops at 请选择 instead — the parent picks 新建 either
    // way); the poem card gains its OWN 课次 input in textbook mode —
    // 第8课, distinct from the 词语's 第5课, proving per-poem lessons.
    const bookSelect = panel.locator('select[aria-label="教材归属"]');
    if ((await bookSelect.inputValue()) === 'unset') {
      await bookSelect.selectOption('new');
    }
    await panel.getByRole('combobox', { name: '单元', exact: true }).fill(UNIT_IMPORT);
    await card.locator('input[aria-label="诗题 第 1 首"]').fill(title);
    await card.locator('input[aria-label="课次 第 1 首"]').fill('第8课');
    await panel.getByRole('button', { name: /确认添加/ }).click();

    const summary = panel.locator('.result-banner .result-summary');
    await expect(summary).toContainText('新增古诗 1 首', { timeout: 30_000 });
    await expect(summary).toContainText('新增 2 个');
    await expect(panel.locator('.result-banner .result-detail', { hasText: '新增古诗' }))
      .toContainText('新增古诗：' + title);
    await expect(panel.locator('.result-banner')).toContainText('古诗按整首保存');

    // Real storage: the attribution landed with the poem's own lesson,
    // the poem lesson joined the book's lesson table, and the scope
    // verdict is progress-gated (已学 0 课 → 暂不在学习范围).
    const rows = await poemRows(request, title);
    expect(rows).toHaveLength(1);
    expect(rows[0].textbooks).toHaveLength(1);
    expect(rows[0].textbooks[0].lesson).toBe('第8课');
    expect(rows[0].in_scope).toBe(false);

    const booksResp = await request.get('/api/v1/textbooks?category=' + CAT_ZH);
    const books = (await booksResp.json()).textbooks || [];
    const book = books.find((b) => b.name === rows[0].textbooks[0].name);
    expect(book, 'the poem-attributed textbook exists').toBeTruthy();
    expect(book.lessons).toEqual(expect.arrayContaining(['第5课', '第8课']));

    // The unified content view shows the attribution chip + the gate.
    await page.getByRole('tab', { name: '词库' }).click();
    await page.locator('.type-pills button', { hasText: '古诗' }).click();
    const poemCard = page.locator('.poem-card', { hasText: title });
    await expect(poemCard).toBeVisible({ timeout: 15_000 });
    await expect(poemCard.locator('.textbook-chip')).toContainText('第8课');
    await expect(poemCard.locator('.poem-scope')).toHaveText('课次尚未学到，暂不在学习范围');
  } finally {
    await deleteUnit(request, CAT_ZH, UNIT_IMPORT);
  }
});
