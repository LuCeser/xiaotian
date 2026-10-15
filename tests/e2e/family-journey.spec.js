/**
 * Family-journey browser E2E (13.3 UAT-1…6,10 persistence + UI) against the
 * HERMETIC parent stack (playwright.parent.config.js / npm run e2e:parent).
 *
 * Complements mock parent-lessons.spec.js (render shapes) and L3 synthetic
 * driver (realtime voice). Here the Go+Python+SQLite stack is real; lesson
 * rows are seeded into the isolated temp DB (never production).
 */
// @ts-check
const { test, expect } = require('@playwright/test');
const {
  waitForDb,
  seedLesson,
  seedReceipt,
  seedPracticeWord,
  countReviewLogs,
} = require('./helpers/seed-lessons');

const HIDDEN = ['alpha', 'bravo', 'charlie'];

test.beforeAll(async ({ request }) => {
  // Ensure the service finished boot/migrate before any SQLite seed.
  const res = await request.get('/api/v1/lessons');
  expect(res.status()).toBe(200);
  waitForDb();
});

test('UAT-1/2: completed lesson visible as 已保存; browsing is read-zero-write', async ({ page, request }) => {
  // r0- prefix → kind badge 到期复习 (handlers.lessonKind).
  const id = 'r0-20261014-saved';
  seedLesson({ lessonId: id, stage: 'submitted', unchecked: 0 });
  seedReceipt({
    lessonId: id,
    requestId: 'req-family-saved',
    kind: 'check',
    status: 'saved',
  });

  const before = countReviewLogs();

  await page.goto('/parent.html');
  await expect(page.locator('#lessonsSection')).toBeVisible();
  const row = page.locator(`.lesson-row[data-lesson-id="${id}"]`);
  await expect(row).toBeVisible();
  await expect(row).toContainText('到期复习');
  await expect(row).toContainText('已保存');
  await expect(row).toContainText('已核对');

  await row.click();
  await expect(page.locator('.lesson-detail')).toBeVisible();
  await expect(page.locator('.lesson-detail')).toContainText('alpha');
  await expect(page.locator('.lesson-detail')).toContainText('检查提交');
  await expect(page.locator('.lesson-detail')).toContainText('已保存');
  // Read-only save area: refresh/back only — no re-judge.
  await expect(page.getByRole('button', { name: /改判|重评|再提交/ })).toHaveCount(0);

  await page.getByRole('button', { name: '返回列表' }).click();
  await row.click();
  await page.getByRole('button', { name: '刷新' }).click();

  const list2 = await request.get('/api/v1/lessons');
  expect(list2.status()).toBe(200);
  expect(countReviewLogs()).toBe(before);
});

test('UAT-3: in-progress lesson API/UI hide word surfaces', async ({ page, request }) => {
  const id = 'r0-20261014-progress';
  seedLesson({
    lessonId: id,
    stage: 'checking',
    currentIndex: 2,
    unchecked: 0,
    verdictsJson: '[]',
    createdAt: '2026-10-14 13:00:00',
  });

  const listRes = await request.get('/api/v1/lessons');
  const list = await listRes.json();
  const summary = list.lessons.find((l) => l.lesson_id === id);
  expect(summary).toBeTruthy();
  expect(summary.completed).toBe(false);
  for (const w of HIDDEN) {
    expect(JSON.stringify(summary)).not.toContain(w);
  }

  const detailRes = await request.get(`/api/v1/lessons/${id}`);
  const detailBody = await detailRes.text();
  for (const w of HIDDEN) {
    expect(detailBody).not.toContain(w);
  }
  const detail = JSON.parse(detailBody);
  expect(detail.words).toBeUndefined();
  expect(detail.results).toBeUndefined();

  await page.goto('/parent.html');
  const row = page.locator(`.lesson-row[data-lesson-id="${id}"]`);
  await expect(row).toBeVisible();
  await row.click();
  const html = await page.content();
  for (const w of HIDDEN) {
    expect(html).not.toContain(w);
  }
});

test('UAT-4: unchecked close shows 未核对 + 已保存兜底', async ({ page }) => {
  const id = 'r0-20261014-unchecked';
  seedLesson({
    lessonId: id,
    stage: 'submitted',
    unchecked: 1,
    createdAt: '2026-10-14 14:00:00',
  });
  seedReceipt({
    lessonId: id,
    requestId: `${id}-complete-check`,
    kind: 'unchecked_fallback',
    status: 'saved',
  });

  await page.goto('/parent.html');
  const row = page.locator(`.lesson-row[data-lesson-id="${id}"]`);
  await expect(row).toContainText('未核对');
  await expect(row).toContainText('已保存');
  await row.click();
  await expect(page.locator('.lesson-detail')).toContainText('未核对兜底');
});

test('UAT-5: practice row is 加练 and never implies FSRS', async ({ page }) => {
  const id = 'r0x-20261014-120000';
  seedLesson({
    lessonId: id,
    stage: 'submitted',
    createdAt: '2026-10-14 15:00:00',
  });
  seedPracticeWord({ lessonId: id, word: 'alpha' });
  seedPracticeWord({ lessonId: id, word: 'bravo' });
  seedPracticeWord({ lessonId: id, word: 'charlie' });
  seedReceipt({
    lessonId: id,
    requestId: 'req-practice',
    kind: 'practice',
    status: 'saved',
  });

  const before = countReviewLogs();
  await page.goto('/parent.html');
  const row = page.locator(`.lesson-row[data-lesson-id="${id}"]`);
  await expect(row).toContainText('加练');
  await row.click();
  await expect(page.locator('.lesson-detail')).toContainText('加练记录：不调整复习计划');
  await expect(page.locator('.lesson-detail')).toContainText('加练记录');
  expect(countReviewLogs()).toBe(before);
});

test('UAT-6: pending receipt surfaces as 待核实', async ({ page }) => {
  const id = 'r0-20261014-pending';
  seedLesson({
    lessonId: id,
    stage: 'submitted',
    createdAt: '2026-10-14 16:00:00',
  });
  seedReceipt({
    lessonId: id,
    requestId: 'req-crash',
    kind: 'check',
    status: 'pending',
  });

  await page.goto('/parent.html');
  const row = page.locator(`.lesson-row[data-lesson-id="${id}"]`);
  await expect(row).toContainText('待核实');
  await row.click();
  await expect(page.locator('.lesson-detail')).toContainText('待核实');
  await expect(page.locator('.lesson-detail')).not.toContainText('已保存');
});

test('UAT-1 toast: parent add-word copy stays selection-honest', async ({ page }) => {
  const word = `fam-${Date.now()}`;
  await page.goto('/parent.html');
  await page.getByRole('tab', { name: '词库' }).click();
  await page.getByRole('textbox', { name: '单词', exact: true }).fill(word);
  await page.getByRole('radio', { name: '英语' }).check();
  await page.getByRole('combobox', { name: '单元', exact: true }).fill('e2e-family');
  await page.getByRole('button', { name: '添加单词' }).click();
  await expect(page.locator('.toast-success')).toHaveText(
    '已添加。新词将按复习安排选入课堂，不一定是下一课。',
    { timeout: 15_000 },
  );
  await page.request.delete('/api/v1/words', { data: { category: 'english', unit: 'e2e-family' } }).catch(() => {});
});
