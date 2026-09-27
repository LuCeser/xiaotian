/**
 * UAT 8 logic check — iOS audio unlock happens as the direct result of ONE
 * single click on the session start button (D-03 3-condition unlock):
 *
 *   1. AudioContext construction,
 *   2. resume(),
 *   3. silent-buffer start —
 *
 * all synchronously inside the user gesture (SessionButton.handleClick →
 * audioController.unlockAudioContext, app.js/ui-components.js wiring).
 *
 * Playwright's page.click() dispatches a trusted user gesture, so the same
 * code path real fingers take on iPad Safari is exercised here. Before the
 * click NO AudioContext exists at all (AudioController constructs it lazily
 * inside unlockAudioContext), so a passing run proves the context was both
 * created AND resumed by that single gesture.
 */
// @ts-check
const { test, expect } = require('@playwright/test');

test('a single start-button click results in a running AudioContext', async ({ page }) => {
  await page.goto('/');

  // Precondition: no AudioContext exists before any gesture (lazy D-03
  // construction — see audio-controller.js constructor: this.audioCtx=null).
  const before = await page.evaluate(() => {
    const ac = window.audioController;
    return { hasController: !!ac, hasCtx: !!(ac && ac.audioCtx) };
  });
  expect(before.hasController, 'app.js must expose window.audioController').toBe(true);
  expect(before.hasCtx, 'AudioContext must not exist before the click').toBe(false);

  // THE single click (counts as a user gesture in Chromium).
  await page.getByRole('button', { name: 'Start a dictation session with 30 words' }).click();

  // The controller's own readiness helper: audioCtx && state === 'running'.
  await expect
    .poll(() => page.evaluate(() => {
      const ac = window.audioController;
      return ac && ac.audioCtx ? ac.audioCtx.state : null;
    }), { timeout: 10_000 })
    .toBe('running');
  expect(await page.evaluate(() => window.audioController.isAudioCtxReady())).toBe(true);
});
