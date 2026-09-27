/**
 * iPad layout E2E — UAT 3/5/6/7/11, driven at the real iPad viewports in
 * both orientations via Playwright viewport emulation:
 *
 *   UAT 3  — portrait 768x1024 is the primary design target
 *   UAT 5  — landscape 1024x768 switches to the horizontal layout
 *   UAT 6  — media-query layout ACTUALLY differs between orientations
 *   UAT 7  — kid touch targets: kid-button >=72px, session button >=88px
 *   UAT 11 — no zoom-on-focus: visualViewport.scale stays 1 on input focus
 *
 * Discriminators are read from the real stylesheet (styles/main.css):
 *   - iPad MQ: `@media (min-device-width: 768px) and (max-device-width:
 *     1024px)` bumps .progress-container to 24px, .kid-button min-height
 *     to 72px, .session-button min-height to 88px.
 *   - Landscape sub-MQ (`... and (orientation: landscape)`) re-overrides
 *     .progress-container to 18px and .container justify-content to
 *     flex-start — so a computed-style delta between orientations proves
 *     the media queries really fire, not just that the page renders.
 */
// @ts-check
const { test, expect } = require('@playwright/test');

/**
 * Read the layout discriminators from the live page. A detached probe
 * button (class="kid-button", display-preserved) is used to measure the
 * generic .kid-button rule: the only kid-button the idle page renders is
 * the session CTA, whose .session-button rule (88px) intentionally
 * overrides the generic 72px one.
 */
async function readLayout(page) {
  return page.evaluate(() => {
    const computed = (el) => (el ? getComputedStyle(el) : null);

    const probe = document.createElement('button');
    probe.type = 'button';
    probe.className = 'kid-button';
    probe.style.position = 'absolute';
    probe.style.visibility = 'hidden';
    document.body.appendChild(probe);
    const kidStyle = getComputedStyle(probe);
    const out = {
      ipadMQ: window.matchMedia('(min-device-width: 768px) and (max-device-width: 1024px)').matches,
      landscapeMQ: window.matchMedia('(orientation: landscape)').matches,
      // Orientation discriminator #1: landscape sub-MQ flips justify-content.
      containerJustify: computed(document.querySelector('.container')).justifyContent,
      // Orientation discriminator #2: 24px (iPad MQ) vs 18px (landscape).
      // Fixed-height computed style resolves even though the section is
      // still [hidden] before a session starts.
      progressTrackHeight: computed(document.querySelector('.progress-container')).height,
      kidMinHeight: kidStyle.minHeight,
      sessionMinHeight: computed(document.querySelector('.session-button')).minHeight,
      sessionTouchAction: computed(document.querySelector('.session-button')).touchAction,
      bodyTouchAction: getComputedStyle(document.body).touchAction,
    };
    probe.remove();
    return out;
  });
}

/**
 * UAT 11 — focusing the batch-mode checkbox input must not scale the
 * viewport (zoom-on-focus defense: touch-action: manipulation + 16px+
 * input font + maximum-scale=1.0 viewport meta).
 */
async function assertFocusDoesNotZoom(page) {
  const scale = await page.evaluate(() => {
    const vv = document.visualViewport;
    const before = vv ? vv.scale : 1;
    const input = document.getElementById('batch-mode-toggle');
    if (!input) return { before, after: null, missing: true };
    input.focus();
    const after = vv ? vv.scale : 1;
    input.blur();
    return { before, after, missing: false };
  });
  expect(scale.missing, '#batch-mode-toggle input must exist').toBe(false);
  expect(scale.before).toBe(1);
  expect(scale.after, 'input focus must not change visualViewport scale').toBe(1);
}

test.describe('iPad portrait 768x1024 (UAT 3/6/7/11)', () => {
  test.use({ viewport: { width: 768, height: 1024 } });

  test('portrait: iPad media query applies, touch targets sized, focus does not zoom', async ({ page }) => {
    await page.goto('/');

    const l = await readLayout(page);

    // The iPad MQ must fire at this viewport (device-width tracking).
    expect(l.ipadMQ, 'min/max-device-width:768..1024 MQ must match 768px viewport').toBe(true);
    expect(l.landscapeMQ).toBe(false);

    // UAT 6 (portrait side): landscape overrides must NOT be applied.
    expect(l.containerJustify).toBe('normal');
    expect(l.progressTrackHeight).toBe('24px');

    // UAT 7: kid-sized touch targets.
    expect(parseFloat(l.kidMinHeight)).toBeGreaterThanOrEqual(72);
    expect(parseFloat(l.sessionMinHeight)).toBeGreaterThanOrEqual(88);
    // The real rendered CTA is at least its computed min-height.
    const box = await page.locator('.session-button').boundingBox();
    expect(box).not.toBeNull();
    expect(box.height).toBeGreaterThanOrEqual(88);

    // Touch interaction hygiene: no double-tap zoom on interactive elements.
    expect(l.sessionTouchAction).toBe('manipulation');
    expect(l.bodyTouchAction).toBe('manipulation');

    // UAT 11: focusing an input never scales the viewport.
    await assertFocusDoesNotZoom(page);
  });
});

test.describe('iPad landscape 1024x768 (UAT 5/6/7)', () => {
  test.use({ viewport: { width: 1024, height: 768 } });

  test('landscape: layout actually differs from portrait, targets stay large', async ({ page }) => {
    await page.goto('/');

    const l = await readLayout(page);

    expect(l.ipadMQ, 'iPad MQ must still match at 1024px wide').toBe(true);
    expect(l.landscapeMQ).toBe(true);

    // UAT 5/6 (landscape side): the landscape sub-MQ overrides ARE applied,
    // and both discriminators differ from the portrait values above —
    // computed proof the orientation media queries actually switch layout.
    expect(l.containerJustify).toBe('flex-start');
    expect(l.progressTrackHeight).toBe('18px');

    // UAT 7: touch targets do not shrink in landscape.
    expect(parseFloat(l.kidMinHeight)).toBeGreaterThanOrEqual(72);
    expect(parseFloat(l.sessionMinHeight)).toBeGreaterThanOrEqual(88);
    expect(l.sessionTouchAction).toBe('manipulation');
  });
});
