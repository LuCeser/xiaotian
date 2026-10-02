/**
 * tests/e2e-rt/rt-loop.spec.js — mock-server e2e for the /tutor/ page
 * (Phase 13, Plan 05, Task 3). Lives in its OWN directory (tests/e2e-rt)
 * with its own config (playwright.rt.config.js) so the legacy suite
 * (playwright.config.js: testDir tests/e2e, real Go backend webServer,
 * NO testMatch filter) never picks this spec up — the isolation is itself
 * an acceptance criterion (`npx playwright test --list` must not contain
 * rt-loop, review H-1).
 *
 * The page is loaded with ?interrupt_rms=99&interrupt_sustain_ms=999999 so
 * the RMS auto-detector cannot fire on Chromium's fake audio device — the
 * deterministic interrupt path under test is the explicit interruptNow()
 * hook (the URL override is the real-device calibration mechanism this
 * also exercises). Real-mic/speaker interruption belongs to 13-07.
 *
 *   1. 10-round loop — one getUserMedia authorization for the whole
 *      session (RT-02), 10 teacher sentences rendered, zero pageerrors,
 *      at least one clock_offset event (RT-04 probe, review M-6).
 *   2. Interruption — interruptNow() during round 3: mock receives the
 *      interrupt control message (epoch/last_sentence_id/played_until_ms),
 *      interrupt_ack bumps the epoch, the loop continues into a new round.
 *   3. End button — end_session → session_ended → UI ended state + mic
 *      released (child-audio privacy boundary).
 */
// @ts-check
const { test, expect } = require('@playwright/test');

/** Fetch the mock's recorded state for THIS page's session. */
async function getSession(page) {
  const sid = await page.evaluate(() =>
    (window.__rtTutor ? window.__rtTutor.client.sessionId : null));
  expect(sid, 'session_ready must have arrived (client.sessionId set)').toBeTruthy();
  const res = await page.request.get('/__test/state');
  expect(res.ok(), '/__test/state responds').toBe(true);
  const body = await res.json();
  const s = body.sessions[sid];
  expect(s, 'mock state exists for session ' + sid).toBeTruthy();
  return s;
}

test('10-round loop runs on a single getUserMedia authorization', async ({ page }) => {
  const pageErrors = [];
  page.on('pageerror', (err) => pageErrors.push(String(err)));

  // Count getUserMedia calls for the page's whole lifetime (RT-02).
  await page.addInitScript(() => {
    const md = navigator.mediaDevices;
    const orig = md.getUserMedia.bind(md);
    let count = 0;
    md.getUserMedia = (...args) => { count += 1; return orig(...args); };
    md.__gumCallCount = () => count;
  });

  await page.goto('/tutor/?interrupt_rms=99&interrupt_sustain_ms=999999');
  await expect(page.locator('body[data-page="tutor"]')).toBeVisible();
  await page.click('#rt-start');

  // session_ready arrived → connected + session bound.
  await expect
    .poll(() => page.evaluate(() => window.__rtTutor && window.__rtTutor.client.sessionId),
      { timeout: 20_000 })
    .toBeTruthy();

  // All 10 scripted rounds render their teacher sentence (~1 s per round).
  await expect
    .poll(() => page.locator('#rt-teacher .t-line').count(), { timeout: 90_000 })
    .toBe(10);

  // The single authorization held for the whole 10-round session.
  expect(await page.evaluate(() => navigator.mediaDevices.__gumCallCount())).toBe(1);
  expect(await page.evaluate(() => window.__rtTutor.capture.isCapturing)).toBe(true);

  // No page errors across the loop.
  expect(pageErrors, JSON.stringify(pageErrors)).toEqual([]);

  // At least one ping/pong clock-offset estimate landed in the timing log.
  await expect
    .poll(() => page.evaluate(() => window.__rtTutor.timing.count('clock_offset')),
      { timeout: 20_000 })
    .toBeGreaterThanOrEqual(1);

  // Lesson progress tracked the scripted lesson_update sequence.
  const progressText = await page.textContent('#rt-progress-text');
  expect(progressText).toContain('3');
});

test('interrupt during playback stops the old round and starts a new one', async ({ page }) => {
  await page.goto('/tutor/?interrupt_rms=99&interrupt_sustain_ms=999999');
  await page.click('#rt-start');

  // Wait until at least 3 rounds have rendered.
  await expect
    .poll(() => page.locator('#rt-teacher .t-line').count(), { timeout: 90_000 })
    .toBeGreaterThanOrEqual(3);

  // Interrupt while the teacher is still speaking.
  await expect
    .poll(() => page.getAttribute('#rt-status', 'data-state'), { timeout: 30_000 })
    .toBe('speaking');
  await page.evaluate(() => window.__rtTutor.interruptNow());

  // The mock received the interrupt control message with all three fields.
  const s = await getSession(page);
  expect(s.interrupts.length).toBeGreaterThanOrEqual(1);
  const first = s.interrupts[0];
  expect(first.epoch).toBeGreaterThanOrEqual(1);
  expect(typeof first.last_sentence_id).toBe('string');
  expect(typeof first.played_until_ms).toBe('number');

  // interrupt_ack bumped the epoch and the loop continued into a new round:
  // a 4th teacher sentence renders and the round indicator advances.
  await expect
    .poll(() => page.locator('#rt-teacher .t-line').count(), { timeout: 60_000 })
    .toBeGreaterThanOrEqual(4);
  const s2 = await getSession(page);
  expect(s2.epoch).toBeGreaterThan(first.epoch);
  const roundText = await page.textContent('#rt-round');
  expect(parseInt(String(roundText).replace(/[^0-9]/g, ''), 10)).toBeGreaterThanOrEqual(4);
});

test('end button ends the session and releases the microphone', async ({ page }) => {
  await page.goto('/tutor/?interrupt_rms=99&interrupt_sustain_ms=999999');
  await page.click('#rt-start');

  await expect
    .poll(() => page.locator('#rt-teacher .t-line').count(), { timeout: 90_000 })
    .toBeGreaterThanOrEqual(1);
  expect(await page.evaluate(() => window.__rtTutor.capture.isCapturing)).toBe(true);

  await page.click('#rt-end');

  // UI reaches the ended state after session_ended.
  await expect
    .poll(() => page.getAttribute('#rt-status', 'data-state'), { timeout: 20_000 })
    .toBe('ended');

  // Child-audio privacy boundary: the mic stream is released, not held.
  await expect
    .poll(() => page.evaluate(() => window.__rtTutor.capture.isCapturing),
      { timeout: 10_000 })
    .toBe(false);

  const s = await getSession(page);
  expect(s.endReceived).toBe(true);
  expect(s.sessionEndedSent).toBe(true);
});
