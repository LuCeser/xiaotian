/**
 * Main browser smoke — the minimum end-to-end dictation flow:
 *
 *   学生打开网页 → (WebSocket 连接成功) → 点击开始按钮 →
 *   session_ready → 进入 DICTATING → 进度 UI 更新 → AudioContext 可用
 *
 * Every selector/label below is read from the real frontend code:
 *   - pkg/web/static/index.html      (#connectionStatus, section structure)
 *   - pkg/web/static/js/ui-components.js  (SessionButton labels — the idle
 *     label is the literal English "I want to dictate 30 words", mounted by
 *     JS into #session-button-container; ProgressDisplay status strings)
 *   - pkg/web/static/js/app.js       (setConnectionState indicator states)
 *   - pkg/web/static/js/state-manager.js (STATUS_DICTATING snapshot fields)
 *   - pkg/web/static/styles/main.css (themed computed styles)
 *
 * Tolerance policy: connection/session/progress states are what the DOM
 * reflects regardless of TTS health, so they are hard assertions (with
 * generous timeouts, no artificial sleeps). Audible-frame-dependent
 * assertions are wrapped so they soft-skip (test.info annotation) when no
 * audio frames arrive within the window (e.g. .env without TTS keys).
 */
// @ts-check
const { test, expect } = require('@playwright/test');

// Idle CTA — the DICT-01 literal rendered by SessionButton (ui-components.js).
const START_BUTTON_ROLE = { name: 'Start a dictation session with 30 words' };

test('student opens the page, connects, and starts a dictation session', async ({ page }) => {
  await page.goto('/');

  // ------------------------------------------------------------------
  // CSS actually applied — computed styles, not raw HTML presence.
  // ------------------------------------------------------------------
  const bodyBg = await page.evaluate(() => getComputedStyle(document.body).backgroundImage);
  expect(bodyBg, 'main.css body gradient must be applied').toContain('linear-gradient');

  const startButton = page.getByRole('button', START_BUTTON_ROLE);
  await expect(startButton).toBeVisible();
  // .session-button overrides .kid-button's flat #6a8dff with its gradient
  // CTA (main.css line ~309) — the themed look lands in backgroundImage.
  const btnBg = await startButton.evaluate((el) => getComputedStyle(el).backgroundImage);
  expect(btnBg, 'session-button CTA must carry its themed gradient (#4facfe → #00f2fe)')
    .toContain('linear-gradient(135deg');

  // ------------------------------------------------------------------
  // Connection indicator transitions to the connected state.
  // app.js drives #connectionStatus[data-state] from the WebSocketClient
  // state machine (state-manager → subscriber → setConnectionState).
  // ------------------------------------------------------------------
  const indicator = page.locator('#connectionStatus');
  await expect(indicator).toHaveAttribute('data-state', 'connected', { timeout: 15_000 });
  // The dot repaints via the CSS attribute selector — proof the state made
  // it all the way from the socket into themed DOM.
  await expect(indicator.locator('.status-dot')).toHaveCSS('background-color', 'rgb(16, 185, 129)', { timeout: 5_000 });
  await expect(indicator.locator('.connection-text')).toHaveText('已连接');

  // ------------------------------------------------------------------
  // ONE real click on the start button (also the audio unlock gesture).
  // ------------------------------------------------------------------
  await startButton.click();

  // In-flight feedback: the label leaves the idle copy and the button locks
  // (T-08-07). Do NOT pin "Starting session..." — a warm cache can flip to
  // "Session in progress..." before this assertion schedules.
  await expect(startButton, 'button must leave the idle label after click')
    .not.toHaveText('I want to dictate 30 words');
  await expect(startButton).toBeDisabled();

  // ------------------------------------------------------------------
  // Session reaches DICTATING (session_ready → SessionStateManager → DOM).
  // Generous timeout: session_ready waits on real FSRS + TTS pregeneration
  // server-side (config python.pregenTimeout is 120s).
  // ------------------------------------------------------------------
  await expect(startButton).toHaveText('Session in progress...', { timeout: 60_000 });

  // app.js mirrors state.status onto <body data-session-state> for theming.
  await expect(page.locator('body')).toHaveAttribute('data-session-state', 'DICTATING', { timeout: 10_000 });

  // Progress UI: #progressDisplay is revealed and ProgressDisplay renders
  // the dictating status + word counter from the state store.
  const progressSection = page.locator('#progressDisplay');
  await expect(progressSection).toBeVisible({ timeout: 10_000 });
  await expect(progressSection.locator('.status-text')).toHaveText('听写进行中', { timeout: 10_000 });
  await expect(progressSection.locator('.word-counter')).toHaveText(/已完成 \d+ \/ \d+ 个词/, { timeout: 10_000 });

  // The authoritative state snapshot agrees with what the DOM shows.
  const state = await page.evaluate(() => window.__app.stateManager.getState());
  expect(state.sessionActive).toBe(true);
  expect(state.status).toBe('DICTATING');
  expect(Array.isArray(state.words)).toBe(true);
  expect(state.words.length, 'backend must promise at least one due word').toBeGreaterThan(0);
  expect(state.totalWords).toBe(state.words.length);
  expect(state.currentWordId).toBe(state.words[0]);

  // Themed accent: .progress-display-section border-top consumes
  // var(--state-accent) which body[data-session-state='DICTATING'] pins to
  // #2563eb (main.css state accent system).
  await expect(progressSection).toHaveCSS('border-top-color', 'rgb(37, 99, 235)', { timeout: 10_000 });

  // ------------------------------------------------------------------
  // AudioContext unlocked and running (D-03 unlock ran inside the click).
  // ------------------------------------------------------------------
  await expect
    .poll(() => page.evaluate(() => {
      const ac = window.audioController;
      return ac && ac.audioCtx ? ac.audioCtx.state : null;
    }), { timeout: 10_000 })
    .toBe('running');

  // ------------------------------------------------------------------
  // Audible frames — soft-skip when TTS never streams (no credentials /
  // unreachable provider). Connection/session/progress above do not
  // depend on this, which is exactly what the minimum flow requires.
  // ------------------------------------------------------------------
  let framesArrived = true;
  try {
    await page.waitForFunction(() => {
      const ac = window.audioController;
      return !!ac && (ac.lastSeq > 0 || (Array.isArray(ac.rollingPcm) && ac.rollingPcm.length > 0));
    }, { timeout: 20_000 });
  } catch (e) {
    framesArrived = false;
    test.info().annotations.push({
      type: 'info',
      description: 'soft-skip: no audible TTS frames arrived within 20s ' +
        '(TTS credentials/network unavailable?). Connection, session and ' +
        'progress states remain fully verified.',
    });
  }

  if (framesArrived) {
    // Frames are 640-byte PCM chunks; the controller only accumulates
    // conforming ones, so lastSeq > 0 proves real audio is flowing.
    const frameInfo = await page.evaluate(() => ({
      lastSeq: window.audioController.lastSeq,
      ctxState: window.audioController.audioCtx.state,
    }));
    expect(frameInfo.lastSeq).toBeGreaterThan(0);
    expect(frameInfo.ctxState).toBe('running');
  }
});
