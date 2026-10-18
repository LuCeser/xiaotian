/**
 * UAT-7 forceStudentMode browser gate — hermetic stack with
 * HTA_RT_FORCESTUDENTMODE=true (playwright.family.config.js).
 *
 * Opens /tutor/?mode=test and asserts the classroom does NOT full-reveal
 * word surfaces (production anti-fat-finger). Uses fake media flags only.
 */
// @ts-check
const { test, expect } = require('@playwright/test');

test.use({
  launchOptions: {
    args: [
      '--use-fake-ui-for-media-stream',
      '--use-fake-device-for-media-stream',
    ],
  },
});

test('UAT-7: forceStudentMode masks mode=test reveal', async ({ page }) => {
  await page.goto('/tutor/?mode=test');
  await page.click('#rt-start');
  // Fresh DB → no_due_words natural ending, or a masked lesson view.
  // Either way mode=test must not dump full card JSON into the DOM.
  await expect
    .poll(() => page.getAttribute('#rt-status', 'data-state'), { timeout: 60_000 })
    .toMatch(/ended|listening|speaking|paused/);
  const body = await page.locator('body').innerText();
  expect(body).not.toMatch(/mode=test 全显|测试模式全显|TEST MODE REVEAL/i);
  const hasDump = await page.evaluate(() => {
    const t = (document.body && document.body.innerText) || '';
    return t.includes('"meaning"') && t.includes('"example"') && t.includes('"word"');
  });
  expect(hasDump).toBe(false);
});
