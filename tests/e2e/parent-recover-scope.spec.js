/**
 * Issue #80 AC-08 — textbook-owned 待确认 words recover through the browse
 * row action without opening the bulk assign modal. Card ids stay stable.
 */
// @ts-check
const { test, expect } = require('@playwright/test');
const { practiceSelection, wordRows, deleteWords } = require('./helpers/photo-flow');

const CAT = 'english';
const BOOK = 'e2e-recover-scope';
const WORDS = ['e2eRecoverScope'];

async function createBook(request, name) {
  const resp = await request.post('/api/v1/textbooks', {
    data: { name, category: CAT, lessons: [] },
  });
  expect(resp.ok()).toBeTruthy();
  return resp.json();
}

test('row 加入默写范围 admits 待确认 textbook words (ids unchanged)', async ({ page, request }) => {
  await deleteWords(request, WORDS);
  try {
    const book = await createBook(request, BOOK);
    const add = await request.post('/api/v1/words', {
      data: { category: CAT, entries: [{ word: WORDS[0], meaning: 'recover me' }] },
    });
    expect(add.ok()).toBeTruthy();
    const before = await wordRows(request);
    const idBefore = before.get(WORDS[0]).id;

    const assign = await request.post(`/api/v1/textbooks/${book.id}/assign`, {
      data: { cardIds: [idBefore], lesson: '', includeInScope: false },
    });
    expect(assign.ok()).toBeTruthy();

    await page.goto('/parent.html');
    await page.getByRole('tab', { name: '词库' }).click();
    const row = page.locator(`.word-row[data-word="${WORDS[0]}"]`);
    await expect(row).toBeVisible();
    await row.getByRole('button', { name: '加入默写范围', exact: true }).click();
    await expect(page.locator('.toast-success')).toHaveText('已加入默写范围');

    const after = await wordRows(request);
    expect(after.get(WORDS[0]).id).toBe(idBefore);
    const tb = (after.get(WORDS[0]).textbooks || []).find((t) => t.name === BOOK);
    expect(tb && tb.includeInScope).toBe(true);

    const picked = await practiceSelection();
    expect(picked).toContain(WORDS[0]);
  } finally {
    await deleteWords(request, WORDS);
  }
});
