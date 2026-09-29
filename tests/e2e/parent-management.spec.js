/**
 * Parent management E2E (Phase 9).
 *
 *   tracer (Plan 09-01): the minimum end-to-end flow —
 *     家长打开 /parent.html → 填单词 → 选分类 → 点「添加单词」→
 *     POST /api/v1/words → pybridge → vocab_manager.py add → SQLite →
 *     GET /api/v1/words → 浏览词库 list renders the new word (no reload)
 *
 *   full flow (Plan 09-08 Task 1): the complete management flow through the
 *     REAL service — batch add (preview → commit with the duplicate skip
 *     surfaced) → group browse + collapse persistence → search → edit →
 *     rename → delete one word → settings save/restore → cleanup deletes
 *     the e2e units so the library is left as found.
 *
 * Every selector below is read from the real frontend copy (UI-SPEC
 * Copywriting Contract):
 *   - pkg/web/static/parent.html        (section titles 添加单词/浏览词库)
 *   - pkg/web/static/js/parent-modes.js (labels 单词/单元/添加单词, radios 英语/中文,
 *                                         batch tab copy 解析并预览/确认添加/结果横幅)
 *   - pkg/web/static/js/parent-browse.js (toolbar/组头/行操作/modal 文案)
 *
 * Test-data hygiene (T-09-12): all writes are confined to the dedicated
 * `e2e-*` units; every test removes them in a finally-shaped API cleanup,
 * so the suite is idempotent across consecutive runs and a mid-flow
 * failure still leaves the family library as found.
 */
// @ts-check
const { test, expect } = require('@playwright/test');

const CAT = 'english';
const UNIT_TRACER = 'e2e-tracer';
const UNIT_FULL = 'e2e-full';
const UNIT_FULL_RENAMED = 'e2e-full-2';

/** DELETE one whole unit through the real API. A 404 (already gone) is fine. */
async function deleteUnit(request, unit, category = CAT) {
  await request.delete('/api/v1/words', { data: { category, unit } }).catch(() => {});
}

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

test('full flow: batch add → browse → search → edit → rename → delete → settings → cleanup', async ({ page, request }) => {
  // -- Setup: pre-clean any crashed earlier run's e2e-full leftovers, then
  //    seed the deterministic duplicate target. The batch paste below
  //    re-includes this word, so the server MUST skip it (skips are
  //    UNIQUE(word, category)) and the banner shows the honest counts.
  await deleteUnit(request, UNIT_FULL);
  await deleteUnit(request, UNIT_FULL_RENAMED);
  const seed = await request.post('/api/v1/words', {
    data: {
      category: CAT,
      unit: UNIT_FULL,
      entries: [{ word: 'e2e-dup-seed', meaning: '重复种子' }],
    },
  });
  expect(seed.ok(), 'seed POST must succeed').toBeTruthy();

  try {
    await page.goto('/parent.html');
    const addSection = page.locator('#addSection');
    const browseSection = page.locator('#browseSection');
    await expect(addSection).toBeVisible();
    await expect(browseSection).toBeVisible();

    // ----------------------------------------------------------------
    // 1. Batch add (D-10): 4 pasted lines = 1 comment + 1 duplicate of
    //    an existing word + 2 new words, all into unit e2e-full.
    // ----------------------------------------------------------------
    await page.getByRole('tab', { name: '批量添加' }).click();
    const batchPanel = page.locator('#add-panel-batch');
    await batchPanel.getByRole('radio', { name: '英语' }).check();
    await batchPanel.getByRole('combobox', { name: '单元', exact: true }).fill(UNIT_FULL);
    const paste = [
      '# 注释行：解析时应被忽略',
      'e2e-dup-seed\t重复的已有单词',
      'e2e-apple\t苹果',
      'e2e-banana\t香蕉',
    ].join('\n');
    await batchPanel.locator('#parent-batch-paste').fill(paste);
    await batchPanel.getByRole('button', { name: '解析并预览' }).click();

    // The comment line is dropped by the parser; 3 valid rows are
    // numbered and none carries a per-line Warning.
    await expect(batchPanel.locator('.preview-row')).toHaveCount(3);
    await expect(batchPanel.locator('.preview-row-warning')).toHaveCount(0);
    await expect(
      batchPanel.locator('.preview-row').first().locator('.preview-word'),
    ).toHaveText('e2e-dup-seed');
    const commitBtn = batchPanel.getByRole('button', { name: '确认添加（3 个）' });
    await expect(commitBtn).toBeEnabled();
    await commitBtn.click();

    // Banner: server counts — 2 added, 1 skipped (the seeded duplicate);
    // skipped>0 renders the Warning variant and NAMES the skipped word.
    const banner = batchPanel.locator('.result-banner');
    await expect(banner).toHaveClass(/result-banner-warning/);
    await expect(banner.locator('.result-summary')).toHaveText('已添加 2 个，跳过 1 个重复单词');
    await expect(banner.locator('.result-detail')).toHaveText('重复单词：e2e-dup-seed');

    // ----------------------------------------------------------------
    // 2. Browse: the e2e-full group header shows its count badge; the
    //    collapsed state survives a reload (localStorage).
    // ----------------------------------------------------------------
    const fullGroup = browseSection.locator(`.unit-group[data-unit="${UNIT_FULL}"]`);
    await expect(fullGroup.locator('.unit-count')).toHaveText('3 个单词');

    const fullBody = fullGroup.locator('.unit-body');
    await fullGroup.locator('.unit-toggle').click();
    await expect(fullBody).toBeHidden();
    await expect(fullGroup.locator('.unit-toggle')).toHaveAttribute('aria-expanded', 'false');

    await page.reload();
    await expect(fullGroup).toBeVisible();
    await expect(fullBody, 'collapse state must persist across reload (localStorage)').toBeHidden();
    await expect(fullGroup.locator('.unit-toggle')).toHaveAttribute('aria-expanded', 'false');

    // Expand again for the following steps.
    await fullGroup.locator('.unit-toggle').click();
    await expect(fullBody).toBeVisible();

    // ----------------------------------------------------------------
    // 3. Search narrows the group list to the match; ✕ restores.
    // ----------------------------------------------------------------
    const groupsBefore = await page.evaluate(() =>
      Array.from(document.querySelectorAll('.unit-group')).map((g) => g.getAttribute('data-unit')));

    await browseSection.locator('.search-input').fill('e2e-apple');
    // 300ms debounce → GET /words?q= → re-render (skeleton in between).
    await expect(browseSection.locator('.unit-group')).toHaveCount(1);
    await expect(browseSection.locator('.word-row[data-word="e2e-apple"]')).toBeVisible();

    await browseSection.locator('.search-clear').click();
    await expect(browseSection.locator('.unit-group')).toHaveCount(groupsBefore.length);
    const groupsAfter = await page.evaluate(() =>
      Array.from(document.querySelectorAll('.unit-group')).map((g) => g.getAttribute('data-unit')));
    expect(groupsAfter, 'clearing the search restores the full group list').toEqual(groupsBefore);

    // ----------------------------------------------------------------
    // 4. Edit: change 意思 on one row; the row text updates + toast.
    // ----------------------------------------------------------------
    const appleRow = browseSection.locator('.word-row[data-word="e2e-apple"]');
    await appleRow.getByRole('button', { name: '编辑' }).click();

    const editDialog = page.getByRole('dialog', { name: '编辑单词' });
    await expect(editDialog).toBeVisible();
    await editDialog.locator('#edit-meaning').fill('苹果（已编辑）');
    await editDialog.getByRole('button', { name: '保存修改' }).click();

    await expect(page.locator('.toast-success')).toHaveText('已保存');
    await expect(appleRow.locator('.word-meaning')).toHaveText('苹果（已编辑）');

    // ----------------------------------------------------------------
    // 5. Rename the unit e2e-full → e2e-full-2.
    // ----------------------------------------------------------------
    await fullGroup.locator('.unit-action', { hasText: '重命名' }).click();

    const renameDialog = page.getByRole('dialog', { name: '重命名单元' });
    await expect(renameDialog).toBeVisible();
    await expect(renameDialog.locator('.modal-message')).toHaveText(`将「${UNIT_FULL}」重命名为`);
    await renameDialog.locator('#rename-unit-input').fill(UNIT_FULL_RENAMED);
    await renameDialog.getByRole('button', { name: '确认重命名' }).click();

    await expect(page.locator('.toast-success')).toHaveText('已重命名');
    const renamedGroup = browseSection.locator(`.unit-group[data-unit="${UNIT_FULL_RENAMED}"]`);
    await expect(renamedGroup).toBeVisible();
    await expect(browseSection.locator(`.unit-group[data-unit="${UNIT_FULL}"]`)).toHaveCount(0);

    // ----------------------------------------------------------------
    // 6. Delete one word via its row confirm dialog (exact-word copy);
    //    the row disappears and the count badge decrements.
    // ----------------------------------------------------------------
    const bananaRow = browseSection.locator('.word-row[data-word="e2e-banana"]');
    await bananaRow.getByRole('button', { name: '删除' }).click();

    const wordDialog = page.getByRole('dialog', { name: '删除单词' });
    await expect(wordDialog.locator('.modal-message')).toHaveText(
      '确认删除「e2e-banana」？它的学习记录会一起删除，无法恢复。');
    await wordDialog.getByRole('button', { name: '确认删除' }).click();

    await expect(page.locator('.toast-success')).toHaveText('已删除');
    await expect(bananaRow).toHaveCount(0);
    await expect(renamedGroup.locator('.unit-count')).toHaveText('2 个单词');

    // ----------------------------------------------------------------
    // 7. Settings: 记忆保持率 → 0.90, save, reload reflects it (slider +
    //    GET), then restore defaults via the UI and save so the config
    //    file returns to 0.85/180.
    // ----------------------------------------------------------------
    const retention = page.locator('#settings-retention');
    await retention.evaluate((el) => {
      el.value = '0.9';
      el.dispatchEvent(new Event('input', { bubbles: true }));
      el.dispatchEvent(new Event('change', { bubbles: true }));
    });
    await expect(
      page.locator('.slider-field', { has: retention }).locator('.slider-readout'),
    ).toHaveText('当前 0.90 · 默认 0.85');

    await page.getByRole('button', { name: '保存设置' }).click();
    await expect(page.locator('.toast-success')).toHaveText('已保存，下次听写生效');

    await page.reload();
    const saved = await (await request.get('/api/v1/settings/fsrs')).json();
    expect(saved.desired_retention, 'GET must reflect the saved 0.90').toBeCloseTo(0.9, 8);
    await expect(page.locator('#settings-retention')).toHaveValue('0.9');
    await expect(
      page.locator('.slider-field', { has: retention }).locator('.slider-readout'),
    ).toHaveText('当前 0.90 · 默认 0.85');

    await page.getByRole('button', { name: '恢复默认' }).click();
    await expect(page.locator('#settings-retention')).toHaveValue('0.85');
    await page.getByRole('button', { name: '保存设置' }).click();
    await expect(page.locator('.toast-success')).toHaveText('已保存，下次听写生效');
    const restored = await (await request.get('/api/v1/settings/fsrs')).json();
    expect(restored, 'settings file must return to the documented defaults').toEqual({
      desired_retention: 0.85,
      maximum_interval: 180,
    });

    // ----------------------------------------------------------------
    // 8. Cleanup via the UI: delete e2e-full-2 (exact-count copy), then
    //    the Plan 09-01 e2e-tracer leftovers the same way. The finally
    //    block below is the API backstop.
    // ----------------------------------------------------------------
    await renamedGroup.locator('.unit-action', { hasText: '删除' }).click();
    const unitDialog = page.getByRole('dialog', { name: '删除单元' });
    await expect(unitDialog.locator('.modal-message')).toHaveText(
      `确认删除「${UNIT_FULL_RENAMED}」的全部 2 个单词？学习记录会一起删除，无法恢复。`);
    await unitDialog.getByRole('button', { name: '确认删除' }).click();
    await expect(page.locator('.toast-success')).toHaveText('已删除 2 个单词');
    await expect(renamedGroup).toHaveCount(0);

    const tracerGroup = browseSection.locator(`.unit-group[data-unit="${UNIT_TRACER}"]`);
    if ((await tracerGroup.count()) > 0) {
      const badge = await tracerGroup.locator('.unit-count').textContent();
      const n = parseInt(badge, 10);
      await tracerGroup.locator('.unit-action', { hasText: '删除' }).click();
      const tracerDialog = page.getByRole('dialog', { name: '删除单元' });
      await expect(tracerDialog.locator('.modal-message')).toHaveText(
        `确认删除「${UNIT_TRACER}」的全部 ${n} 个单词？学习记录会一起删除，无法恢复。`);
      await tracerDialog.getByRole('button', { name: '确认删除' }).click();
      await expect(tracerGroup).toHaveCount(0);
    }
  } finally {
    // Net-zero guarantee (T-09-12): whatever failed above, no e2e unit
    // survives this test. A 404 for an already-deleted unit is fine.
    await deleteUnit(request, UNIT_FULL);
    await deleteUnit(request, UNIT_FULL_RENAMED);
    await deleteUnit(request, UNIT_TRACER);
  }
});
