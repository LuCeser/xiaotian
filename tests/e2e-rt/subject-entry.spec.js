/**
 * Issue #55 browser pins — the tutor page's subject declaration:
 *   - the UNSPECIFIED entry (/tutor/ with no category param) sends
 *     session_init.category="auto" (the server-side model judgment —
 *     starting never forces a manual pre-selection);
 *   - an explicit ?category=english|chinese is forwarded VERBATIM (the
 *     user's word rides the wire untouched);
 *   - the page ADOPTS the resolved subject from session_ready.category,
 *     so a later in-page re-init (再练一组) rides the subject the classroom
 *     actually ran under instead of being re-judged (mock_category echoes
 *     a fixed subject for that pin).
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
  // classroom; the auto entry's judgment landing as chinese, the page must
  // keep that subject (never re-auto, never re-judged) for 再练一组.
  const init = await bootAndCaptureInit(page, 'mock_step_ms=5000&mock_category=chinese');
  expect(init.category).toBe('auto');
  await expect
    .poll(() => page.evaluate(() => window.__rtTutor.client.initCategory), { timeout: 20_000 })
    .toBe('chinese');
});
