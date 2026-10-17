/**
 * Parent scanned-PDF import E2E (Issue #7): 多页扫描 PDF 按页识别与逐页结果
 * 确认, proven against the ISOLATED real service — real browser (pdf.js
 * page split in-browser, ADR-0004) → real Go → real Python bridge → real
 * SQLite (per-run temp DB shared across the run; only the external vision
 * service is stubbed, answering each RENDERED page image byte-identically
 * with its canned reply — the page images themselves are the browser's own
 * canvas output, captured once via tests/e2e/pdf-capture.spec.js).
 *
 * Fixture families: 外研版 四年级上册 (pdf-textbook.pdf, 4 pages — page 3
 * repeats page 1's "pen" for the cross-page duplicate) and 北师大版 五年级
 * 上册 (pdf-retry.pdf, 3 pages — page 2's image carries a .failcount-4
 * marker covering the service's own vision retries, so the page fails at
 * the UI level exactly once).
 *
 * Acceptance link (issue #7), one test per observable scenario:
 *   1. 整本跨课次导入 — every page recognized through the SAME preview/
 *      edit/confirm flow, rows grouped under page heads, the cross-page
 *      duplicate flagged 与第 N 行重复 (never double-submitted); per-page
 *      lesson assignment (批量调整课次); the commit lands all words with
 *      their lessons in the real DB, and the real classroom follows the
 *      learned progress — 第1课 in, 第2/3课 out (AC-1/2/5/6/7).
 *   2. 部分页失败与重试 — page 2 fails with its reason on the chip, the
 *      completed pages keep their rows, the run is NEVER a whole-file
 *      success; 重试该页 re-runs only that page and the row lands back IN
 *      ORDER between its neighbours (AC-3/4).
 *   3. 大小／页数／无法读取的明确反馈 — an over-cap file is refused
 *      before anything is processed, an over-page-count PDF is refused at
 *      open, a damaged PDF surfaces 无法读取 (AC-3).
 */
// @ts-check
const { test, expect } = require('@playwright/test');
const path = require('node:path');
const {
  recognize, deleteUnit, wordRows, textbooks, awaitReceipt, practiceSelection,
} = require('./helpers/photo-flow');

const UNIT = 'e2e-pdf-import';

const FIXTURES = path.resolve(__dirname, 'fixtures');
const PDF_TEXTBOOK = path.join(FIXTURES, 'pdf-textbook.pdf');
const PDF_RETRY = path.join(FIXTURES, 'pdf-retry.pdf');
const PDF_TOOMANY = path.join(FIXTURES, 'pdf-toomany.pdf');

const BOOK_WAIYAN = '外研版·小学·四年级·上册·Module 1';
const BOOK_BEISHIDA = '北师大版·小学·五年级·上册·Unit 2';

/** The preview's word-cell values, in row order (AC-2 不乱序). */
async function wordValues(page) {
  const inputs = page.locator('tr.photo-row > td:nth-child(2) input');
  const n = await inputs.count();
  const values = [];
  for (let i = 0; i < n; i++) {
    values.push(await inputs.nth(i).inputValue());
  }
  return values;
}

test.describe('Issue #7 acceptance', () => {
test('whole textbook, cross-lesson: page-grouped preview, cross-page duplicate flagged, per-page lesson batch, real DB + classroom scope', async ({ page, request }) => {
  await deleteUnit(request, UNIT);
  try {
    await recognize(page, PDF_TEXTBOOK, UNIT);

    // All four pages done; the bar says so honestly (AC-3).
    await expect(page.locator('.pdf-page-chip[data-status="done"]')).toHaveCount(4, { timeout: 60_000 });
    await expect(page.locator('.pdf-summary-line')).toContainText('共 4 页：成功 4，失败 0');

    // Seven rows under four page heads, in PAGE order (AC-1/2).
    await expect(page.locator('tr.photo-row')).toHaveCount(7);
    await expect(page.locator('tr.pdf-page-head')).toHaveCount(4);
    expect(await wordValues(page)).toEqual([
      'pen', 'bag', // 第 1 页
      'cat', 'dog', // 第 2 页
      'bird', 'pen', // 第 3 页 (pen = the cross-page repeat)
      'fish', // 第 4 页
    ]);
    await expect(page.locator('tr.pdf-page-head').nth(0)).toContainText('第 1 页（2 条）');
    await expect(page.locator('tr.pdf-page-head').nth(3)).toContainText('第 4 页（1 条）');

    // The cross-page duplicate is invalid 与第 1 行重复 — never a second
    // submittable copy (AC-2).
    await expect(page.locator('tr.photo-row-invalid')).toHaveCount(1);
    await expect(page.locator('tr.photo-row-invalid')).toContainText('与第 1 行重复');

    // No existing textbooks: the panel starts at 新建教材, prefilled from
    // page 1's header — the book background is confirmed ONCE (AC-6).
    await expect(page.locator('#parent-photo-textbook')).toHaveValue('new');
    await expect(page.locator('#parent-photo-tb-name')).toHaveValue(BOOK_WAIYAN);

    // Per-page lesson assignment (批量调整课次, AC-6).
    const heads = page.locator('tr.pdf-page-head');
    const lessons = ['第1课', '第2课', '第2课', '第3课'];
    for (let i = 0; i < 4; i++) {
      await heads.nth(i).locator('.pdf-page-lesson').fill(lessons[i]);
      await heads.nth(i).locator('.pdf-page-lesson-apply').click();
    }
    await expect(page.locator('button:has-text("确认添加（6 个）")')).toBeEnabled();

    // Commit → receipt: six 新增, the duplicate honestly 未提交 (AC-2/US-45).
    await page.getByRole('button', { name: '确认添加（6 个）' }).click();
    await awaitReceipt(page, BOOK_WAIYAN, { 新增: 6, 未提交: 1 });
    await expect(page.locator('.result-banner')).toContainText('已指定课次「第1课」：pen、bag');
    await expect(page.locator('.result-banner')).toContainText('已指定课次「第2课」：cat、dog、bird');
    await expect(page.locator('.result-banner')).toContainText('已指定课次「第3课」：fish');

    // Real DB truth: six words, one 外研版 source each, lessons as
    // confirmed; the book's lesson list grew in confirmed order.
    const rows = await wordRows(request);
    const expected = { pen: '第1课', bag: '第1课', cat: '第2课', dog: '第2课', bird: '第2课', fish: '第3课' };
    for (const [word, lesson] of Object.entries(expected)) {
      const row = rows.get(word);
      expect(row, word).toBeTruthy();
      const mine = (row.textbooks || []).filter((t) => t.name === BOOK_WAIYAN);
      expect(mine.length, word).toBe(1);
      expect(mine[0].lesson, word).toBe(lesson);
    }
    const books = await textbooks(request);
    const book = books.find((b) => b.name === BOOK_WAIYAN);
    expect(book).toBeTruthy();
    expect(book.lessons).toEqual(['第1课', '第2课', '第3课']);

    // Classroom scope follows the learned progress (AC-7): 已学完第1课
    // admits pen/bag and keeps every later lesson out.
    const put = await request.put('/api/v1/textbooks/' + book.id, { data: { learnedLessons: 1 } });
    expect(put.ok()).toBeTruthy();
    const picked = await practiceSelection();
    expect(picked).toContain('pen');
    expect(picked).toContain('bag');
    for (const w of ['cat', 'dog', 'bird', 'fish']) {
      expect(picked, w).not.toContain(w);
    }
  } finally {
    await deleteUnit(request, UNIT);
  }
});

test('partial failure: page 2 fails with its reason, completed pages keep their rows, 重试该页 lands the page back in order', async ({ page, request }) => {
  await deleteUnit(request, UNIT);
  try {
    await recognize(page, PDF_RETRY, UNIT);

    // 2 done, 1 failed — the summary never claims a whole-file success.
    await expect(page.locator('.pdf-page-chip[data-status="done"]')).toHaveCount(2, { timeout: 60_000 });
    await expect(page.locator('.pdf-page-chip[data-status="failed"]')).toHaveCount(1);
    await expect(page.locator('.pdf-page-chip[data-status="failed"]')).toHaveAttribute('data-page', '2');
    await expect(page.locator('.pdf-summary-line')).toContainText('失败 1');
    await expect(page.locator('#parent-photo-form-error')).toContainText('有 1 页识别失败');

    // Completed pages' results are already on review (AC-4) — in order,
    // with NO duck row (its page failed).
    expect(await wordValues(page)).toEqual(['goat', 'frog', 'horse']);

    // Retry ONLY the failed page (AC-4): duck lands between frog and
    // horse — page order restored, nothing duplicated.
    await page.getByRole('button', { name: '重试第 2 页' }).click();
    await expect(page.locator('.pdf-page-chip[data-status="done"]')).toHaveCount(3, { timeout: 60_000 });
    await expect(page.locator('.pdf-page-chip[data-status="failed"]')).toHaveCount(0);
    expect(await wordValues(page)).toEqual(['goat', 'frog', 'duck', 'horse']);

    // The retried flow commits like any other (page attribution intact).
    const heads = page.locator('tr.pdf-page-head');
    const lessons = ['第1课', '第2课', '第3课'];
    for (let i = 0; i < 3; i++) {
      await heads.nth(i).locator('.pdf-page-lesson').fill(lessons[i]);
      await heads.nth(i).locator('.pdf-page-lesson-apply').click();
    }
    await expect(page.locator('#parent-photo-textbook')).toHaveValue('new');
    await page.getByRole('button', { name: '确认添加（4 个）' }).click();
    await awaitReceipt(page, BOOK_BEISHIDA, { 新增: 4 });

    const rows = await wordRows(request);
    expect(rows.get('duck')).toBeTruthy();
    const mine = (rows.get('duck').textbooks || []).filter((t) => t.name === BOOK_BEISHIDA);
    expect(mine[0].lesson).toBe('第2课');
  } finally {
    await deleteUnit(request, UNIT);
  }
});

test('limits: over-cap size refused at pick, over-cap page count refused at open, damaged PDF says unreadable — nothing processed', async ({ page, request }) => {
  await deleteUnit(request, UNIT);
  try {
    await page.goto('/parent.html');
    await page.getByRole('tab', { name: '词库' }).click();
    await page.locator('#addSection').waitFor();
    await page.getByRole('tab', { name: '照片导入' }).click();
    const panel = page.locator('#add-panel-photo');
    await panel.waitFor();

    // 40MiB + 1: refused BEFORE the file is loaded (AC-3) — no recognize
    // button, no chips, nothing sent.
    await page.locator('#parent-photo-file').setInputFiles({
      name: 'big.pdf',
      mimeType: 'application/pdf',
      buffer: Buffer.alloc(40 * 1024 * 1024 + 1, 1),
    });
    await expect(page.locator('#parent-photo-form-error')).toContainText('PDF 超过 40MB 上限');
    await expect(page.getByRole('button', { name: '开始识别' })).toBeDisabled();

    // 101 pages: the file loads, the page count is over the cap — the
    // honest refusal, no page ever runs. (Issue #10: the subject radio is
    // required before 开始识别 does anything — 英语, this suite's subject.)
    await panel.locator('input[name="parent-category-photo"][value="english"]').check();
    await page.locator('#parent-photo-file').setInputFiles(PDF_TOOMANY);
    await page.getByRole('button', { name: '开始识别' }).click();
    await expect(page.locator('#parent-photo-form-error')).toContainText('超过 100 页上限', { timeout: 30_000 });
    await expect(page.locator('.pdf-page-chip')).toHaveCount(0);

    // A damaged PDF (random bytes, valid size): 无法读取, nothing processed.
    await page.locator('#parent-photo-file').setInputFiles({
      name: 'broken.pdf',
      mimeType: 'application/pdf',
      buffer: Buffer.from('this is not a pdf at all, just bytes'),
    });
    await page.getByRole('button', { name: '开始识别' }).click();
    await expect(page.locator('#parent-photo-form-error')).toContainText('无法读取这个 PDF', { timeout: 30_000 });
    await expect(page.locator('.pdf-page-chip')).toHaveCount(0);
    await expect(page.locator('tr.photo-row')).toHaveCount(0);
  } finally {
    await deleteUnit(request, UNIT);
  }
});
});
