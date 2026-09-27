/**
 * Parent management tracer — the Phase 9 Plan 01 minimum end-to-end flow:
 *
 *   家长打开 /parent.html → 填单词 → 选分类 → 点「添加单词」→
 *   POST /api/v1/words → pybridge → vocab_manager.py add → SQLite →
 *   GET /api/v1/words → 浏览词库 list renders the new word (no reload)
 *
 * Every selector below is read from the real frontend copy (UI-SPEC
 * Copywriting Contract):
 *   - pkg/web/static/parent.html        (section titles 添加单词/浏览词库)
 *   - pkg/web/static/js/parent-modes.js (labels 单词/单元/添加单词, radios 英语/中文)
 *
 * The dedicated `e2e-tracer` unit value exists so Plan 09-08's cleanup
 * pass can delete these leftover rows once the delete endpoint ships.
 */
// @ts-check
const { test, expect } = require('@playwright/test');

test('tracer: parent adds one word from parent.html and sees it in the browse list', async ({ page }) => {
  const word = `tracer-${Date.now()}`;

  await page.goto('/parent.html');

  // The add form and browse card both render.
  const addSection = page.locator('#addSection');
  const browseSection = page.locator('#browseSection');
  await expect(addSection).toBeVisible();
  await expect(browseSection).toBeVisible();

  // Fill 单词 (unique value), select the 英语 category radio (no
  // pre-selection per D-06), tag the unit e2e-tracer. Locators are
  // exact-match role queries: the section title 添加单词 also contains
  // the substring 单词, so substring matching is ambiguous.
  await page.getByRole('textbox', { name: '单词', exact: true }).fill(word);
  await page.getByRole('radio', { name: '英语' }).check();
  // The 单元 input carries a datalist association, so its ARIA role is
  // combobox, not textbox.
  await page.getByRole('combobox', { name: '单元', exact: true }).fill('e2e-tracer');

  // Submit through the real REST → pybridge → Python → SQLite path.
  await page.getByRole('button', { name: '添加单词' }).click();

  // Success toast appears (已添加) and the form is re-enabled.
  await expect(page.locator('.toast-success')).toHaveText('已添加', { timeout: 15_000 });

  // The word appears in the 浏览词库 section WITHOUT a page reload.
  await expect(
    browseSection.locator(`.word-row[data-word="${word}"]`),
    'the newly added word must render in the browse list without reload',
  ).toBeVisible({ timeout: 15_000 });
  await expect(
    browseSection.locator(`.word-row[data-word="${word}"] .word-text`),
  ).toHaveText(word);
});
