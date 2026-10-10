/**
 * Parent learning-record e2e (13.3-03 Task 3) — mock /api/v1 stub route.
 *
 * Complements the REAL-backend parent-management.spec.js suite: the mock
 * server (tests/e2e-rt/mock-rt-server.js) serves a FIXED four-lesson
 * dataset whose field names mirror the 13.3-02 wire structs field-for-
 * field (lessonListItem / lessonDetailResponse / receiptJSON — the
 * mock-parity discipline: the stub proves frontend rendering, never
 * server behavior; the projection rules themselves are httptest-pinned
 * in pkg/web/api).
 *
 * What this suite pins:
 *   - the 学习记录 tab is the DEFAULT tab and renders the four stub rows
 *     with the honest copy VERBATIM: 已核对 X 对 / Y 错 / Z 未确认
 *     (never a percentage), the four save chips 已保存/保存未完成/待核实/
 *     保存状态未知, the 未核对 flag, kind badges 到期复习/加练.
 *   - the leak-guard negative: the in-progress lesson ① (r0-20261009)
 *     keeps word surfaces server-side ONLY ('zephyr'/'onyx'/'marina');
 *     neither its list row nor its opened detail may contain them
 *     (page.content() scan — serialized DOM, not just visible text).
 *   - the detail views: vertical per-word rows (词面/释义/判定徽标/
 *     提示 N 次/outcome/识别：…), receipt lines (kind 中文 + status +
 *     real 保存 X / Y counts), the practice fixed note 加练记录：不调整
 *     复习计划, and the read-only save-status area (only 返回列表 + 刷新
 *     buttons — no re-submit/re-judge entries anywhere).
 *   - tab-wiring regression on the mock origin: 词库 still reaches the
 *     add card.
 */

const { test, expect } = require('@playwright/test');

// Word surfaces that exist ONLY in the in-progress stub lesson's
// server-side dataset — no stub endpoint ever serializes them, so any
// occurrence on the page is a leak.
const HIDDEN_WORDS = ['zephyr', 'onyx', 'marina'];

const IN_PROGRESS_SUMMARY = {
  lesson_id: 'r0-20261009',
  created_at: '2026-10-09 18:30:00',
  word_count: 3,
  stage: '',
  kind: 'lesson',
  completed: false,
  unchecked: false,
  save_status: 'unknown',
  correct: 0,
  wrong: 0,
  unconfirmed: 0
};

test('mock /api/v1 lessons stubs follow the 13.3-02 wire contract', async ({ request }) => {
  // List: four summary rows, created_at DESC.
  const listRes = await request.get('/api/v1/lessons');
  expect(listRes.status()).toBe(200);
  const list = await listRes.json();
  expect(list.lessons).toHaveLength(4);
  expect(list.lessons[0]).toEqual(IN_PROGRESS_SUMMARY);
  expect(list.lessons.map((l) => l.lesson_id)).toEqual([
    'r0-20261009',
    'r0x-20261009-101500',
    'r0-20261008',
    'r0-20261007'
  ]);

  // In-progress detail: the summary IS the whole response — the
  // word-bearing keys do not exist in the serialization at all.
  const detailRes = await request.get('/api/v1/lessons/r0-20261009');
  expect(detailRes.status()).toBe(200);
  const detail = await detailRes.json();
  expect(Object.keys(detail).sort()).toEqual(Object.keys(IN_PROGRESS_SUMMARY).sort());

  // Completed detail carries the three detail keys.
  const fullRes = await request.get('/api/v1/lessons/r0-20261008');
  expect(fullRes.status()).toBe(200);
  const full = await fullRes.json();
  expect(full.words).toHaveLength(3);
  expect(full.results).toHaveLength(3);
  expect(full.save_receipts).toHaveLength(1);

  // Unknown id: handlers.go error envelope parity.
  const missingRes = await request.get('/api/v1/lessons/no-such-lesson');
  expect(missingRes.status()).toBe(404);
  expect(await missingRes.json()).toEqual({
    error: { code: 'not_found', message: 'lesson not found' }
  });
});

test('learning record: default tab renders four honest rows', async ({ page }) => {
  await page.goto('/parent.html');

  // 学习记录 is the default tab; rows render from the stub list.
  await expect(page.locator('#lessonsSection')).toBeVisible();
  const rows = page.locator('.lesson-row');
  await expect(rows).toHaveCount(4);

  // ① In-progress: stage summary + word count ONLY — no counts, no save
  // chip (a lesson still in the room has nothing saved yet).
  const inProgress = page.locator('.lesson-row[data-lesson-id="r0-20261009"]');
  await expect(inProgress).toContainText('2026-10-09');
  await expect(inProgress).toContainText('到期复习');
  await expect(inProgress).toContainText('3 词');
  await expect(inProgress).toContainText('默写中');
  await expect(inProgress.locator('.lesson-counts')).toHaveCount(0);
  await expect(inProgress.locator('.save-chip')).toHaveCount(0);

  // ④ Extra practice: kind badge + counts + saved chip.
  const practice = page.locator('.lesson-row[data-lesson-id="r0x-20261009-101500"]');
  await expect(practice).toContainText('加练');
  await expect(practice.locator('.lesson-counts')).toHaveText('已核对 1 对 / 1 错 / 0 未确认');
  await expect(practice.locator('.save-chip')).toHaveText('已保存');

  // ② Completed check lesson: the exact count line (未知词不入分母 —
  // verbatim, no percentage) + saved chip.
  const checkLesson = page.locator('.lesson-row[data-lesson-id="r0-20261008"]');
  await expect(checkLesson.locator('.lesson-counts')).toHaveText('已核对 2 对 / 1 错 / 0 未确认');
  await expect(checkLesson.locator('.save-chip')).toHaveText('已保存');

  // ③ Unchecked fallback: 未核对 flag + pending chip + derived-only counts.
  const fallback = page.locator('.lesson-row[data-lesson-id="r0-20261007"]');
  await expect(fallback).toContainText('未核对');
  await expect(fallback.locator('.save-chip')).toHaveText('待核实');
  await expect(fallback.locator('.lesson-counts')).toHaveText('已核对 0 对 / 0 错 / 2 未确认');

  // Honest numbers never become percentages: no % glyph in the VISIBLE
  // text (the serialized-DOM scan would false-positive on inline style
  // values like the sliders' --range-fill:50%).
  const listText = await page.locator('body').innerText();
  expect(listText, 'no percentage anywhere on the list').not.toContain('%');

  // Leak-guard negative at the list level: the in-progress surfaces
  // never appear in the serialized DOM.
  const html = await page.content();
  for (const word of HIDDEN_WORDS) {
    expect(html, `leak check (list): ${word}`).not.toContain(word);
  }
});

test('learning record: completed detail shows vertical word rows and receipts', async ({ page }) => {
  await page.goto('/parent.html');
  await page.locator('.lesson-row[data-lesson-id="r0-20261008"]').click();

  const detail = page.locator('.lesson-detail');
  await expect(detail).toBeVisible();
  await expect(detail).toContainText('2026-10-08');
  await expect(detail).toContainText('到期复习');
  await expect(detail.locator('.lesson-detail-meta').locator('.save-chip')).toHaveText('已保存');

  // One vertical row per word: face / meaning / verdict / hints /
  // outcome / 识别 — all from the stub detail (practice shape vs lesson
  // shape alike).
  const wordRows = page.locator('.lesson-word-row');
  await expect(wordRows).toHaveCount(3);

  const summer = page.locator('.lesson-word-row[data-word="summer"]');
  await expect(summer.locator('.lesson-word-face')).toHaveText('summer');
  await expect(summer.locator('.lesson-word-meaning')).toHaveText('夏天');
  await expect(summer.locator('.lesson-verdict')).toHaveText('错');
  await expect(summer).toContainText('提示 1 次');
  await expect(summer).toContainText('超时');
  await expect(summer).toContainText('识别：sumner');

  const spring = page.locator('.lesson-word-row[data-word="spring"]');
  await expect(spring.locator('.lesson-verdict')).toHaveText('对');
  await expect(spring).toContainText('确认');

  // Receipt line: kind in Chinese + status copy + the real X/Y counts.
  const receipt = page.locator('.lesson-receipt');
  await expect(receipt).toHaveCount(1);
  await expect(receipt).toContainText('检查提交');
  await expect(receipt).toContainText('已保存');
  await expect(receipt).toContainText('保存 3 / 3');

  // No percentage in the detail's visible text either.
  const detailText = await page.locator('body').innerText();
  expect(detailText, 'no percentage anywhere on the detail').not.toContain('%');

  // Back to the list re-reads it (rows render again).
  await page.getByRole('button', { name: '返回列表' }).click();
  await expect(page.locator('.lesson-row')).toHaveCount(4);
});

test('learning record: unchecked fallback detail marks 未确认 and pending receipt', async ({ page }) => {
  await page.goto('/parent.html');
  await page.locator('.lesson-row[data-lesson-id="r0-20261007"]').click();

  const detail = page.locator('.lesson-detail');
  await expect(detail).toBeVisible();
  await expect(detail).toContainText('未核对');

  // Derived fallback verdicts: manual=false renders 未确认, not 对/错.
  const river = page.locator('.lesson-word-row[data-word="river"]');
  await expect(river.locator('.lesson-verdict')).toHaveText('未确认');
  const stone = page.locator('.lesson-word-row[data-word="stone"]');
  await expect(stone.locator('.lesson-verdict')).toHaveText('未确认');
  await expect(stone).toContainText('提示 2 次');

  // Pending receipt: honest 待核实, real counts (nothing written yet).
  const receipt = page.locator('.lesson-receipt');
  await expect(receipt).toContainText('未核对兜底');
  await expect(receipt).toContainText('待核实');
  await expect(receipt).toContainText('保存 0 / 2');

  // The save-status area is READ-ONLY: the detail's only buttons are
  // 返回列表 + 刷新 (re-read) — no re-submit, no re-judge, no re-open.
  const buttons = detail.getByRole('button');
  await expect(buttons).toHaveText(['返回列表', '刷新']);
});

test('learning record: practice detail shows the no-FSRS note and practice receipt', async ({ page }) => {
  await page.goto('/parent.html');
  await page.locator('.lesson-row[data-lesson-id="r0x-20261009-101500"]').click();

  const detail = page.locator('.lesson-detail');
  await expect(detail).toBeVisible();
  await expect(detail).toContainText('加练');
  // Fixed practice line: 加练 never adjusts the FSRS schedule.
  await expect(detail.locator('.lesson-practice-note')).toHaveText('加练记录：不调整复习计划');

  // Practice-shaped results (no manual/outcome/ocr fields, no meanings).
  const wordRows = page.locator('.lesson-word-row');
  await expect(wordRows).toHaveCount(2);
  await expect(page.locator('.lesson-word-row[data-word="guitar"]').locator('.lesson-verdict')).toHaveText('对');
  const piano = page.locator('.lesson-word-row[data-word="piano"]');
  await expect(piano.locator('.lesson-verdict')).toHaveText('错');
  await expect(piano).toContainText('提示 1 次');

  const receipt = page.locator('.lesson-receipt');
  await expect(receipt).toContainText('加练记录');
  await expect(receipt).toContainText('已保存');
  await expect(receipt).toContainText('保存 2 / 2');
});

test('learning record: in-progress lesson exposes no word surfaces (negative)', async ({ page }) => {
  await page.goto('/parent.html');

  // Open the in-progress row's detail: the summary-only response renders
  // the stage note — no word rows, no surfaces.
  await page.locator('.lesson-row[data-lesson-id="r0-20261009"]').click();
  const detail = page.locator('.lesson-detail');
  await expect(detail).toBeVisible();
  await expect(detail).toContainText('默写中');
  await expect(detail).toContainText('这次课还没结束');
  await expect(page.locator('.lesson-word-row')).toHaveCount(0);
  await expect(page.locator('.lesson-receipt')).toHaveCount(0);

  // Serialized-DOM scan (stronger than visible text): none of the
  // server-side-only surfaces appear anywhere.
  const html = await page.content();
  for (const word of HIDDEN_WORDS) {
    expect(html, `leak check (in-progress detail): ${word}`).not.toContain(word);
  }
});

test('learning record: tab wiring still reaches 词库 (add card visible)', async ({ page }) => {
  await page.goto('/parent.html');
  await page.getByRole('tab', { name: '词库' }).click();
  await expect(page.locator('#addSection')).toBeVisible();
  await expect(page.locator('#addSection').getByRole('button', { name: '添加单词' })).toBeVisible();
});
