/**
 * RT observable browser pins (Wave 3 gaps) — mock /rt stack.
 * Complements rt-loop / word-controls with explicit RT-02/05/06 gates that
 * stay in Chromium (no device soak).
 */
// @ts-check
const { test, expect } = require('@playwright/test');

const QUIET = 'interrupt_rms=99&interrupt_sustain_ms=999999';

async function boot(page, extra = '') {
  const q = extra ? `${QUIET}&${extra}` : QUIET;
  await page.goto(`/tutor/?${q}`);
  await page.click('#rt-start');
  await expect
    .poll(() => page.evaluate(() =>
      (window.__rtTutor ? window.__rtTutor.client.sessionId : null)),
    { timeout: 20_000 })
    .toBeTruthy();
}

test('RT-02: scripted lesson reaches check without hold-to-talk UI', async ({ page }) => {
  await boot(page, 'mock_step_ms=800');
  await expect(page.locator('[data-hold-to-talk], .hold-to-talk, #holdToTalk')).toHaveCount(0);
  await expect(page.locator('#rt-check')).toBeVisible({ timeout: 60_000 });
  await expect(page.locator('#rt-check .rt-check-row')).toHaveCount(3);
});

test('RT-06: pause stays paused until explicit resume', async ({ page }) => {
  await boot(page, 'mock_pause=1');
  await expect
    .poll(() => page.getAttribute('#rt-status', 'data-state'), { timeout: 30_000 })
    .toBe('paused');
  await page.evaluate(() => window.__rtTutor.client.sendJSON({ type: '__test_resume' }));
  await expect
    .poll(() => page.getAttribute('#rt-status', 'data-state'), { timeout: 30_000 })
    .toBe('listening');
});
