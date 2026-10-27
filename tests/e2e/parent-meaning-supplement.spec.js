/**
 * Parent meaning-supplement E2E (Issue #8): 缺失英语释义按教材语境补充并
 * 标明来源, proven against the ISOLATED real service — real browser →
 * real Go service → real Python bridge → real SQLite (per-run temp DB,
 * SHARED by every test in the run). Only the EXTERNAL services are
 * stubbed: recognition by fixture-matched canned vision replies (Issue #3
 * boundary), generation by the new text-*.reply.json prompt-matched
 * fixtures in the same mimo stub (ADR-0005).
 *
 * Test-data isolation: the per-run DB keeps textbooks across tests, and
 * the attribution preselect matches EXISTING books by meta — each test
 * therefore works on its OWN meta family (外研版 / 北师大版 / 陕旅版) so a
 * book another test created can never hijack a preselect (the #5/#6/#7
 * suites' rule).
 *
 * Acceptance link (issue #8), one test per observable scenario:
 *   1. 材料原文优先＋来源标记＋真实写入 — two rows carry the photo's own
 *      meanings, two are blank; 补充缺失释义 fills ONLY the blanks with
 *      the 系统补充 badge; the commit writes real rows whose GET /words
 *      meaning_source separates material from generated, and the browse
 *      list keeps showing the marker afterwards (AC-1/AC-2; 后续查看).
 *   2. 编辑保留补充来源、取消落回缺失、保留缺失可提交 — the reply
 *      deliberately answers deer+goat but NOT wolf: a parent edit keeps
 *      the generated marker, 取消补充 clears one row back to missing,
 *      and the never-answered row still commits as an empty material
 *      meaning (AC-4/AC-5 保留缺失).
 *   3. 生成失败如实显示、重试成功 — the stub's failcount marker leads
 *      with 500s (covering the client's own 4 attempts), the form says
 *      释义生成失败 honestly with nothing faked, and the 重试生成 click
 *      then succeeds (AC-5).
 */
// @ts-check
const { test, expect } = require('@playwright/test');
const path = require('node:path');
const { recognize, deleteWords, wordRows, awaitReceipt } = require('./helpers/photo-flow');

// This suite's whole vocabulary (all three fixtures). The textbook-mode
// commit writes NO unit tag (Issue #89), so the exact word list is the
// suite's isolation key — before AND after each test.
const WORDS = ['mango', 'peach', 'kiwi', 'melon', 'camel', 'deer', 'goat', 'wolf', 'corn', 'beanpod'];

const FIXTURES = path.resolve(__dirname, 'fixtures');
const FIXTURE_WAIYAN = path.join(FIXTURES, 'photo-meaning-waiyan.png');
const FIXTURE_BSDA = path.join(FIXTURES, 'photo-meaning-bsda.png');
const FIXTURE_SHANLV = path.join(FIXTURES, 'photo-meaning-shanlv.png');

const BOOK_WAIYAN = '外研版·四年级·上册·Unit 2';
const BOOK_BSDA = '北师大版·五年级·上册·Unit 1';
const BOOK_SHANLV = '陕旅版·三年级·上册·Unit 4';

/** One preview row's meaning input (rows are 1-labeled, 0-indexed). */
function meaningInput(page, row) {
  return page.locator(`tr[data-row-index="${row}"] input[aria-label^="释义"]`);
}

/** The supplement button (label depends on pending/failed counts). */
function genButton(page) {
  return page.locator('.photo-meaning-gen-btn');
}

test.describe('Issue #8 acceptance', () => {
test('meaning supplement: fills only the blanks, marks them, and the real write keeps the source', async ({ page, request }) => {
  await deleteWords(request, WORDS);
  try {
    await recognize(page, FIXTURE_WAIYAN);

    // The read header prefills the 新建教材 form (its own meta family).
    await expect(page.locator('#parent-photo-textbook')).toHaveValue('new');
    await expect(page.locator('#parent-photo-tb-name')).toHaveValue(BOOK_WAIYAN);

    // The affordance names exactly the two blank rows (AC-4 trigger).
    await expect(genButton(page)).toContainText('补充缺失释义（2 词）');

    await genButton(page).click();

    // AC-1: the photo's own meanings survive untouched (材料原文优先).
    await expect(meaningInput(page, 0)).toHaveValue('芒果');
    await expect(meaningInput(page, 1)).toHaveValue('桃子');
    // AC-2: ONLY the supplemented rows carry the marker.
    await expect(meaningInput(page, 2)).toHaveValue('n. 猕猴桃');
    await expect(meaningInput(page, 3)).toHaveValue('n. 瓜，甜瓜');
    await expect(page.locator('tr[data-row-index="2"] .meaning-source-badge')).toBeVisible();
    await expect(page.locator('tr[data-row-index="3"] .meaning-source-badge')).toBeVisible();
    // The marker cells exist on every row (hidden when not applicable) —
    // pin VISIBILITY, not node count.
    await expect(page.locator('tr[data-row-index="0"] .meaning-source-badge')).toBeHidden();
    await expect(page.locator('tr[data-row-index="1"] .meaning-source-badge')).toBeHidden();

    await page.getByRole('button', { name: '确认添加（4 个）' }).click();
    await awaitReceipt(page, BOOK_WAIYAN, { 新增: 4 });

    // Real write: GET /words separates the sources (AC-2 真实写入).
    const words = await wordRows(request);
    expect(words.get('kiwi').meaning).toBe('n. 猕猴桃');
    expect(words.get('kiwi').meaning_source).toBe('generated');
    expect(words.get('melon').meaning_source).toBe('generated');
    expect(words.get('mango').meaning_source).toBe('material');
    expect(words.get('peach').meaning_source).toBe('material');

    // 后续查看: the browse rows keep showing the 系统 badge.
    await expect(page.locator('[data-word="kiwi"] .word-src-badge')).toBeVisible();
    await expect(page.locator('[data-word="melon"] .word-src-badge')).toBeVisible();
    await expect(page.locator('[data-word="mango"] .word-src-badge')).toHaveCount(0);
  } finally {
    await deleteWords(request, WORDS);
  }
});

test('meaning supplement: parent edit keeps the marker, cancel falls back to missing, and a never-answered row still commits', async ({ page, request }) => {
  await deleteWords(request, WORDS);
  try {
    await recognize(page, FIXTURE_BSDA);
    await expect(page.locator('#parent-photo-tb-name')).toHaveValue(BOOK_BSDA);
    await expect(genButton(page)).toContainText('补充缺失释义（3 词）');

    await genButton(page).click();

    // The reply answers deer+goat only — wolf is an honest per-word miss.
    await expect(meaningInput(page, 1)).toHaveValue('n. 鹿');
    await expect(meaningInput(page, 2)).toHaveValue('n. 山羊');
    await expect(page.locator('#parent-photo-form-error')).toContainText('1 个词未生成释义');
    await expect(meaningInput(page, 3)).toHaveValue('');
    await expect(page.locator('tr[data-row-index="3"] .photo-gen-failed-note'))
      .toContainText('释义生成失败，可重试或保留缺失');

    // AC-4 编辑: a parent correction keeps the supplement marker.
    await meaningInput(page, 1).fill('n. 小鹿（家长改）');
    await expect(page.locator('tr[data-row-index="1"] .meaning-source-badge')).toBeVisible();

    // AC-4 取消: back to missing — badge gone, meaning empty.
    await page.locator('tr[data-row-index="2"] .meaning-cancel-btn').click();
    await expect(meaningInput(page, 2)).toHaveValue('');
    await expect(page.locator('tr[data-row-index="2"] .meaning-source-badge')).toBeHidden();

    // AC-5 保留缺失: the never-answered row commits as-is, no fake.
    await page.getByRole('button', { name: '确认添加（4 个）' }).click();
    await awaitReceipt(page, BOOK_BSDA, { 新增: 4 });

    const words = await wordRows(request);
    expect(words.get('deer').meaning).toBe('n. 小鹿（家长改）');
    expect(words.get('deer').meaning_source).toBe('generated'); // 编辑不改来源
    expect(words.get('goat').meaning).toBe('');
    expect(words.get('goat').meaning_source).toBe('material'); // 取消落回缺失
    expect(words.get('wolf').meaning).toBe('');
    expect(words.get('wolf').meaning_source).toBe('material'); // 保留缺失
    expect(words.get('camel').meaning_source).toBe('material');
  } finally {
    await deleteWords(request, WORDS);
  }
});

test('meaning supplement: a failed generation is reported honestly and the retry succeeds', async ({ page, request }) => {
  await deleteWords(request, WORDS);
  try {
    await recognize(page, FIXTURE_SHANLV);
    await expect(page.locator('#parent-photo-tb-name')).toHaveValue(BOOK_SHANLV);
    await expect(genButton(page)).toContainText('补充缺失释义（1 词）');

    // The stub's failcount marker sinks the click INCLUDING the client's
    // own retries — the honest 502 reaches the form, nothing is faked.
    await genButton(page).click();
    await expect(page.locator('#parent-photo-form-error')).toContainText('释义生成失败', { timeout: 30_000 });
    await expect(meaningInput(page, 1)).toHaveValue('');
    await expect(page.locator('tr[data-row-index="1"] .meaning-source-badge')).toBeHidden();

    // The affordance switched to 重试生成 naming the same missing word.
    await expect(genButton(page)).toContainText('重试生成（1 词）');

    await genButton(page).click();
    await expect(meaningInput(page, 1)).toHaveValue('n. 豆荚');
    await expect(page.locator('tr[data-row-index="1"] .meaning-source-badge')).toBeVisible();

    await page.getByRole('button', { name: '确认添加（2 个）' }).click();
    await awaitReceipt(page, BOOK_SHANLV, { 新增: 2 });

    const words = await wordRows(request);
    expect(words.get('beanpod').meaning).toBe('n. 豆荚');
    expect(words.get('beanpod').meaning_source).toBe('generated');
  } finally {
    await deleteWords(request, WORDS);
  }
});
});
