/**
 * Issue #55 browser pins — the tutor page's subject declaration:
 *   - the UNSPECIFIED entry (/tutor/ with no category param) sends
 *     session_init.category="auto" (the server settles it with the child —
 *     one subject due opens it directly, both due brings the clarify — so
 *     starting never forces a manual pre-selection);
 *   - an explicit ?category=english|chinese is forwarded VERBATIM (the
 *     user's word rides the wire untouched);
 *   - the page ADOPTS the resolved subject from session_ready.category,
 *     so a later in-page re-init (再练一组) rides the subject the classroom
 *     actually ran under instead of being re-asked (mock_category echoes
 *     a fixed subject for that pin);
 *   - the clarify flow: ?mock_subject_ask=1 parks the auto entry on
 *     subject_ask (no session_ready yet), the page shows the question and
 *     the two pick buttons, tapping one sends subject_pick, and the
 *     classroom then opens under the PICKED subject (adopted for re-inits).
 */
// @ts-check
const { test, expect } = require('@playwright/test');

const QUIET = 'interrupt_rms=99&interrupt_sustain_ms=999999';

// bootAndCaptureInit opens the tutor page under extraParams, starts the
// session, and resolves the first session_init control frame the page
// sends (parsed JSON), plus the page-exposed client handle.
async function bootAndCaptureInit(page, extraParams) {
  const q = extraParams ? `${QUIET}&${extraParams}` : QUIET;
  const inits = [];
  page.on('websocket', (ws) => {
    ws.on('framesent', (frame) => {
      const payload = frame.payload;
      if (typeof payload !== 'string' && payload instanceof Buffer === false) return;
      try {
        const msg = JSON.parse(payload.toString());
        if (msg && msg.type === 'session_init') inits.push(msg);
      } catch (parseErr) { /* binary audio frames are not JSON */ }
    });
  });
  await page.goto(`/tutor/?${q}`);
  await page.click('#rt-start');
  await expect
    .poll(() => inits.length, { timeout: 20_000 })
    .toBeGreaterThanOrEqual(1);
  return inits[0];
}

test('#55: unspecified entry sends category=auto (no forced pre-selection)', async ({ page }) => {
  const init = await bootAndCaptureInit(page, 'mock_step_ms=5000');
  expect(init.category).toBe('auto');
});

test('#55: explicit subject declarations ride the wire verbatim', async ({ page }) => {
  for (const [param, want] of [['category=english', 'english'], ['category=chinese', 'chinese']]) {
    const init = await bootAndCaptureInit(page, `mock_step_ms=5000&${param}`);
    expect(init.category).toBe(want);
  }
});

test('#55: the page adopts the resolved subject for later re-inits', async ({ page }) => {
  // mock_category=chinese makes the mock's session_ready CLAIM a chinese
  // classroom; the auto entry's outcome landing as chinese, the page must
  // keep that subject (never re-auto, never re-asked) for 再练一组.
  const init = await bootAndCaptureInit(page, 'mock_step_ms=5000&mock_category=chinese');
  expect(init.category).toBe('auto');
  await expect
    .poll(() => page.evaluate(() => window.__rtTutor.client.initCategory), { timeout: 20_000 })
    .toBe('chinese');
});

test('#55: both subjects due — the clarify buttons answer subject_pick and bind the round', async ({ page }) => {
  const picks = [];
  page.on('websocket', (ws) => {
    ws.on('framesent', (frame) => {
      const payload = frame.payload;
      if (typeof payload !== 'string' && payload instanceof Buffer === false) return;
      try {
        const msg = JSON.parse(payload.toString());
        if (msg && msg.type === 'subject_pick') picks.push(msg);
      } catch (parseErr) { /* binary audio frames are not JSON */ }
    });
  });
  await page.goto(`/tutor/?${QUIET}&mock_step_ms=5000&mock_subject_ask=1`);
  await page.click('#rt-start');

  // The clarify question arrives and the panel turns on with both buttons.
  const ask = page.locator('#rt-subject-ask');
  await expect(ask).toBeVisible({ timeout: 20_000 });
  await expect(page.locator('#rt-subject-ask-text')).not.toBeEmpty();
  await expect(page.locator('#rt-subject-zh')).toBeEnabled();
  await expect(page.locator('#rt-subject-en')).toBeEnabled();

  // The child taps 语文: subject_pick(chinese) rides the wire, the mock
  // opens the classroom under it, and the page adopts the picked subject
  // (never re-auto) for later re-inits.
  await page.click('#rt-subject-zh');
  await expect
    .poll(() => picks.length, { timeout: 20_000 })
    .toBeGreaterThanOrEqual(1);
  expect(picks[0].category).toBe('chinese');
  await expect
    .poll(() => page.evaluate(() => window.__rtTutor.client.initCategory), { timeout: 20_000 })
    .toBe('chinese');
  await expect(ask).toBeHidden();
});
