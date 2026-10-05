/**
 * tests/e2e-rt/rt-loop.spec.js — mock-server e2e for the /tutor/ page
 * (Phase 13, Plan 05 → dictation-flow rewrite 13-11). Lives in its OWN
 * directory (tests/e2e-rt) with its own config (playwright.rt.config.js) so
 * the legacy suite (playwright.config.js: testDir tests/e2e, real Go backend
 * webServer, NO testMatch filter) never picks this spec up — the isolation
 * is itself an acceptance criterion (`npx playwright test --list` must not
 * contain rt-loop, review H-1).
 *
 * The page is loaded with ?interrupt_rms=99&interrupt_sustain_ms=999999 so
 * the RMS auto-detector cannot fire on Chromium's fake audio device — the
 * deterministic interrupt path under test is the explicit interruptNow()
 * hook. Real-mic/speaker interruption belongs to the real-device checks.
 * Script selectors (?mock_pause / ?mock_empty / ?mock_ack_timeout /
 * ?mock_step_ms) ride the page URL onto the /rt upgrade request (the real
 * Go mux matches by path and ignores the query).
 *
 *   1. Complete classroom — one getUserMedia authorization for the WHOLE
 *      dictation lesson (RT-02 backstop), 3 words advance via
 *      lesson_update, the wrap-up summary's real last source.onended
 *      precedes its sentence_ack which precedes session_ended
 *      (lesson_complete), completion banner renders, subtitles keep the
 *      summary, the drain path releases resources with ZERO stopNow calls
 *      (natural ending never truncates scheduled audio, T-13-24/24b).
 *   2. Interrupt during the second announcement — interruptNow() during
 *      word 2: mock receives interrupt {epoch, last_sentence_id,
 *      played_until_ms}, interrupt_ack bumps the epoch, the interrupted
 *      sentence is never acked and its audio never revives, and the
 *      classroom still advances to lesson_complete without skipping a word.
 *   3. Pause & no-due — ?mock_pause=1: the page shows paused and STAYS
 *      paused after the pause-confirmation speech is fully played and
 *      acked; only the test resume frame returns it to listening (voice
 *      resume proof belongs to 13-12/real backend). Then ?mock_empty=1:
 *      the no-due explainer plays fully, its ack precedes
 *      session_ended(no_due_words), the notice renders, and every
 *      MediaStreamTrack stops (child-audio privacy boundary).
 *   4. Playback-ack timeout drain — ?mock_ack_timeout=1: the browser
 *      processes ErrorMsg(playback_ack_timeout) → session_ended
 *      (lesson_complete — the ORIGINAL natural reason) → normal close,
 *      with summary sources still queued: terminalDraining holds,
 *      stopNowCalls stay flat, shared playback resources stay held; only
      the real last source.onended lets drainNaturally resolve and release.
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

/**
 * Session state polled until the mock recorded its terminal close — the
 * mock pushes 'close' ~50 ms after session_ended, which can race a plain
 * read issued right after the client hits the ended state.
 */
async function getFinalSession(page) {
  let s = null;
  await expect
    .poll(async () => {
      s = await getSession(page);
      return s.terminalOrder.length >= 3 ? 'closed' : 'closing';
    }, { timeout: 15_000 })
    .toBe('closed');
  return s;
}

/** Read-only drain observables from the page (13-11). */
async function getObserve(page) {
  return page.evaluate(() => window.__rtTutor.observe());
}

/** Ordered client observation trace (playback + page notes). */
async function getTrace(page) {
  return page.evaluate(() => window.__rtTutor.playback.trace.slice());
}

/** Silence the RMS auto-detector; tests interrupt explicitly. */
const QUIET_PARAMS = 'interrupt_rms=99&interrupt_sustain_ms=999999';

test('complete classroom runs on a single getUserMedia authorization', async ({ page }) => {
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

  await page.goto('/tutor/?' + QUIET_PARAMS);
  await expect(page.locator('body[data-page="tutor"]')).toBeVisible();
  // Dynamic placeholder before session_ready — no hardcoded 3-word total.
  await expect(page.locator('#rt-progress-text')).toHaveText('第 - / - 个词');
  await page.click('#rt-start');

  // session_ready arrived → connected + session bound.
  await expect
    .poll(() => page.evaluate(() => window.__rtTutor && window.__rtTutor.client.sessionId),
      { timeout: 20_000 })
    .toBeTruthy();

  // The scripted lesson runs to its natural end (3 confirms + summary).
  await expect
    .poll(() => page.getAttribute('#rt-status', 'data-state'), { timeout: 60_000 })
    .toBe('ended');

  // Progress reached the completed presentation (dynamic word count).
  await expect(page.locator('#rt-progress-text')).toHaveText('已完成');
  // Completion banner (reason=lesson_complete presentation).
  await expect(page.locator('#rt-banner')).toBeVisible();
  // Subtitles RETAIN the wrap-up summary (opening + w2 + w3 + summary).
  await expect
    .poll(() => page.locator('#rt-teacher .t-line').count(), { timeout: 10_000 })
    .toBeGreaterThanOrEqual(4);
  const lastLine = await page.locator('#rt-teacher .t-line').last().textContent();
  expect(lastLine).toContain('结束啦');

  // The single authorization held for the whole classroom (RT-02 backstop).
  expect(await page.evaluate(() => navigator.mediaDevices.__gumCallCount())).toBe(1);

  // Client-side ordering: the summary's real last onended → its
  // sentence_ack → session_ended(lesson_complete) → close. The server
  // closes ~50 ms after session_ended, so poll until the client's close
  // note has landed before reading the full ordering.
  await expect
    .poll(async () => ((await getTrace(page)).includes('close') ? 'y' : 'n'), { timeout: 15_000 })
    .toBe('y');
  const trace = await getTrace(page);
  const onendedIdx = trace.lastIndexOf('onended:1:sum');
  const ackIdx = trace.indexOf('ack:1:sum');
  const endedIdx = trace.indexOf('session_ended:lesson_complete');
  expect(onendedIdx, 'summary onended observed').toBeGreaterThanOrEqual(0);
  expect(ackIdx, 'ack strictly after the real last onended').toBeGreaterThan(onendedIdx);
  expect(endedIdx, 'session_ended strictly after the ack').toBeGreaterThan(ackIdx);
  expect(trace.indexOf('close')).toBeGreaterThan(endedIdx);

  // Natural ending drains: resources release AFTER drain completes, with
  // ZERO stopNow calls — the summary was never truncated (T-13-24b).
  const obs = await getObserve(page);
  expect(obs.stopNowCalls).toBe(0);
  expect(obs.terminalDraining).toBe(true);
  expect(obs.playbackResourceReleased).toBe(true);
  expect(trace).toContain('drain:drained');
  expect(trace.indexOf('release')).toBeGreaterThan(trace.indexOf('drain:drained'));

  // Uplink stopped by the drain (child-audio privacy boundary).
  await expect
    .poll(() => page.evaluate(() => window.__rtTutor.capture.isCapturing), { timeout: 10_000 })
    .toBe(false);

  // No page errors across the loop; the clock-offset probe still ran.
  expect(pageErrors, JSON.stringify(pageErrors)).toEqual([]);
  await expect
    .poll(() => page.evaluate(() => window.__rtTutor.timing.count('clock_offset')),
      { timeout: 20_000 })
    .toBeGreaterThanOrEqual(1);

  // Mock-side: exactly 3 lesson_updates, terminal order ack-gated.
  const s = await getFinalSession(page);
  expect(s.lessonUpdates).toBe(3);
  expect(s.currentIndex).toBe(3);
  expect(s.lessonCompleteSent).toBe(true);
  expect(s.terminalOrder).toEqual(['terminal_ack', 'session_ended', 'close']);
});

test('interrupt during the second announcement bumps the epoch and the lesson still completes', async ({ page }) => {
  const pageErrors = [];
  page.on('pageerror', (err) => pageErrors.push(String(err)));

  await page.goto('/tutor/?' + QUIET_PARAMS);
  await page.click('#rt-start');
  await expect
    .poll(() => page.evaluate(() => window.__rtTutor && window.__rtTutor.client.sessionId),
      { timeout: 20_000 })
    .toBeTruthy();

  // Atomic wait: at least 2 teacher lines are up, the teacher is speaking,
  // AND the client still has sources queued for the current sentence —
  // with pendingSources > 0 the sentence provably cannot have acked yet,
  // so the interrupt genuinely cancels in-flight audio (no race with the
  // DOM's speaking display lagging the audio clock).
  await expect
    .poll(async () => {
      const st = await page.getAttribute('#rt-status', 'data-state');
      const n = await page.locator('#rt-teacher .t-line').count();
      const obs = await page.evaluate(() => window.__rtTutor.observe());
      return n >= 2 && st === 'speaking' && obs.pendingSources > 0 ? 'ready' : 'waiting';
    }, { timeout: 30_000 })
    .toBe('ready');
  await page.evaluate(() => window.__rtTutor.interruptNow());

  // The mock received the interrupt control message with all three fields.
  const s = await getSession(page);
  expect(s.interrupts.length).toBeGreaterThanOrEqual(1);
  const first = s.interrupts[0];
  expect(first.epoch).toBeGreaterThanOrEqual(1);
  expect(typeof first.last_sentence_id).toBe('string');
  expect(typeof first.played_until_ms).toBe('number');
  // interrupt_ack bumped the epoch.
  expect(s.epoch).toBe(first.epoch + 1);

  // The interrupted sentence is completion-poisoned: never acked.
  const interrupted = first.epoch + ':' + first.last_sentence_id;
  const oldAcks = s.acks.filter((a) => a.epoch === first.epoch && a.sentence_id === first.last_sentence_id);
  expect(oldAcks.length, 'interrupted sentence never acked').toBe(0);

  // The classroom continues under the new epoch to its natural completion.
  await expect
    .poll(() => page.getAttribute('#rt-status', 'data-state'), { timeout: 60_000 })
    .toBe('ended');
  const s2 = await getFinalSession(page);
  expect(s2.lessonCompleteSent).toBe(true);
  expect(s2.currentIndex).toBe(3);
  expect(s2.lessonUpdates).toBe(3); // an interrupt never skips or advances a word
  expect(s2.terminalOrder).toEqual(['terminal_ack', 'session_ended', 'close']);

  // Client side: no ack for the interrupted key — the old audio never
  // produced completion evidence. And nothing revives: no onended entry for
  // the interrupted key may appear AFTER the stopNow marker (chunks that
  // legally finished before the interrupt are allowed to be in the trace).
  const trace = await getTrace(page);
  expect(trace.includes('ack:' + interrupted)).toBe(false);
  const stopIdx = trace.indexOf('stopNow');
  expect(stopIdx, 'interrupt recorded a stopNow').toBeGreaterThanOrEqual(0);
  const revived = trace.filter((t, i) => i > stopIdx && t === 'onended:' + interrupted);
  expect(revived.length, 'interrupted audio never revives after stopNow').toBe(0);
  // The new-epoch re-announcement completed normally.
  expect(trace.includes('ack:' + s2.epoch + ':sum')).toBe(true);

  expect(pageErrors, JSON.stringify(pageErrors)).toEqual([]);
});

test('pause stays paused after the confirmation speech; no-due path ends honestly', async ({ page }) => {
  // --- Phase A: ?mock_pause=1 -----------------------------------------
  await page.goto('/tutor/?' + QUIET_PARAMS + '&mock_pause=1');
  await page.click('#rt-start');
  await expect
    .poll(() => page.evaluate(() => window.__rtTutor && window.__rtTutor.client.sessionId),
      { timeout: 20_000 })
    .toBeTruthy();

  // The paused chip activates.
  await expect
    .poll(() => page.getAttribute('#rt-status', 'data-state'), { timeout: 30_000 })
    .toBe('paused');
  // The pause-confirmation speech fully plays AND is acked by the page…
  await expect
    .poll(async () => (await getTrace(page)).includes('ack:1:p1'), { timeout: 30_000 })
    .toBe(true);
  // …and the page STILL shows paused afterwards (no silent resume).
  await expect
    .poll(() => page.getAttribute('#rt-status', 'data-state'), { timeout: 5_000 })
    .toBe('paused');

  // Only the resume input returns the page to listening (voice resume is
  // proven against the real backend in 13-12 — the mock is a stand-in).
  await page.evaluate(() => window.__rtTutor.client.sendJSON({ type: '__test_resume' }));
  await expect
    .poll(() => page.getAttribute('#rt-status', 'data-state'), { timeout: 30_000 })
    .toBe('listening');
  const s = await getSession(page);
  expect(s.pauseSent).toBe(true);
  expect(s.resumed).toBe(true);

  // --- Phase B: ?mock_empty=1 -----------------------------------------
  await page.goto('/tutor/?' + QUIET_PARAMS + '&mock_empty=1');
  await page.click('#rt-start');
  await expect
    .poll(() => page.evaluate(() => window.__rtTutor && window.__rtTutor.client.sessionId),
      { timeout: 20_000 })
    .toBeTruthy();

  await expect
    .poll(() => page.getAttribute('#rt-status', 'data-state'), { timeout: 60_000 })
    .toBe('ended');

  // Explainer ack strictly before session_ended(no_due_words).
  const trace = await getTrace(page);
  const ackIdx = trace.indexOf('ack:1:nodue');
  const endedIdx = trace.indexOf('session_ended:no_due_words');
  expect(ackIdx, 'no-due explainer was acked').toBeGreaterThanOrEqual(0);
  expect(endedIdx, 'session_ended strictly after the explainer ack').toBeGreaterThan(ackIdx);
  expect(trace).toContain('drain:drained');

  // The no-due notice renders; no completion banner for this reason.
  const notice = await page.textContent('#rt-notice');
  expect(notice).toContain('今天没有到期单词');
  expect(await page.locator('#rt-banner').isVisible()).toBe(false);

  // Child-audio privacy boundary: every MediaStreamTrack is stopped.
  await expect
    .poll(() => page.evaluate(() => window.__rtTutor.capture.isCapturing), { timeout: 10_000 })
    .toBe(false);
  const obs = await getObserve(page);
  expect(obs.stopNowCalls).toBe(0);
  expect(obs.playbackResourceReleased).toBe(true);

  const s2 = await getFinalSession(page);
  expect(s2.noDueSent).toBe(true);
  expect(s2.terminalOrder).toEqual(['terminal_ack', 'session_ended', 'close']);
});

test('playback-ack timeout drains with sources queued and releases only after the real last onended', async ({ page }) => {
  const pageErrors = [];
  page.on('pageerror', (err) => pageErrors.push(String(err)));

  await page.goto('/tutor/?' + QUIET_PARAMS + '&mock_ack_timeout=1');
  await page.click('#rt-start');
  await expect
    .poll(() => page.evaluate(() => window.__rtTutor && window.__rtTutor.client.sessionId),
      { timeout: 20_000 })
    .toBeTruthy();

  // The degraded order arrives while the summary is still playing.
  await expect
    .poll(async () => ((await getTrace(page)).includes('error:playback_ack_timeout') ? 'y' : 'n'),
      { timeout: 60_000 })
    .toBe('y');

  // Right after the timeout error: terminal draining holds, nothing is
  // released, sources are still queued, and no stopNow has happened.
  const obs1 = await getObserve(page);
  expect(obs1.terminalDraining).toBe(true);
  expect(obs1.playbackResourceReleased).toBe(false);
  expect(obs1.pendingSources, 'summary sources still queued at error time').toBeGreaterThan(0);
  expect(obs1.stopNowCalls).toBe(0);

  // The normal close must not change any of that (close beats the audio).
  await expect
    .poll(async () => ((await getTrace(page)).includes('close') ? 'y' : 'n'), { timeout: 20_000 })
    .toBe('y');
  const obs2 = await getObserve(page);
  expect(obs2.stopNowCalls).toBe(0);
  expect(obs2.playbackResourceReleased).toBe(false);
  expect(obs2.pendingSources, 'sources still queued at close time').toBeGreaterThan(0);

  // Ordered degraded sequence, exactly as the 13-07 contract freezes it:
  // ErrorMsg(playback_ack_timeout) → session_ended(lesson_complete) → close.
  const traceAtClose = await getTrace(page);
  const errIdx = traceAtClose.indexOf('error:playback_ack_timeout');
  const endedIdx = traceAtClose.indexOf('session_ended:lesson_complete');
  const closeIdx = traceAtClose.indexOf('close');
  expect(errIdx).toBeGreaterThanOrEqual(0);
  expect(endedIdx, 'timeout error precedes session_ended').toBeGreaterThan(errIdx);
  expect(closeIdx, 'session_ended precedes the normal close').toBeGreaterThan(endedIdx);

  // The timeout is NOT completion evidence: no summary ack existed at any
  // point before the audio actually finished (a post-drain ack attempt
  // against the closed socket is harmless and carries no server weight).
  const ackIdx = traceAtClose.indexOf('ack:1:sum');
  expect(ackIdx === -1 || ackIdx > closeIdx, 'no summary ack before the degraded order').toBe(true);

  // Only the real last source.onended lets drainNaturally resolve and
  // release the shared playback resources — still zero stopNow calls, so
  // the watchdog never had to truncate (T-13-24b bounded cleanup unneeded).
  await expect
    .poll(async () => (await getObserve(page)).playbackResourceReleased, { timeout: 30_000 })
    .toBe(true);
  const obs3 = await getObserve(page);
  expect(obs3.stopNowCalls).toBe(0);
  expect(obs3.terminalDraining).toBe(true);
  const traceEnd = await getTrace(page);
  expect(traceEnd).toContain('drain:drained');
  expect(traceEnd.indexOf('release')).toBeGreaterThan(traceEnd.indexOf('drain:drained'));

  // reason=lesson_complete still renders the completion banner on the
  // degraded path (the banner keys on the reason, never on the timeout).
  await expect(page.locator('#rt-banner')).toBeVisible();

  // Mock-side: the terminal order carried the timeout error, the ORIGINAL
  // natural reason, and the close — and the summary ack never landed.
  const s = await getFinalSession(page);
  expect(s.terminalOrder).toEqual(['timeout_error', 'session_ended', 'close']);
  expect(s.summaryAcked).toBe(false);
  expect(s.lessonCompleteSent).toBe(true);

  expect(pageErrors, JSON.stringify(pageErrors)).toEqual([]);
});
