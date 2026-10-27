/**
 * Issue #80 AC-09 — batch word/meaning edit from the browse selection.
 * Preview is memory-only; 确认修改 issues one PUT per changed row.
 */
// @ts-check
const { test, expect } = require('@playwright/test');

const CAT = 'english';
const UNIT = 'e2e-batch-edit';

async function deleteUnit(request) {
  await request.delete('/api/v1/words', { data: { category: CAT, unit: UNIT } }).catch(() => {});
}

async function seedPair(request) {
  const resp = await request.post('/api/v1/words', {
    data: {
      category: CAT,
      unit: UNIT,
      entries: [
        { word: 'e2e-batch-a', meaning: 'alpha' },
        { word: 'e2e-batch-b', meaning: 'beta' },
      ],
    },
  });
  expect(resp.ok()).toBeTruthy();
}

test('browse batch edit: preview then PUT updates both words', async ({ page, request }) => {
  await deleteUnit(request);
  await seedPair(request);
  try {
    await page.goto('/parent.html');
    await page.getByRole('tab', { name: '词库' }).click();
    await expect(page.locator('.word-row[data-word="e2e-batch-a"]')).toBeVisible();

    for (const w of ['e2e-batch-a', 'e2e-batch-b']) {
      await page.locator(`.word-row[data-word="${w}"]`)
        .getByRole('checkbox', { name: `选择 ${w}` }).check();
    }
    await page.locator('#bulk-edit-btn').click();
    const dialog = page.locator('.modal-wide');
    await expect(dialog).toBeVisible();
    const paste = dialog.locator('#batch-edit-input');
    await paste.fill('e2e-batch-a\talpha revised\ne2e-batch-b\tbeta revised');
    await expect(dialog.locator('.batch-edit-summary')).toContainText('2 处修改');
    await dialog.getByRole('button', { name: '确认修改' }).click();
    await expect(dialog).toBeHidden({ timeout: 15_000 });
    await expect(page.locator('.toast-success')).toContainText('已保存');

    const rows = await request.get(`/api/v1/words?category=${CAT}&unit=${UNIT}`);
    const data = await rows.json();
    const byWord = new Map((data.words || []).map((w) => [w.word, w]));
    expect(byWord.get('e2e-batch-a').meaning).toBe('alpha revised');
    expect(byWord.get('e2e-batch-b').meaning).toBe('beta revised');
    await expect(page.locator('.word-select:checked')).toHaveCount(0);
  } finally {
    await deleteUnit(request);
  }
});

test('browse batch edit: cancel writes nothing; identity conflict keeps both cards', async ({ page, request }) => {
  await deleteUnit(request);
  const add = await request.post('/api/v1/words', {
    data: {
      category: CAT,
      unit: UNIT,
      entries: [
        { word: 'e2e-own-a', meaning: 'one' },
        { word: 'e2e-own-b', meaning: 'two' },
      ],
    },
  });
  expect(add.ok()).toBeTruthy();
  const before = await (await request.get(`/api/v1/words?category=${CAT}&unit=${UNIT}`)).json();
  const snap = new Map((before.words || []).map((w) => [w.word, w]));
  try {
    await page.goto('/parent.html');
    await page.getByRole('tab', { name: '词库' }).click();
    for (const w of ['e2e-own-a', 'e2e-own-b']) {
      await page.locator(`.word-row[data-word="${w}"]`)
        .getByRole('checkbox', { name: `选择 ${w}` }).check();
    }
    await page.locator('#bulk-edit-btn').click();
    const dialog = page.locator('.modal-wide');
    await expect(dialog).toBeVisible();
    await dialog.locator('#batch-edit-input').fill('e2e-own-a\tedited-in-preview');
    await dialog.getByRole('button', { name: '取消' }).click();
    await expect(dialog).toBeHidden();

    const mid = await (await request.get(`/api/v1/words?category=${CAT}&unit=${UNIT}`)).json();
    for (const w of ['e2e-own-a', 'e2e-own-b']) {
      const row = (mid.words || []).find((x) => x.word === w);
      expect(row.id).toBe(snap.get(w).id);
      expect(row.meaning).toBe(snap.get(w).meaning);
      if (snap.get(w).fsrs_card) {
        expect(row.fsrs_card).toEqual(snap.get(w).fsrs_card);
      }
    }

    await page.locator('.word-select:checked').uncheck({ force: true });
    for (const w of ['e2e-own-a', 'e2e-own-b']) {
      await page.locator(`.word-row[data-word="${w}"]`)
        .getByRole('checkbox', { name: `选择 ${w}` }).check();
    }
    await page.locator('#bulk-edit-btn').click();
    const conflictDlg = page.locator('.modal-wide');
    await expect(conflictDlg).toBeVisible();
    await conflictDlg.locator('#batch-edit-input').fill(
      'e2e-own-b\tstolen\ne2e-own-b\ttwo');
    await expect(conflictDlg.locator('.batch-edit-blocked')).toBeVisible();
    await expect(conflictDlg.getByRole('button', { name: '确认修改' })).toBeDisabled();

    const after = await (await request.get(`/api/v1/words?category=${CAT}&unit=${UNIT}`)).json();
    for (const w of ['e2e-own-a', 'e2e-own-b']) {
      const row = (after.words || []).find((x) => x.word === w);
      expect(row.id).toBe(snap.get(w).id);
      expect(row.meaning).toBe(snap.get(w).meaning);
      if (snap.get(w).fsrs_card) {
        expect(row.fsrs_card).toEqual(snap.get(w).fsrs_card);
      }
    }
  } finally {
    await deleteUnit(request);
  }
});
