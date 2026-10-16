/**
 * tests/e2e-rt/family-accent.spec.js — single family accent e2e (Issue #16,
 * 13.3 UI review fix 2) on the mock /rt server (same host as home-button).
 *
 * The family journey home.html -> /tutor/ -> home.html must ride ONE accent
 * color end to end: the home primary door, the in-lesson current-word chip,
 * the unified check submit and the terminal 回首页 door all resolve the
 * same --accent token (#2563eb) in the REAL cascade. Assertions read
 * computed styles off the real elements, so both halves of the original
 * defect fail here — a second hardcoded blue renders a different rgb(), and
 * an undefined var() falls back to transparent, not to any blue at all.
 *
 * Layer note: static-token unity (same declaration in home.css/parent.css/
 * tutor.css) is pinned textually by pkg/infra/static_accent_test.go; this
 * spec proves what the browser actually paints on the journey path.
 */
// @ts-check
const { test, expect } = require('@playwright/test');

/** Silence the RMS auto-detector (rt-loop.spec.js convention). */
const QUIET_PARAMS = 'interrupt_rms=99&interrupt_sustain_ms=999999';

/** #2563eb as Chromium's computed backgroundColor. */
const ACCENT_RGB = 'rgb(37, 99, 235)';

/** Computed backgroundColor of the first element matching selector. */
async function bgColor(page, selector) {
  return page.evaluate((sel) => {
    const el = document.querySelector(sel);
    return el ? getComputedStyle(el).backgroundColor : null;
  }, selector);
}

/** Computed backgroundColor of a class-scoped rule probed via a throwaway
 *  element (the real rule still applies — the probe rides the page's
 *  actual stylesheet cascade). ancestor wraps the probe when the rule is a
 *  descendant selector (e.g. ".rt-banner .rt-btn-home"). */
async function probeClassColor(page, className, ancestor) {
  return page.evaluate(({ cls, anc }) => {
    const host = document.createElement('div');
    host.className = anc || '';
    const el = document.createElement('div');
    el.className = cls;
    host.appendChild(el);
    document.body.appendChild(host);
    const bg = getComputedStyle(el).backgroundColor;
    host.remove();
    return bg;
  }, { cls: className, anc: ancestor });
}

/** The :root --accent custom property as the page resolves it. */
async function rootAccent(page) {
  return page.evaluate(() =>
    getComputedStyle(document.documentElement)
      .getPropertyValue('--accent')
      .trim());
}

async function getSessionId(page) {
  return page.evaluate(() =>
    (window.__rtTutor ? window.__rtTutor.client.sessionId : null));
}

/** Wait for session_ready to bind the page's client. */
async function awaitSessionId(page) {
  await expect
    .poll(() => getSessionId(page), { timeout: 20_000 })
    .toBeTruthy();
}

/**
 * Drive the 13.1-01 check stage from the page: manual 对 per row (waiting
 * for each server echo, RT-07), then the unified submit. Local copy of
 * home-button.spec.js's helper (specs do not export). Returns the submit
 * button locator so callers can assert its painted color before clicking.
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
  return submitBtn;
}

test('home and tutor pages resolve the same --accent in the cascade', async ({ page }) => {
  const pageErrors = [];
  page.on('pageerror', (err) => pageErrors.push(String(err)));

  // Home: the primary door is the accent-filled surface of the family entry.
  await page.goto('/home.html');
  expect(await rootAccent(page), 'home.css :root --accent').toBe('#2563eb');
  expect(await bgColor(page, '.home-door-primary')).toBe(ACCENT_RGB);

  // Tutor, no session needed — the accent rules are class-scoped, so probe
  // them through throwaway elements riding the real stylesheet.
  await page.goto('/tutor/');
  expect(await rootAccent(page), 'tutor.css :root --accent').toBe('#2563eb');
  expect(await probeClassColor(page, 'rt-check-submit'),
    'check submit fill').toBe(ACCENT_RGB);
  expect(await probeClassColor(page, 'rt-btn-home', 'rt-banner'),
    '回首页 fill').toBe(ACCENT_RGB);
  // The active state chip needs its data-state ancestor — build the real
  // selector structure the page's status bar uses.
  const chipBg = await page.evaluate(() => {
    const state = document.createElement('div');
    state.className = 'rt-state';
    state.dataset.state = 'listening';
    const chip = document.createElement('span');
    chip.className = 'rt-state-chip';
    chip.dataset.chip = 'listening';
    state.appendChild(chip);
    document.body.appendChild(state);
    const bg = getComputedStyle(chip).backgroundColor;
    state.remove();
    return bg;
  });
  expect(chipBg, 'active state chip fill').toBe(ACCENT_RGB);

  expect(pageErrors, JSON.stringify(pageErrors)).toEqual([]);
});

test('round trip home → tutor → 回首页 → home paints one accent, no console errors', async ({ page }) => {
  const pageErrors = [];
  page.on('pageerror', (err) => pageErrors.push(String(err)));
  const consoleErrors = [];
  page.on('console', (msg) => {
    if (msg.type() === 'error') consoleErrors.push(msg.text());
  });

  // --- Home side of the seam: the door INTO the classroom.
  await page.goto('/home.html');
  const doorOut = await bgColor(page, '.home-door-primary');
  expect(doorOut).toBe(ACCENT_RGB);

  // Cross the seam through the real door (13.3 built this navigation).
  await page.click('a[href="/tutor/"]');
  await page.waitForURL('**/tutor/');
  // Mock params ride the page URL — reload with them for a fast lesson.
  await page.goto('/tutor/?' + QUIET_PARAMS + '&mock_step_ms=100');

  // --- Inside the classroom: mid-lesson current-word chip is the accent.
  await page.click('#rt-start');
  await awaitSessionId(page);
  await expect
    .poll(async () => await bgColor(page, '.rt-words .rt-word-current'),
      { timeout: 30_000 })
    .toBe(ACCENT_RGB);

  // --- Check stage: the unified submit carries the accent.
  const submitBtn = await driveCheckStage(page, 3);
  expect(await bgColor(page, '#rt-check .rt-check-submit')).toBe(ACCENT_RGB);
  await submitBtn.click();

  // --- Natural ending: the 回首页 door out of the classroom.
  await expect
    .poll(() => page.getAttribute('#rt-status', 'data-state'), { timeout: 60_000 })
    .toBe('ended');
  const home = page.locator('#rt-banner a.rt-btn-home');
  await expect(home).toBeVisible({ timeout: 30_000 });
  expect(await bgColor(page, '#rt-banner a.rt-btn-home')).toBe(ACCENT_RGB);

  // --- Back on home: the same door, the same blue.
  await home.click();
  await page.waitForURL('**/home.html');
  const doorBack = await bgColor(page, '.home-door-primary');
  expect(doorBack).toBe(ACCENT_RGB);
  expect(doorBack, 'round-trip door colors must match').toBe(doorOut);

  expect(pageErrors, JSON.stringify(pageErrors)).toEqual([]);
  expect(consoleErrors, JSON.stringify(consoleErrors)).toEqual([]);
});
