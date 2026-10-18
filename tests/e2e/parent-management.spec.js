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
 *   - pkg/web/static/parent.html        (page tabs 学习记录/词库/设置 +
 *                                         section titles 添加单词/浏览词库)
 *   - pkg/web/static/js/parent-modes.js (labels 单词/单元/添加单词, radios 英语/中文,
 *                                         batch tab copy 解析并预览/确认添加/结果横幅)
 *   - pkg/web/static/js/parent-browse.js (toolbar/组头/行操作/modal 文案)
 *
 * 13.3-03: parent.html is now a page-level three-tab workspace with
 * 学习记录 as the DEFAULT tab. Every flow that touches the vocabulary
 * therefore clicks the 词库 tab after page.goto (exact copy locator), and
 * every reload resets to 学习记录 — flows re-click the tab they need.
 *
 * Test-data hygiene (T-09-12): all writes are confined to the dedicated
 * `e2e-*` units; every test removes them in a finally-shaped API cleanup,
 * so the suite is idempotent across consecutive runs and a mid-flow
 * failure still leaves the family library as found. The global
 * fsrs_config.json is snapshotted before the full flow and restored in
 * its finally block (WR-04) — settings are not unit-scoped.
 */
// @ts-check
const { test, expect } = require('@playwright/test');

const CAT = 'english';
const UNIT_TRACER = 'e2e-tracer';
const UNIT_FULL = 'e2e-full';
const UNIT_FULL_RENAMED = 'e2e-full-2';
const UNIT_PARTICIPATION_FAILURE = 'e2e-participation-failure';

/** DELETE one whole unit through the real API. A 404 (already gone) is fine. */
async function deleteUnit(request, unit, category = CAT) {
  await request.delete('/api/v1/words', { data: { category, unit } }).catch(() => {});
}

test('tracer: parent adds one word from parent.html and sees it in the browse list', async ({ page }) => {
  const word = `tracer-${Date.now()}`;

  await page.goto('/parent.html');

  // 13.3-03: the page opens on 学习记录 — the vocabulary lives on the 词库 tab.
  await page.getByRole('tab', { name: '词库' }).click();

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

  // Success toast appears (已添加 + the 13.3-03 selection note) and the
  // form is re-enabled.
  await expect(page.locator('.toast-success')).toHaveText(
    '已添加。新词将按复习安排选入课堂，不一定是下一课。', { timeout: 15_000 });

  // The word appears in the 浏览词库 section WITHOUT a page reload.
  await expect(
    browseSection.locator(`.word-row[data-word="${word}"]`),
    'the newly added word must render in the browse list without reload',
  ).toBeVisible({ timeout: 15_000 });
  await expect(
    browseSection.locator(`.word-row[data-word="${word}"] .word-text`),
  ).toHaveText(word);
});

test('participation failure restores the persisted state', async ({ page, request }) => {
  await deleteUnit(request, UNIT_PARTICIPATION_FAILURE);
  const word = `e2e-participation-${Date.now()}`;
  const seed = await request.post('/api/v1/words', {
    data: { category: CAT, unit: UNIT_PARTICIPATION_FAILURE, entries: [{ word }] },
  });
  expect(seed.ok()).toBeTruthy();
  try {
    await page.goto('/parent.html');
    await page.getByRole('tab', { name: '词库' }).click();
    const row = page.locator(`.word-row[data-word="${word}"]`);
    const toggle = row.getByRole('checkbox', { name: `加入默写范围 ${word}` });
    await expect(toggle).toBeChecked();
    await page.route('**/api/v1/words/participation', async (route) => {
      await route.fulfill({ status: 502, contentType: 'application/json', body: '{"error":{"code":"python_failed","message":"temporary"}}' });
    }, { times: 1 });
    await toggle.uncheck();
    // The failure copy is per-direction: an uncheck failure says
    // 移出默写范围失败：… (NOT the UAT doc's 更新失败：), followed by the
    // honest 已重新读取当前状态。 and a rollback to the persisted state.
    await expect(page.locator('.toast-error')).toContainText('移出默写范围失败：');
    await expect(page.locator('.toast-error')).toContainText('已重新读取当前状态');
    await expect(toggle).toBeChecked();

    // The symmetric 加入 direction carries its own prefix — prove it too.
    // Persist a real removal first so the following check actually fires a
    // change event, then intercept that one request.
    await toggle.uncheck();
    await expect(page.locator('.toast-success')).toHaveText('已移出默写范围');
    await page.route('**/api/v1/words/participation', async (route) => {
      await route.fulfill({ status: 502, contentType: 'application/json', body: '{"error":{"code":"python_failed","message":"temporary"}}' });
    }, { times: 1 });
    await toggle.check();
    await expect(page.locator('.toast-error')).toContainText('加入默写范围失败：');
    await expect(page.locator('.toast-error')).toContainText('已重新读取当前状态');
    await expect(toggle).not.toBeChecked();
  } finally {
    await deleteUnit(request, UNIT_PARTICIPATION_FAILURE);
  }
});

test('full flow: batch add → browse → search → edit → rename → delete → settings → cleanup', async ({ page, request }) => {
  // WR-04: fsrs_config.json is a GLOBAL file (outside the e2e-* unit
  // namespace), so snapshot the settings as-found and restore them in the
  // finally block — never clobber a parent's saved values with doc
  // defaults when the suite runs against a real service/DataDir.
  const settingsBefore = await (await request.get('/api/v1/settings/fsrs')).json();

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
    // 13.3-03: the page opens on 学习记录 — the vocabulary lives on the 词库 tab.
    await page.getByRole('tab', { name: '词库' }).click();
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
    // 13.3-03: a reload resets to the default 学习记录 tab — back to 词库.
    await page.getByRole('tab', { name: '词库' }).click();
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
    // 4. Participation: single and bulk changes persist through the real
    //    service. Bulk restore leaves the following management flow intact.
    // ----------------------------------------------------------------
    const appleRow = browseSection.locator('.word-row[data-word="e2e-apple"]');
    const appleParticipation = appleRow.getByRole('checkbox', { name: '加入默写范围 e2e-apple' });
    await appleParticipation.uncheck();
    await expect(page.locator('.toast-success')).toHaveText('已移出默写范围');
    await expect(appleParticipation).not.toBeChecked();

    await appleRow.getByRole('checkbox', { name: '选择 e2e-apple' }).check();
    const participationBananaRow = browseSection.locator('.word-row[data-word="e2e-banana"]');
    await participationBananaRow.getByRole('checkbox', { name: '选择 e2e-banana' }).check();
    await browseSection.getByRole('button', { name: '加入默写' }).click();
    await expect(page.locator('.toast-success')).toHaveText('已加入默写范围');
    await expect(appleParticipation).toBeChecked();
    await expect(participationBananaRow.getByRole('checkbox', { name: '加入默写范围 e2e-banana' })).toBeChecked();

    const persisted = await (await request.get('/api/v1/words?unit=' + UNIT_FULL)).json();
    expect(persisted.words.filter((word) => word.word === 'e2e-apple' || word.word === 'e2e-banana')
      .every((word) => word.participates === true)).toBeTruthy();

    // AC「刷新后仍保持」— a real page reload (not just an API re-read) must
    // re-render the persisted toggles.
    await page.reload();
    await page.getByRole('tab', { name: '词库' }).click();
    const reloadedApple = browseSection.locator('.word-row[data-word="e2e-apple"]');
    await expect(reloadedApple.getByRole('checkbox', { name: '加入默写范围 e2e-apple' })).toBeChecked();
    await expect(browseSection.locator('.word-row[data-word="e2e-banana"]')
      .getByRole('checkbox', { name: '加入默写范围 e2e-banana' })).toBeChecked();

    // ----------------------------------------------------------------
    // 5. Edit: change 意思 on one row; the row text updates + toast.
    // ----------------------------------------------------------------
    await appleRow.getByRole('button', { name: '编辑' }).click();

    const editDialog = page.getByRole('dialog', { name: '编辑单词' });
    await expect(editDialog).toBeVisible();
    await editDialog.locator('#edit-meaning').fill('苹果（已编辑）');
    await editDialog.getByRole('button', { name: '保存修改' }).click();

    await expect(page.locator('.toast-success')).toHaveText('已保存');
    await expect(appleRow.locator('.word-meaning')).toHaveText('苹果（已编辑）');

    // ----------------------------------------------------------------
    // 6. Rename the unit e2e-full → e2e-full-2.
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
    // 7. Settings (设置 tab, 13.3-03): 记忆保持率 → 0.90, save, reload
    //    reflects it (slider + GET), then exercise 恢复默认 via the UI
    //    and save (the button's doc-default behavior is asserted below;
    //    the parent's REAL pre-test settings are restored in the finally
    //    block, WR-04).
    // ----------------------------------------------------------------
    await page.getByRole('tab', { name: '设置' }).click();
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
    // 13.3-03: the reload reset to 学习记录 — back to 设置 for the
    // slider + 恢复默认 assertions.
    await page.getByRole('tab', { name: '设置' }).click();
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
    // 8. Cleanup via the UI (back on the 词库 tab): delete e2e-full-2
    //    (exact-count copy), then the Plan 09-01 e2e-tracer leftovers the
    //    same way. The finally block below is the API backstop.
    // ----------------------------------------------------------------
    await page.getByRole('tab', { name: '词库' }).click();
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
    // WR-04: restore the settings snapshotted before the flow — covers
    // both a mid-flow failure (0.90 left saved) and a real-VPS run where
    // the parent's custom values must not be reset to doc defaults.
    await request.put('/api/v1/settings/fsrs', { data: settingsBefore }).catch(() => {});
  }
});

// ---------------------------------------------------------------------------
// Plan 09-08 Task 2 — D-03 responsive matrix + UI-SPEC backstop rows.
//
// Discriminators are read from the real stylesheet (styles/parent.css) via
// computed styles / window.matchMedia — the ipad-layout.spec.js approach.
// 13.3-03 rebase: the page itself is single-column at every width (tab bar
// + one visible panel); the two-column arrangement lives on the 词库 panel
// (#vocabPanel) at >=1024px:
//   - <1024px: the vocab panel stacks (display:block), sections in DOM order
//   - >=1024px: the vocab panel is a two-column grid (browse 2fr wider than
//     the 1fr rail), container max-width 1120px
//   - every viewport: inputs report font-size >=16px (no iOS zoom-on-focus)
//
// Backstop fixtures (UI Considerations long-text + overflow rows): an
// over-length unit name and a long Chinese meaning prove the clamp/ellipsis
// styles; a 25-line paste proves the 320px preview cap and page scroll with
// a collapsed group present. Screenshots land under test-results/ as the
// durable human-review evidence (the automated asserts are best-effort;
// the plan routes these rows to visual confirmation).
// ---------------------------------------------------------------------------

const OVER_UNIT = 'e2e-backstop-' + '超长单元名称测试'.repeat(15); // 133 runes
const LONG_MEANING = '这是一个用来验证长文本换行与省略行为的超长中文释义。'.repeat(8);
// 25 pasted lines for the preview-overflow fixture — previewed, NEVER committed.
const OVERFLOW_PASTE = Array.from({ length: 25 }, (_, i) =>
  `e2e-overflow-${String(i + 1).padStart(2, '0')}\t溢出第${i + 1}行`).join('\n');

test('responsive matrix (phone/tablet/desktop) + backstop visual checks', async ({ page, request }) => {
  const info = test.info();
  await deleteUnit(request, OVER_UNIT);
  try {
    const seed = await request.post('/api/v1/words', {
      data: {
        category: CAT,
        unit: OVER_UNIT,
        entries: [
          { word: 'e2e-longtext', meaning: LONG_MEANING },
          { word: 'e2e-filler', meaning: '占位' },
        ],
      },
    });
    expect(seed.ok(), 'backstop seed POST must succeed').toBeTruthy();

    const viewports = [
      { name: 'phone-375x812', width: 375, height: 812, twoCol: false, cardPadding: '16px', assertToolbarColumn: true },
      { name: 'tablet-820x1180', width: 820, height: 1180, twoCol: false, cardPadding: '24px', assertToolbarColumn: false },
      { name: 'desktop-1280x800', width: 1280, height: 800, twoCol: true, cardPadding: '24px', assertToolbarColumn: false },
    ];

    await page.setViewportSize({ width: 375, height: 812 });
    await page.goto('/parent.html');
    // 13.3-03: the vocabulary cards live on the 词库 tab.
    await page.getByRole('tab', { name: '词库' }).click();

    const longRow = page.locator('.word-row[data-word="e2e-longtext"]');
    await expect(longRow).toBeVisible();

    for (const vp of viewports) {
      await page.setViewportSize({ width: vp.width, height: vp.height });

      const layout = await page.evaluate(() => {
        const cs = (el) => (el ? getComputedStyle(el) : null);
        const main = document.querySelector('.main-content');
        const vocabPanel = document.querySelector('#vocabPanel');
        const browse = document.querySelector('.browse-section');
        const add = document.querySelector('.add-section');
        const toolbar = document.querySelector('.browse-toolbar');
        const container = document.querySelector('.container');
        // iOS zooms text-ENTRY inputs whose font is <16px (radios/ranges
        // are not zoom-on-focus targets); parent.css pins 16px on every
        // text input class — assert that surface.
        const textInputs = Array.from(
          document.querySelectorAll('input[type="text"], input:not([type]), textarea'),
        ).map((el) => parseFloat(getComputedStyle(el).fontSize));
        return {
          mq1024: window.matchMedia('(min-width: 1024px)').matches,
          mainDisplay: cs(main).display,
          panelDisplay: cs(vocabPanel).display,
          gridTracks: cs(vocabPanel).gridTemplateColumns.split(' ').filter(Boolean).length,
          toolbarDirection: cs(toolbar).flexDirection,
          containerMaxWidth: cs(container).maxWidth,
          browseWidth: browse.getBoundingClientRect().width,
          addWidth: add.getBoundingClientRect().width,
          browseTop: browse.getBoundingClientRect().top,
          addBottom: add.getBoundingClientRect().bottom,
          cardPadding: cs(add).paddingLeft,
          minInputFont: textInputs.length ? Math.min(...textInputs) : 0,
        };
      });

      expect(layout.mq1024, `${vp.name}: (min-width: 1024px) MQ must match the viewport`).toBe(vp.twoCol);
      // 13.3-03: the page itself never grids — tabs own the page chrome.
      expect(layout.mainDisplay, `${vp.name}: main stays single-column (the tab bar owns the page)`).toBe('block');
      if (!vp.twoCol) {
        // Single column: vocab panel stacks in DOM order, no grid, toolbar stacks.
        expect(layout.panelDisplay, `${vp.name}: vocab panel stacks (single column)`).toBe('block');
        expect(layout.browseTop, `${vp.name}: browse renders below the add card`).toBeGreaterThanOrEqual(layout.addBottom);
        if (vp.assertToolbarColumn) {
          expect(layout.toolbarDirection, `${vp.name}: toolbar stacks on phone`).toBe('column');
        }
      } else {
        // Two-column grid on the vocab panel: browse 2fr wider than the rail.
        expect(layout.panelDisplay, `${vp.name}: two-column grid active on the vocab panel at >=1024px`).toBe('grid');
        expect(layout.gridTracks, `${vp.name}: grid-template-columns has two tracks`).toBe(2);
        expect(layout.browseWidth, `${vp.name}: browse column wider than the rail`).toBeGreaterThan(layout.addWidth);
        expect(layout.containerMaxWidth, `${vp.name}: container capped at 1120px`).toBe('1120px');
      }
      expect(layout.cardPadding, `${vp.name}: card padding follows the breakpoint`).toBe(vp.cardPadding);
      expect(layout.minInputFont, `${vp.name}: every input stays >=16px (no iOS zoom-on-focus)`).toBeGreaterThanOrEqual(16);

      await page.screenshot({
        path: info.outputPath(`matrix-${vp.name}.png`),
        fullPage: true,
      });
    }

    // ------------------------------------------------------------------
    // Backstop row 1 — long-text handling (UI-SPEC UI Considerations):
    // meanings clamp to 2 lines with ellipsis; unit names stay single-line
    // ellipsized with the full text available (title attribute; AT reads
    // the untruncated textContent).
    // ------------------------------------------------------------------
    // This Chromium computes display:-webkit-box as flow-root while the
    // clamp still applies — assert the clamp properties plus the rendered
    // 2-line height (the behavioral proof), not the display alias.
    const meaningStyles = await longRow.locator('.word-meaning').evaluate((el) => {
      const s = getComputedStyle(el);
      const lineHeight = parseFloat(s.lineHeight) || parseFloat(s.fontSize) * 1.5;
      return {
        clamp: s.webkitLineClamp || s.getPropertyValue('-webkit-line-clamp'),
        orient: s.webkitBoxOrient,
        overflow: s.overflow,
        height: el.getBoundingClientRect().height,
        lineHeight,
      };
    });
    expect(meaningStyles.clamp, 'meaning clamps at 2 lines').toBe('2');
    expect(meaningStyles.orient, 'clamp orientation is vertical').toBe('vertical');
    expect(meaningStyles.overflow, 'clamped meaning hides the overflow').toBe('hidden');
    expect(
      meaningStyles.height,
      'rendered meaning stays within 2 lines (clamp actually applies to the long fixture)',
    ).toBeLessThanOrEqual(meaningStyles.lineHeight * 2.5);

    const backstopGroup = page.locator(`.unit-group[data-unit="${OVER_UNIT}"]`);
    const unitName = backstopGroup.locator('.unit-name');
    const nameStyles = await unitName.evaluate((el) => {
      const s = getComputedStyle(el);
      return { whiteSpace: s.whiteSpace, textOverflow: s.textOverflow, overflow: s.overflow };
    });
    expect(nameStyles.whiteSpace, 'unit name stays single-line').toBe('nowrap');
    expect(nameStyles.textOverflow, 'unit name ellipsizes').toBe('ellipsis');
    expect(nameStyles.overflow).toBe('hidden');
    expect(
      await unitName.getAttribute('title'),
      'full unit name stays available beyond the ellipsis',
    ).toBe(OVER_UNIT);

    await page.screenshot({
      path: info.outputPath('backstop-longtext-desktop.png'),
      fullPage: true,
    });

    // ------------------------------------------------------------------
    // Backstop row 2 — overflow: the batch preview table caps at 320px
    // with an inner scroll; the long page scrolls with a collapsed group
    // present (uses the e2e unit — no 20-unit seeding, T-09-12).
    // ------------------------------------------------------------------
    await page.getByRole('tab', { name: '批量添加' }).click();
    const batchPanel = page.locator('#add-panel-batch');
    await batchPanel.locator('#parent-batch-paste').fill(OVERFLOW_PASTE);
    await batchPanel.getByRole('button', { name: '解析并预览' }).click();
    await expect(batchPanel.locator('.preview-row')).toHaveCount(25);

    const previewStyles = await batchPanel.locator('.preview-scroll').evaluate((el) => {
      const s = getComputedStyle(el);
      return {
        maxHeight: s.maxHeight,
        overflowY: s.overflowY,
        scrollable: el.scrollHeight > el.clientHeight,
      };
    });
    expect(previewStyles.maxHeight, 'preview region capped at 320px').toBe('320px');
    expect(previewStyles.overflowY, 'preview scrolls internally').toBe('auto');
    expect(previewStyles.scrollable, '25 rows overflow into the inner scroll').toBe(true);

    await page.screenshot({
      path: info.outputPath('backstop-preview-overflow.png'),
      fullPage: true,
    });

    await backstopGroup.locator('.unit-toggle').click();
    await expect(backstopGroup.locator('.unit-body')).toBeHidden();
    const pageScrolls = await page.evaluate(() =>
      document.documentElement.scrollHeight > window.innerHeight);
    expect(pageScrolls, 'long page scrolls with a collapsed group present').toBe(true);
  } finally {
    await deleteUnit(request, OVER_UNIT);
  }
});

// ---------------------------------------------------------------------------
// Issue #18 — the browse rows' inline checkbox (选择) and the 加入默写范围
// toggle meet the 44px touch floor. The input keeps its 18px visual size;
// the hit area is the wrapping <label> (Chrome zeroes USED padding on
// appearance:auto checkboxes, so the reverted padding + negative-margin
// approach on the input itself cannot work). Discriminators are behavioral,
// not CSS declarations: a tap INSIDE the 44px label but OUTSIDE the 18px
// input must toggle — under the bare-input implementation that corner is
// row background/word text and the tap does nothing.
// ---------------------------------------------------------------------------
test('browse rows: inline checkbox meets the 44px touch floor without deforming the row (Issue #18)', async ({ page, request }) => {
  const unit = 'e2e-touch-floor';
  await deleteUnit(request, unit);
  const seed = await request.post('/api/v1/words', {
    data: {
      category: CAT, unit,
      entries: [
        { word: 'e2e-touch-a', meaning: '触控测试甲' },
        { word: 'e2e-touch-b', meaning: '触控测试乙' },
      ],
    },
  });
  expect(seed.ok()).toBeTruthy();

  const consoleErrors = [];
  page.on('console', (msg) => { if (msg.type() === 'error') consoleErrors.push(msg.text()); });
  page.on('pageerror', (err) => consoleErrors.push(String(err)));

  try {
    await page.goto('/parent.html');
    await page.getByRole('tab', { name: '词库' }).click();
    const group = page.locator(`.unit-group[data-unit="${unit}"]`);
    await expect(group.locator('.word-row')).toHaveCount(2);

    /** Label/input geometry of the first row's 选择 checkbox. Scrolled
     *  into view first: the corner taps below ride viewport coordinates
     *  (page.mouse), which miss a below-the-fold label. */
    const hitMetrics = async () => {
      await group.locator('.word-row').first().scrollIntoViewIfNeeded();
      return group.locator('.word-row').first().locator('.word-select')
        .evaluate((el) => {
          const label = el.closest('label');
          const l = (label || el).getBoundingClientRect();
          const b = el.getBoundingClientRect();
          return {
            wrapped: !!label,
            labelX: l.x, labelY: l.y, labelW: l.width, labelH: l.height,
            inputW: b.width, inputH: b.height,
          };
        });
    };

    // AC 1/2: >=44px hit area, ~18px visual input.
    const m = await hitMetrics();
    expect(m.wrapped, 'the row checkbox rides a wrapping label (Issue #18 方案 A)').toBe(true);
    expect(m.labelW, 'hit area width >= 44px').toBeGreaterThanOrEqual(44);
    expect(m.labelH, 'hit area height >= 44px').toBeGreaterThanOrEqual(44);
    expect(m.inputW, 'visual size stays ~18px (no giant checkbox)').toBeLessThanOrEqual(20);
    expect(m.inputH, 'visual size stays ~18px (no giant checkbox)').toBeLessThanOrEqual(20);

    // Behavioral discriminator: corner taps inside the 44px label but
    // 10px outside the centered 18px input must toggle the checkbox.
    const check = group.locator('.word-row').first().locator('.word-select');
    await page.mouse.click(m.labelX + m.labelW - 3, m.labelY + m.labelH - 3);
    await expect(check).toBeChecked();
    await page.mouse.click(m.labelX + 3, m.labelY + 3);
    await expect(check).not.toBeChecked();

    // The 加入默写范围 label (checkbox + gap + text) is itself >=44px tall.
    const toggleH = await group.locator('.word-row').first().locator('.participation-toggle')
      .evaluate((el) => el.getBoundingClientRect().height);
    expect(toggleH, 'participation toggle label height >= 44px').toBeGreaterThanOrEqual(44);

    // AC 3: row height stays uniform at the 61px rhythm (44px action
    // floor + 2×8px row padding + 1px separator border) — the label must
    // not stretch or skew the rows. The group's LAST row drops its
    // separator by design (.word-row:last-child), so normalize the border
    // out before comparing: 60px rhythm + 0/1px separator = 60/61px rows.
    const rhythms = await group.locator('.word-row').evaluateAll((els) =>
      els.map((el) => {
        const border = parseFloat(getComputedStyle(el).borderBottomWidth) || 0;
        return el.getBoundingClientRect().height - border;
      }));
    expect(new Set(rhythms.map((h) => Math.round(h))).size, 'every row measures the same height').toBe(1);
    expect(rhythms[0], 'row height stays at the pre-fix 60px content rhythm').toBeGreaterThanOrEqual(59);
    expect(rhythms[0]).toBeLessThanOrEqual(61);

    // AC 4: 390px narrow screen — no horizontal overflow, hit area intact.
    await page.setViewportSize({ width: 390, height: 844 });
    const narrow = await hitMetrics();
    expect(narrow.labelH, 'hit area survives the narrow-screen wrap').toBeGreaterThanOrEqual(44);
    const overflow = await page.evaluate(() => ({
      scrollW: document.scrollingElement.scrollWidth,
      innerW: window.innerWidth,
    }));
    expect(overflow.scrollW, '390px viewport: no horizontal overflow').toBeLessThanOrEqual(overflow.innerW);

    // AC 5: keyboard reach — Tab from the unit header lands on the
    // checkbox with the :focus-visible ring (3px accent outline).
    await page.setViewportSize({ width: 1280, height: 720 });
    await group.locator('.unit-toggle').focus();
    let ring = null;
    for (let i = 0; i < 6 && !ring; i++) {
      await page.keyboard.press('Tab');
      ring = await page.evaluate((u) => {
        const el = document.activeElement;
        if (!el || !el.classList.contains('word-select')) return null;
        const g = el.closest('.unit-group');
        if (!g || g.dataset.unit !== u) return null;
        const s = getComputedStyle(el);
        return { outlineStyle: s.outlineStyle, outlineWidth: s.outlineWidth };
      }, unit);
    }
    expect(ring, 'Tab from the unit header reaches the row checkbox').not.toBeNull();
    expect(ring.outlineStyle, ':focus-visible outline is active on keyboard focus').not.toBe('none');
    expect(parseFloat(ring.outlineWidth), 'focus ring keeps the 3px accessibility contract').toBeGreaterThanOrEqual(3);

    // AC 6: the page produced no console errors along the way.
    expect(consoleErrors, 'no console errors on the parent page').toEqual([]);
  } finally {
    await deleteUnit(request, unit);
  }
});

// ---------------------------------------------------------------------------
// 13.3-03 Task 1 — page-level tab smoke (real backend): the page opens on
// 学习记录, 词库/设置 swap their panels via the hidden attribute, and a
// reload resets to the default tab (tab state deliberately not persisted).
// ---------------------------------------------------------------------------

test('parent page tab smoke: 学习记录 default, 词库/设置 switch, reload resets', async ({ page }) => {
  await page.goto('/parent.html');

  // Default: the learning-record panel is the ONLY visible one.
  await expect(page.locator('#lessonsSection')).toBeVisible();
  await expect(page.getByRole('tab', { name: '学习记录' })).toHaveAttribute('aria-selected', 'true');
  await expect(page.locator('#vocabPanel')).toBeHidden();
  await expect(page.locator('#addSection')).toBeHidden();
  await expect(page.locator('#settingsPanel')).toBeHidden();

  // 词库 tab: both vocabulary cards render (the components mounted while
  // their panel was hidden — the settingsSection hidden-then-mount precedent).
  await page.getByRole('tab', { name: '词库' }).click();
  await expect(page.locator('#addSection')).toBeVisible();
  await expect(page.locator('#browseSection')).toBeVisible();
  await expect(page.locator('#lessonsSection')).toBeHidden();
  await expect(page.getByRole('tab', { name: '词库' })).toHaveAttribute('aria-selected', 'true');
  await expect(page.getByRole('tab', { name: '学习记录' })).toHaveAttribute('aria-selected', 'false');

  // 设置 tab: the settings card renders (SettingsCard unhid its section
  // at mount; the tab un-hides the panel).
  await page.getByRole('tab', { name: '设置' }).click();
  await expect(page.locator('#settingsSection')).toBeVisible();
  await expect(page.locator('#vocabPanel')).toBeHidden();
  await expect(page.locator('#lessonsSection')).toBeHidden();

  // Reload resets to 学习记录 — tab state is not persisted.
  await page.reload();
  await expect(page.locator('#lessonsSection')).toBeVisible();
  await expect(page.locator('#vocabPanel')).toBeHidden();
  await expect(page.locator('#settingsPanel')).toBeHidden();
  await expect(page.getByRole('tab', { name: '学习记录' })).toHaveAttribute('aria-selected', 'true');
});
