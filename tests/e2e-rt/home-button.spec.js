/**
 * tests/e2e-rt/home-button.spec.js — terminal-state 「回首页」 timing e2e
 * (Phase 13.3 Plan 04, ROADMAP success criterion 5) on the mock /rt server.
 *
 * The link is appended ONLY inside releasePlaybackResources (the terminal
 * drain's completion callback) and only when the ended session carried a
 * NATURAL reason (lesson_complete / practice_complete / no_due_words) —
 * never during the lesson, never on the explicit user-end path, and never
 * as a direct session_ended side effect (T-13.3-10 / RESEARCH Pitfall 6:
 * a link that appears before the audio is released lets the student tap
 * away mid-summary).
 *
 * Per-path timing notes (why each test asserts what it asserts):
 *   - Natural ack-gated ending (lesson_complete / practice_complete /
 *     no_due_words): the summary/notice has already fully PLAYED when
 *     session_ended arrives (the client's sentence_ack triggered it), so
 *     the drain resolves in the same JS turn — a "banner visible but link
 *     not yet there" window is NOT externally observable there. The
 *     honest ordering proof is the release contract: socket closed
 *     (readyState 3) + mic stream released (capture._stream null, the
 *     word-controls.spec.js assertion form) BEFORE the link-visible
 *     assertion, and the link riding the same callback that flips
 *     playbackResourceReleased.
 *   - ?mock_ack_timeout=1: the degraded order lands while the summary is
 *     still queued client-side (rt-loop's timeout-drain scenario), so the
 *     drain holds a REAL multi-second window after the socket is closed
 *     and the capture is stopped — the link must be ABSENT in exactly
 *     that window. This is the deterministic negative.
 *
 * Coverage trade-off (plan Task 2 ④): there is no URL-selector practice
 * script — practice_complete is reached through the real 再练一组 restart
 * flow, which this spec drives end-to-end (same showEndingBanner code
 * path, and it pins the second natural branch plus the start-flow reset).
 */
// @ts-check
const { test, expect } = require('@playwright/test');

/** Silence the RMS auto-detector (rt-loop.spec.js convention). */
const QUIET_PARAMS = 'interrupt_rms=99&interrupt_sustain_ms=999999';

async function getSessionId(page) {
  return page.evaluate(() =>
    (window.__rtTutor ? window.__rtTutor.client.sessionId : null));
}

async function getObserve(page) {
  return page.evaluate(() => window.__rtTutor.observe());
}

/** Wait for session_ready to bind the page's client. */
async function awaitSessionId(page) {
  await expect
    .poll(() => getSessionId(page), { timeout: 20_000 })
    .toBeTruthy();
}

/** Assert the release contract holds BEFORE the link may show. */
async function expectReleased(page) {
  await expect
    .poll(() => page.evaluate(() => window.__rtTutor.client.ws.readyState),
      { timeout: 20_000 })
    .toBe(3);
  await expect
    .poll(() => page.evaluate(() => window.__rtTutor.capture._stream),
      { timeout: 10_000 })
    .toBe(null);
}

/**
 * Drive the 13.1-01 check stage from the page: manual 对 per row (waiting
 * for each server echo, RT-07), then the unified submit through the honest
 * review_result copy. Local copy of rt-loop.spec.js's helper (specs do not
 * export) with the positional-rows shape this spec needs.
 */
async function driveCheckStage(page, rows) {
  await expect(page.locator('#rt-check')).toBeVisible({ timeout: 60_000 });
  const rowLoc = page.locator('#rt-check .rt-check-row');
  await expect(rowLoc).toHaveCount(rows);
  for (let i = 0; i < rows; i++) {
    await rowLoc.nth(i).locator('button').nth(0).click();
    await expect
      .poll(() => page.evaluate(() => window.__rtTutor.observe().check.checked),
        { timeout: 10_000 })
      .toBe(i + 1);
  }
  const submitBtn = page.locator('#rt-check .rt-check-submit');
  await expect(submitBtn).toBeEnabled({ timeout: 10_000 });
  await submitBtn.click();
  await expect
    .poll(async () => {
      const c = (await page.evaluate(() => window.__rtTutor.observe())).check;
      return !c.submitPending && c.resultText !== '' ? 'done' : 'waiting';
    }, { timeout: 30_000 })
    .toBe('done');
  return submitBtn;
}

test('lesson_complete: 回首页 appears only after socket close and stream release', async ({ page }) => {
  const pageErrors = [];
  page.on('pageerror', (err) => pageErrors.push(String(err)));

  await page.goto('/tutor/?' + QUIET_PARAMS + '&mock_step_ms=100');
  await page.click('#rt-start');
  await awaitSessionId(page);
  await driveCheckStage(page, 3);

  // Natural ending: banner + ended status.
  await expect
    .poll(() => page.getAttribute('#rt-status', 'data-state'), { timeout: 60_000 })
    .toBe('ended');
  await expect(page.locator('#rt-banner')).toBeVisible();

  // Release proofs FIRST (word-controls.spec.js assertion form): the socket
  // is closed and the mic stream is gone. Only then may the link show.
  await expectReleased(page);

  const home = page.locator('#rt-banner a.rt-btn-home');
  await expect(home).toBeVisible();
  await expect(home).toHaveAttribute('href', '/home.html');
  await expect(home).toHaveText('回首页');
  // The append rides the SAME callback that flips the release flag — by the
  // time the link is visible the shared playback resources are released.
  expect((await getObserve(page)).playbackResourceReleased).toBe(true);

  expect(pageErrors, JSON.stringify(pageErrors)).toEqual([]);
});

test('playback_ack_timeout: no 回首页 while the queued summary drains, link after release', async ({ page }) => {
  const pageErrors = [];
  page.on('pageerror', (err) => pageErrors.push(String(err)));

  await page.goto('/tutor/?' + QUIET_PARAMS + '&mock_ack_timeout=1');
  await page.click('#rt-start');
  await awaitSessionId(page);
  await driveCheckStage(page, 3);

  // Degraded order: the banner keys on the ORIGINAL natural reason
  // (lesson_complete — the timeout code is never a reason).
  await expect(page.locator('#rt-banner')).toBeVisible();

  // The degraded order closes the socket and stops the capture EARLY,
  // while the 6 s summary is still queued and draining.
  await expectReleased(page);

  // THE deterministic negative (T-13.3-10): at close+capture-released time
  // the drain is still holding the queued summary — a link here would let
  // the student tap away mid-summary (Pitfall 6).
  expect(await page.locator('#rt-banner a.rt-btn-home').count(),
    '回首页 must not exist while the drain holds the queued summary').toBe(0);

  // Only the real last source.onended releases the drain — the link
  // appears with it (checkpoint ④: the timeout path DOES get the door).
  await expect
    .poll(async () => (await getObserve(page)).playbackResourceReleased,
      { timeout: 30_000 })
    .toBe(true);
  const home = page.locator('#rt-banner a.rt-btn-home');
  await expect(home).toBeVisible();
  await expect(home).toHaveAttribute('href', '/home.html');
  await expect(home).toHaveText('回首页');

  expect(pageErrors, JSON.stringify(pageErrors)).toEqual([]);
});

test('mid-lesson: no 回首页 exists while the lesson is still running', async ({ page }) => {
  await page.goto('/tutor/?' + QUIET_PARAMS + '&mock_step_ms=5000');
  await page.click('#rt-start');
  await awaitSessionId(page);

  // Dictation in progress: word 1 current, check stage not yet open —
  // the classroom surface carries no cross-page navigation whatsoever
  // (success criterion 5: 课堂内无管理操作).
  await expect(page.locator('#rt-status')).toHaveAttribute('data-state', 'listening');
  await expect(page.locator('#rt-words .rt-word-current')).toHaveText('1');
  await expect(page.locator('#rt-check')).toBeHidden();
  expect(await page.locator('.rt-btn-home').count(),
    'no home link during the lesson').toBe(0);
});

test('no_due_words: the third natural ending also earns the 回首页 door', async ({ page }) => {
  const pageErrors = [];
  page.on('pageerror', (err) => pageErrors.push(String(err)));

  await page.goto('/tutor/?' + QUIET_PARAMS + '&mock_empty=1');
  await page.click('#rt-start');
  await awaitSessionId(page);

  await expect
    .poll(() => page.getAttribute('#rt-status', 'data-state'), { timeout: 60_000 })
    .toBe('ended');
  await expect(page.locator('#rt-banner')).toBeVisible();
  await expectReleased(page);

  const home = page.locator('#rt-banner a.rt-btn-home');
  await expect(home).toBeVisible();
  await expect(home).toHaveAttribute('href', '/home.html');

  expect(pageErrors, JSON.stringify(pageErrors)).toEqual([]);
});

test('practice_complete: the 再练一组 ending lands the door too (no door mid-practice)', async ({ page }) => {
  const pageErrors = [];
  page.on('pageerror', (err) => pageErrors.push(String(err)));

  // --- Session 1: the normal lesson → lesson_complete banner + door.
  await page.goto('/tutor/?' + QUIET_PARAMS + '&mock_step_ms=100');
  await page.click('#rt-start');
  await awaitSessionId(page);
  await driveCheckStage(page, 3);
  await expect
    .poll(() => page.getAttribute('#rt-status', 'data-state'), { timeout: 60_000 })
    .toBe('ended');
  await expectReleased(page);
  await expect(page.locator('#rt-banner a.rt-btn-home')).toBeVisible();

  // --- Session 2: the one-tap restart (same start flow — the start button
  // unblocks only after the previous drain released it).
  const practiceBtn = page.locator('#rt-banner .rt-practice-btn');
  await expect(practiceBtn).toBeVisible();
  const sid1 = await getSessionId(page);
  await expect(page.locator('#rt-start')).toBeEnabled({ timeout: 20_000 });
  await practiceBtn.click();
  await expect
    .poll(() => getSessionId(page), { timeout: 20_000 })
    .not.toBe(sid1);

  // Mid-practice: the OLD door must not leak into the new lesson — the
  // banner hides at session_ready and the start-flow reset latched the
  // door off (the stale banner children stay invisible, never clickable).
  await expect(page.locator('#rt-banner a.rt-btn-home')).toBeHidden();

  // The practice set runs the SAME three-stage flow (2 rows) to its
  // practice_complete ending.
  await driveCheckStage(page, 2);
  await expect
    .poll(() => page.getAttribute('#rt-status', 'data-state'), { timeout: 60_000 })
    .toBe('ended');
  await expect(page.locator('#rt-banner')).toBeVisible();
  await expect(page.locator('#rt-banner')).toContainText('这组练习完成啦');
  await expectReleased(page);

  const home = page.locator('#rt-banner a.rt-btn-home');
  await expect(home).toBeVisible();
  await expect(home).toHaveAttribute('href', '/home.html');

  expect(pageErrors, JSON.stringify(pageErrors)).toEqual([]);
});
