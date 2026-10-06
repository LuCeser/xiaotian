/**
 * .scratch/e2e-1312/track2/tutor-real.spec.js — REAL-channel /tutor/ UI
 * verification (13-12 Task 2 Track 2). Scratch-only; lives outside tests/ by
 * design (the repo suites stay untouched).
 *
 * ENVIRONMENT REALITY (verified 2026-10-06, .hta-data/vocab.db + pkg/rt):
 *   resolveLessonID is day-keyed ("r0-YYYYMMDD", pkg/rt/dictation.go L512-520)
 *   and TODAY'S row r0-20261006 is COMPLETE (current_index 10/10, version 11,
 *   written by Track 1's rt-7 run). handleSessionInit (pkg/rt/turn.go L444-455)
 *   therefore takes the startCompletedSession branch (L493-509): session_ready
 *   → session_ended(lesson_complete) → close, with NO greeting, NO audio and
 *   NO uplink turns. Due words exist (10) but FetchDueWords only runs on
 *   lesson-not-found — they cannot produce an active lesson today, and
 *   modifying vocab.db / restarting the service is out of bounds.
 *
 *   Consequently the assertions below cover the REACHABLE real-channel states:
 *     A. completed-resume classroom UI (progress "已完成", completion banner,
 *        notice, ended state, natural-drain release with zero stopNow,
 *        child-audio privacy boundary, error box stays hidden).
 *   and record as CODE-LEVEL (static-on-real-channel) verification:
 *     B. error mapping table entries for lesson_unavailable and
 *        playback_ack_timeout served over the real TLS channel; their trigger
 *        conditions are not reachable today (documented per assertion).
 *
 * The no_due_words UI path is likewise UNREACHABLE today (10 due words exist;
 * the completed row short-circuits before the due fetch) — code-level entry
 * in track2-results.json, no fake DOM assertion here.
 */
// @ts-check
const { test, expect } = require('@playwright/test');

const BANNER_TEXT = '今天的默写课堂完成啦，真棒！';
const COMPLETE_NOTICE = '本次课程已结束。需要再练一次就点「开始上课」。';
const LESSON_UNAVAILABLE_TEXT = '取词失败了，请稍后再试';
const ACK_TIMEOUT_TEXT = '播放确认超时，老师会先把已收到的语音播完；若卡住将自动结束';
const LESSON_UNAVAILABLE_RETRY = '点「开始上课」可以重新试一次。';

test('completed-resume classroom: real-channel UI reaches ended with banner and drain release', async ({ page }) => {
  const pageErrors = [];
  page.on('pageerror', (err) => pageErrors.push(String(err)));

  await page.goto('/tutor/');
  await expect(page.locator('body[data-page="tutor"]')).toBeVisible();
  // Dynamic placeholder before session_ready — no hardcoded word total.
  await expect(page.locator('#rt-progress-text')).toHaveText('第 - / - 个词');

  await page.click('#rt-start');
  // session_ready arrived → session bound (completed-resume answers fast).
  await expect
    .poll(() => page.evaluate(() => window.__rtTutor && window.__rtTutor.client.sessionId),
      { timeout: 20_000 })
    .toBeTruthy();

  // session_ready carried the completed row (words=10, current_index=10):
  // renderProgress must present the COMPLETED state, never "第 11 / 10 个词".
  await expect(page.locator('#rt-progress-text')).toHaveText('已完成');
  await expect(page.locator('#rt-progress-fill')).toHaveAttribute('style', /width:\s*100%/);
  // All 10 word chips rendered; none highlighted current (index 10 is past
  // the last chip — no out-of-range highlight).
  await expect(page.locator('#rt-words .rt-word-chip')).toHaveCount(10);
  await expect(page.locator('#rt-words .rt-word-current')).toHaveCount(0);

  // startCompletedSession sends session_ended(lesson_complete) immediately.
  await expect
    .poll(() => page.getAttribute('#rt-status', 'data-state'), { timeout: 30_000 })
    .toBe('ended');
  // Completion banner (reason=lesson_complete presentation).
  await expect(page.locator('#rt-banner')).toBeVisible();
  await expect(page.locator('#rt-banner')).toHaveText(BANNER_TEXT);
  // End-of-lesson notice with the restart hint.
  await expect(page.locator('#rt-notice')).toBeVisible();
  await expect(page.locator('#rt-notice')).toContainText(COMPLETE_NOTICE);

  // Zero-error classroom: no ErrorMsg ever surfaced (completed resume plays
  // nothing and fails nothing) — the error box must stay hidden.
  await expect(page.locator('#rt-error')).toBeHidden();

  // Natural ending takes the SAME terminal drain as the mock suite: uplink
  // stops (child-audio privacy boundary) and shared playback resources are
  // released with ZERO stopNow calls (nothing scheduled was ever truncated).
  await expect
    .poll(() => page.evaluate(() => window.__rtTutor.capture.isCapturing), { timeout: 10_000 })
    .toBe(false);
  await expect
    .poll(() => page.evaluate(() => window.__rtTutor.observe().playbackResourceReleased),
      { timeout: 15_000 })
    .toBe(true);
  const obs = await page.evaluate(() => window.__rtTutor.observe());
  expect(obs.stopNowCalls).toBe(0);

  // Connection chip walks connected → closed (课程已结束 → 已断开).
  await expect
    .poll(() => page.getAttribute('#rt-conn', 'data-conn'), { timeout: 10_000 })
    .toBe('closed');

  // Restart entry stays available after the drain (start re-enabled).
  await expect
    .poll(() => page.evaluate(() => !document.getElementById('rt-start').disabled),
      { timeout: 15_000 })
    .toBe(true);

  expect(pageErrors, JSON.stringify(pageErrors)).toEqual([]);
});

test('error mapping table served over the real channel; unreachable triggers documented', async ({ page }) => {
  // Static-on-real-channel: the mapping table that the page actually loads
  // must carry both fixed human wordings (RT-10: every failure surfaces a
  // human-readable reason).
  const js = await page.request.get('/tutor/js/rt-client.js');
  expect(js.ok(), 'rt-client.js served over the real TLS channel').toBe(true);
  const jsBody = await js.text();
  expect(jsBody).toContain(LESSON_UNAVAILABLE_TEXT);
  expect(jsBody).toContain(ACK_TIMEOUT_TEXT);

  // The page glue that CONSUMES the two codes must be the served HTML:
  // lesson_unavailable → retry hint + re-enabled start (index.html error
  // handler); playback_ack_timeout → terminal drain, never stopNow.
  const html = await page.request.get('/tutor/');
  expect(html.ok(), '/tutor/ served').toBe(true);
  const htmlBody = await html.text();
  expect(htmlBody).toContain(LESSON_UNAVAILABLE_RETRY);
  expect(htmlBody).toContain("code === 'playback_ack_timeout'");
  expect(htmlBody).toContain("code === 'lesson_unavailable'");
  expect(htmlBody).toContain("reason === 'no_due_words'");
  expect(htmlBody).toContain('今天没有到期单词，休息一天吧。');

  // DOM reachability honesty: neither trigger condition exists in TODAY'S
  // real channel. lesson_unavailable requires failLessonInit (words=nil,
  // RunDue error, EnsureLessonCards error, or legacy empty cards —
  // pkg/rt/turn.go L398-450) and playback_ack_timeout requires an in-flight
  // announcement whose ack never lands (turn.go L1215+) — today's
  // completed-resume path plays no announcement at all. No DOM assertion is
  // made for them here; the live DOM just must NOT be showing the error box.
  await page.goto('/tutor/');
  await expect(page.locator('#rt-error')).toBeHidden();
});
