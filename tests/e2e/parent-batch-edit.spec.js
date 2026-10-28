/**
 * Issue #80 AC-09 — batch word/meaning edit from the browse selection.
 * Preview is memory-only; 确认修改 issues one PUT per changed row.
 *
 * The FSRS half of "edit never touches learning state" is deliberately NOT
 * asserted here: GET /words (handlers.go wordJSON) carries no fsrs_card
 * field, so a browser-side compare would read undefined and never fire. It
 * is pinned unconditionally, byte-level, by the real-SQLite bridge suite —
 * pkg/pybridge/vocab_mgmt_e2e_test.go: TestE2E_VocabEdit_PreservesFSRS,
 * TestE2E_RunEditWordAndMeaningPreservesFSRS (the exact word+meaning payload
 * 确认修改 sends) and TestE2E_EditWordConflictKeepsIdentity.
 */
// @ts-check
const { test, expect } = require('@playwright/test');

const CAT = 'english';
const UNIT = 'e2e-batch-edit';
const PAIR = ['e2e-own-a', 'e2e-own-b'];

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

// fetchUnit returns the unit's rows from the real GET /words the browse
// page rides on — the wire-level read used for every "nothing was written"
// assertion below.
async function fetchUnit(request) {
  const resp = await request.get(`/api/v1/words?category=${CAT}&unit=${UNIT}`);
  expect(resp.ok()).toBeTruthy();
  return (await resp.json()).words || [];
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

    const rows = await fetchUnit(request);
    const byWord = new Map(rows.map((w) => [w.word, w]));
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
  const snap = new Map((await fetchUnit(request)).map((w) => [w.word, w]));

  // Both cards must come back id/word/meaning identical — the shared
  // invariant for the 取消 half and the conflict half of this test.
  const expectBothCardsUntouched = async (stage) => {
    const rows = await fetchUnit(request);
    expect(rows, `${stage}: unit row count`).toHaveLength(2);
    for (const w of PAIR) {
      const row = rows.find((x) => x.word === w);
      expect(row, `${stage}: row ${w} missing`).toBeTruthy();
      expect(row.id, `${stage}: ${w} id`).toBe(snap.get(w).id);
      expect(row.word, `${stage}: ${w} word`).toBe(w);
      expect(row.meaning, `${stage}: ${w} meaning`).toBe(snap.get(w).meaning);
    }
  };

  try {
    await page.goto('/parent.html');
    await page.getByRole('tab', { name: '词库' }).click();
    await expect(page.locator('.word-row[data-word="e2e-own-a"]')).toBeVisible();

    for (const w of PAIR) {
      await page.locator(`.word-row[data-word="${w}"]`)
        .getByRole('checkbox', { name: `选择 ${w}` }).check();
    }
    await expect(page.locator('.word-select:checked')).toHaveCount(2);

    // Cancel with a REAL pending change on the table: two legal lines, the
    // diff visible in the preview, 确认修改 armed — only the 取消 click
    // stands between the edit and the database.
    await page.locator('#bulk-edit-btn').click();
    const dialog = page.locator('.modal-wide');
    await expect(dialog).toBeVisible();
    await dialog.locator('#batch-edit-input').fill(
      'e2e-own-a\tone edited\ne2e-own-b\ttwo edited');
    await expect(dialog.locator('.batch-edit-summary')).toContainText('2 处修改');
    await expect(dialog.locator('.batch-edit-list li').first())
      .toContainText('「one」→「one edited」');
    await expect(dialog.getByRole('button', { name: '确认修改' })).toBeEnabled();
    await dialog.getByRole('button', { name: '取消' }).click();
    await expect(dialog).toBeHidden();
    await expectBothCardsUntouched('cancel wrote nothing:');

    // The selection survives the cancel; drop it row by row, then rebuild it.
    await expect(page.locator('.word-select:checked')).toHaveCount(2);
    for (let i = 0; i < PAIR.length; i++) {
      await page.locator(`.word-row[data-word="${PAIR[i]}"]`)
        .getByRole('checkbox', { name: `选择 ${PAIR[i]}` }).uncheck();
      await expect(page.locator('.word-select:checked')).toHaveCount(PAIR.length - 1 - i);
    }
    for (const w of PAIR) {
      await page.locator(`.word-row[data-word="${w}"]`)
        .getByRole('checkbox', { name: `选择 ${w}` }).check();
    }
    await expect(page.locator('.word-select:checked')).toHaveCount(2);

    // Line 1 would rename e2e-own-a ONTO e2e-own-b's face. The preview must
    // block it against the real other card, keep 确认修改 disabled, and the
    // backend state must stay untouched.
    await page.locator('#bulk-edit-btn').click();
    const conflictDlg = page.locator('.modal-wide');
    await expect(conflictDlg).toBeVisible();
    await conflictDlg.locator('#batch-edit-input').fill(
      'e2e-own-b\tstolen\ne2e-own-b\ttwo');
    await expect(conflictDlg.locator('.batch-edit-blocked')).toBeVisible();
    await expect(conflictDlg.locator('.batch-edit-blocked'))
      .toContainText(`（#${snap.get('e2e-own-b').id}）`);
    await expect(conflictDlg.getByRole('button', { name: '确认修改' })).toBeDisabled();
    await expectBothCardsUntouched('conflict blocked:');
  } finally {
    await deleteUnit(request);
  }
});
