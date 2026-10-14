/**
 * Parent textbook-scope E2E (Issue #4): 教材归属与已学进度控制练习范围,
 * proven against the ISOLATED real service — real browser → real Go
 * service → real Python bridge → real SQLite (per-run temp DB).
 *
 * Acceptance link (issue #4 AC-7): two textbooks with DIFFERENTLY-ORDERED
 * lesson names, the parent changes learned progress THROUGH THE BROWSER,
 * and an isolated real classroom proves the selection result. The
 * classroom probe is a raw /rt practice-init: the daily due row is pinned
 * to the config's fixed lessonId (e2e-parent-photo, occupied by the photo
 * suite — first-content-wins), while practice ids are r0x-<timestamp>,
 * naturally fresh per session. Practice still rides the REAL selection
 * (participating AND textbook-scope filters), so it proves scope exactly
 * where the due path cannot be reused.
 *
 * Flow:
 *   1. seed 8 english words via REST (one unit);
 *   2. UI: 教材管理 creates 快乐英语 (lessons C课,A课,B课 — list order is
 *      deliberately NOT dictionary order; edition filled) and 阳光英语
 *      (lessons Z课,Y课);
 *   3. UI: 批量指定 modal assigns the words to lessons (待确认 and the
 *      include-in-scope override included); attribution chips render;
 *   4. UI: progress 快乐英语 = 已学完第 1 课 (C课 — the LIST's first, not
 *      the dictionary-first A课);
 *   5. classroom probe 1: practice selection == exactly the eligible set
 *      (C课 words + include + supplementary; NOT B课/Z课/待确认);
 *   6. UI: progress 快乐英语 = 3 (B课 learned), 阳光英语 = 1 (Z课 learned);
 *   7. classroom probe 2: the expanded eligible set — the very next
 *      classroom honors the browser-made progress change (≥1.1s apart:
 *      r0x ids are second-granular, two probes in one second would replay
 *      the first's frozen row);
 *   8. orthogonality with #2: 加入默写范围 unchecked on one eligible word →
 *      classroom probe 3 drops exactly that word.
 */
// @ts-check
const { test, expect } = require('@playwright/test');
const WebSocket = require('ws');

const CAT = 'english';
const UNIT = 'e2e-textbook-scope';

const BOOK1 = '快乐英语';
const BOOK1_LABEL = '快乐英语（人教版）'; // option text carries the metadata
const BOOK2 = '阳光英语';

// The seeded vocabulary. Expected eligibility per phase is derived below.
const WORDS = {
  C1: 'tbScopeC1', // → 快乐英语 C课 (list position 1)
  C2: 'tbScopeC2', // → 快乐英语 C课
  B1: 'tbScopeB1', // → 快乐英语 B课 (list position 3)
  Inc: 'tbScopeInc', // → 快乐英语 B课 + include-in-scope override
  Pend: 'tbScopePend', // → 快乐英语 待确认
  Z1: 'tbScopeZ1', // → 阳光英语 Z课 (list position 1)
  S1: 'tbScopeS1', // supplementary (no assignment)
  S2: 'tbScopeS2', // supplementary — later participation-disabled
};
const ALL_WORDS = Object.values(WORDS);

/** DELETE the whole unit through the real API (404 = already gone). */
async function deleteUnit(request) {
  await request.delete('/api/v1/words', { data: { category: CAT, unit: UNIT } }).catch(() => {});
}

/** Sorted copy — practice selection order is shuffled by design. */
const sorted = (list) => [...list].sort();

/** Open the parent page into the 词库 browse view. */
async function openWordLib(page) {
  await page.goto('/parent.html');
  await page.getByRole('tab', { name: '词库' }).click();
  await expect(page.locator('#browseSection')).toBeVisible();
}

/**
 * One real classroom probe: a raw /rt practice session_init (mode=test —
 * the full-reveal lesson view; the student projection masks undictated
 * words to empty strings by design, 13.1-02). Resolves with
 * session_ready's selected words (sent before any turn/announcement audio,
 * so stubbed voice traffic cannot race the assertion).
 */
function practiceSelection() {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket('ws://127.0.0.1:18080/rt', {
      origin: 'http://127.0.0.1:18080',
    });
    const fail = setTimeout(() => reject(new Error('session_ready timeout')), 30_000);
    ws.on('error', (err) => { clearTimeout(fail); reject(err); });
    ws.on('open', () => {
      ws.send(JSON.stringify({
        type: 'session_init',
        practice: true,
        mode: 'test',
        audio: { sampleRate: 16000, format: 's16le', channels: 1 },
      }));
    });
    ws.on('message', (data) => {
      const msg = JSON.parse(String(data));
      if (msg.type === 'session_ready') {
        clearTimeout(fail);
        ws.close();
        resolve(msg.lesson.words);
      } else if (msg.type === 'error') {
        clearTimeout(fail);
        ws.close();
        reject(new Error('session error: ' + JSON.stringify(msg)));
      }
    });
  });
}

/** Select words in the browse list, then drive the 批量指定 modal. */
async function assignThroughUI(page, words, textbookLabel, lessonLabel, include) {
  // Selection SURVIVES the browse refresh that follows every assign —
  // clear it first, or each batch would silently re-assign everything
  // still checked (the UNIQUE(card, textbook) row gets REPLACED).
  const stale = page.locator('.word-row .word-select:checked');
  while (await stale.count() > 0) {
    await stale.first().uncheck();
  }
  for (const w of words) {
    await page.locator(`.word-row[data-word="${w}"]`)
      .getByRole('checkbox', { name: `选择 ${w}` }).check();
  }
  await page.locator('#bulk-assign-textbook-btn').click();
  const dialog = page.locator('.modal:not(.modal-wide)');
  await expect(dialog).toBeVisible();
  await dialog.locator('#assign-textbook').selectOption({ label: textbookLabel });
  await dialog.locator('#assign-lesson').selectOption({ label: lessonLabel });
  if (include) {
    await dialog.locator('#assign-include').check();
  }
  await dialog.getByRole('button', { name: '确认指定' }).click();
  await expect(dialog).toBeHidden({ timeout: 15_000 });
}

/** Create one textbook through the 教材管理 modal's create form. */
async function createTextbook(page, name, lessonsText, edition) {
  await page.locator('#textbook-manage-btn').click();
  const mgr = page.locator('.modal.modal-wide');
  await expect(mgr).toBeVisible();
  await mgr.locator('#tb-name').fill(name);
  await mgr.locator('#tb-category').selectOption(CAT);
  if (edition) {
    await mgr.locator('#tb-edition').fill(edition);
  }
  await mgr.locator('#tb-lessons').fill(lessonsText);
  await mgr.getByRole('button', { name: '创建教材' }).click();
  await expect(mgr.locator('.textbook-row', { hasText: name })).toBeVisible({ timeout: 15_000 });
  await mgr.getByRole('button', { name: '取消' }).click();
  await expect(mgr).toBeHidden();
}

/** Set one textbook's learned progress through the 教材管理 modal. */
async function setProgress(page, bookName, optionLabel) {
  await page.locator('#textbook-manage-btn').click();
  const mgr = page.locator('.modal.modal-wide');
  await expect(mgr).toBeVisible();
  const row = mgr.locator('.textbook-row', { hasText: bookName });
  await row.locator(`select[aria-label="已学进度 ${bookName}"]`)
    .selectOption({ label: optionLabel });
  await expect(row.locator('.textbook-progress')).toContainText(optionLabel, { timeout: 15_000 });
  await mgr.getByRole('button', { name: '取消' }).click();
  await expect(mgr).toBeHidden();
}

test('textbook scope: browser-built scope shapes real classroom selection, progress changes apply immediately, participation stays orthogonal', async ({ page, request }) => {
  await deleteUnit(request);
  // Seed the vocabulary through the real REST → Python → SQLite path.
  const seed = await request.post('/api/v1/words', {
    data: {
      category: CAT, unit: UNIT,
      entries: ALL_WORDS.map((w) => ({ word: w, meaning: '范围e2e' })),
    },
  });
  expect(seed.ok()).toBeTruthy();

  try {
    await openWordLib(page);
    for (const w of ALL_WORDS) {
      await expect(page.locator(`.word-row[data-word="${w}"]`)).toBeVisible();
    }

    // Two textbooks; 快乐英语's lesson list order (C,A,B) deliberately
    // contradicts dictionary order — progress must follow the LIST.
    await createTextbook(page, BOOK1, 'C课\nA课\nB课', '人教版');
    await createTextbook(page, BOOK2, 'Z课\nY课', '');

    // Batch attribution through the UI (AC-2: management fully usable
    // without any recognition feature).
    await assignThroughUI(page, [WORDS.C1, WORDS.C2], BOOK1_LABEL, 'C课', false);
    await assignThroughUI(page, [WORDS.B1], BOOK1_LABEL, 'B课', false);
    await assignThroughUI(page, [WORDS.Inc], BOOK1_LABEL, 'B课', true);
    await assignThroughUI(page, [WORDS.Pend], BOOK1_LABEL, '待确认（课次不确定）', false);
    await assignThroughUI(page, [WORDS.Z1], BOOK2, 'Z课', false);

    // The attribution chips render on the refreshed browse rows; 待确认
    // carries its warning styling (AC-5's 提示).
    await expect(page.locator(`.word-row[data-word="${WORDS.C1}"] .textbook-chip`))
      .toHaveText(`${BOOK1}·C课`);
    // The include override rides the chip as the explicit-scope suffix.
    await expect(page.locator(`.word-row[data-word="${WORDS.Inc}"] .textbook-chip`))
      .toHaveText(`${BOOK1}·B课（已加入范围）`);
    const pendChip = page.locator(`.word-row[data-word="${WORDS.Pend}"] .textbook-chip`);
    await expect(pendChip).toHaveText(`${BOOK1}·待确认`);
    await expect(pendChip).toHaveClass(/textbook-chip-pending/);

    // Progress 1 unlocks C课 — the LIST's first lesson, not the
    // dictionary-first A课 (AC-3/AC-4).
    await setProgress(page, BOOK1, '已学完第 1 课「C课」');

    // Classroom probe 1: exactly the eligible set. B课/Z课 unlearned and
    // 待确认 stay out; the include override and supplementary words pass.
    let picked = sorted(await practiceSelection());
    expect(picked).toEqual(sorted([WORDS.C1, WORDS.C2, WORDS.Inc, WORDS.S1, WORDS.S2]));

    // Browser-side progress changes: B课 becomes learned (progress 3) in
    // 快乐英语, and 阳光英语 advances independently to Z课 (progress 1).
    await setProgress(page, BOOK1, '已学完第 3 课「B课」');
    await setProgress(page, BOOK2, '已学完第 1 课「Z课」');

    // ≥1.1s since probe 1 (r0x ids are second-granular): the very next
    // classroom must serve the EXPANDED set — no cache, no restart.
    await page.waitForTimeout(1100);
    picked = sorted(await practiceSelection());
    expect(picked).toEqual(sorted([WORDS.C1, WORDS.C2, WORDS.B1, WORDS.Inc, WORDS.Z1, WORDS.S1, WORDS.S2]));

    // Orthogonal with #2: the participation switch removes one eligible
    // word from selection while every scope rule keeps holding.
    await page.locator(`.word-row[data-word="${WORDS.S2}"]`)
      .getByRole('checkbox', { name: `加入默写范围 ${WORDS.S2}` }).uncheck();
    await expect(page.locator(`.word-row[data-word="${WORDS.S2}"]`)
      .getByRole('checkbox', { name: `加入默写范围 ${WORDS.S2}` })).not.toBeChecked();

    await page.waitForTimeout(1100);
    picked = sorted(await practiceSelection());
    expect(picked).toEqual(sorted([WORDS.C1, WORDS.C2, WORDS.B1, WORDS.Inc, WORDS.Z1, WORDS.S1]));
  } finally {
    await deleteUnit(request);
  }
});
