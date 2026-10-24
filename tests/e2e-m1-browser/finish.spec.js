// finish.spec.js — J13–J16, the m1 browser line's ENDING acceptance (issue
// #58, e2e-test-plan §7 step 15). Same B-layer contract as the earlier specs:
// real /tutor/ page, real Go /rt + Python + per-test SQLite, real REST
// seeding; replaced parts: vendor answers (strict stub, tone PCM) and the mic
// source (fake mic). The spec never acks on the classroom's behalf, never
// mutates state, never fakes an onended — completion evidence is only the
// production player's real onended → sentence_ack, and every listening resume
// must follow the round's LAST ack (listeningAfterPlayback).
//
//   J13 last word   — exactly ONE check entry, current_index reaches the
//                     word-count boundary, answers masked until
//                     check_started, no phantom next word, no "saved" before
//                     the submit;
//   J14 check & end — the REAL 对/错 buttons, one submit, honest
//                     review_result + saved copy, the summary fully acked
//                     BEFORE session_ended(lesson_complete), mic + playback
//                     resources released, uplink frozen;
//   J15/J16 follow in later tests of this file (主动结束、完成后再开/换科).
'use strict';

const { test, expect } = require('@playwright/test');
const {
  installRtObserver, waitWs, controls, playbackState, playedBeforeAck, listeningAfterPlayback,
  wsStats,
} = require('./helpers/rt-observe');
const { installFakeMic, micInfo, playPcm16 } = require('./helpers/fake-mic');
const { startSpecService, makeStubControl } = require('./helpers/spec-service');

const stub = makeStubControl();
let svc;

test.beforeEach(async () => {
  await stub.reset();
  svc = await startSpecService();
});
test.afterEach(async () => {
  try { if (svc) await svc.stop(); } finally {
    svc = null;
    await stub.reset();
  }
});

// One deterministic spoken-confirm corpus clip: 2 s of 440 Hz s16le @16 kHz —
// loud enough for the server VAD, one segment after the 700 ms hangover (the
// voice-input.spec.js shape). The ASR stub answers 「好了」 for it.
function tonePcm16Base64(ms = 2000, freq = 440, rate = 16000, amp = 0.6) {
  const n = Math.round((ms * rate) / 1000);
  const buf = Buffer.alloc(n * 2);
  for (let i = 0; i < n; i++) {
    buf.writeInt16LE(Math.round(Math.sin((2 * Math.PI * freq * i) / rate) * amp * 32767), i * 2);
  }
  return buf.toString('base64');
}
const CONFIRM_CLIP = tonePcm16Base64();

async function stubScripts(def) {
  const res = await stub.scripts(def);
  expect(res.ok, 'strict stub scripts install').toBeTruthy();
}

function stubState() {
  return stub.state();
}

async function lessonsNow() {
  return (await svc.listLessons()).body.lessons;
}

/** The page's read-only observables (drain flags, check gate). */
function observe(page) {
  return page.evaluate(() => window.__rtTutor.observe());
}

/** Wait until one log entry matches pred; returns its insertion index. */
async function waitIndex(page, pred, label) {
  let at = -1;
  await waitWs(page, (log) => {
    const hit = log.find(pred);
    if (hit) at = hit.i;
    return at >= 0;
  }, { timeoutMs: 30000, label });
  return at;
}

/**
 * Await one announcement round: the first `count` audio_start frames after
 * log index `fromI`. Returns {epoch, sids} for the ack/listening assertions.
 */
async function awaitRound(page, fromI, count, label) {
  let round = null;
  await waitWs(page, (log) => {
    const starts = log.filter((e) => e.dir === 'in' && e.type === 'audio_start' && e.i > fromI);
    if (starts.length >= count) {
      round = { epoch: starts[0].msg.epoch, sids: starts.slice(0, count).map((e) => e.msg.sentence_id) };
    }
    return !!round;
  }, { timeoutMs: 30000, label });
  return round;
}

/**
 * The round's sentences ack NATURALLY (production onended per source before
 * its sentence_ack); with settle (default) the server's listening must follow
 * the round's LAST ack — never sit between the final audio_end and ack.
 */
async function awaitAcked(page, round, label, { settle = true } = {}) {
  await waitWs(page, (log) => {
    const acks = log.filter((e) => e.dir === 'out' && e.type === 'sentence_ack'
      && e.msg.epoch === round.epoch && round.sids.includes(e.msg.sentence_id));
    return acks.length === round.sids.length;
  }, { timeoutMs: 30000, label: label + ' acked by the production player' });
  const { trace } = await playbackState(page);
  for (const sid of round.sids) {
    expect(playedBeforeAck(trace, round.epoch, sid), label + ': real onended before ack ' + sid).toBe(true);
  }
  if (settle) {
    await waitWs(page, (log) => listeningAfterPlayback(log, round.epoch, round.sids),
      { label: label + ': listening only after the last ack' });
  }
}

/** The child says 「好了」 — after the post-ack echo tail has passed. */
async function childSaysDone(page) {
  await page.waitForTimeout(1000); // past the 500 ms echo tail + poll margin
  await playPcm16(page, CONFIRM_CLIP, 16000);
}

/**
 * J14's shared tail (used by J13 and the J16 switch leg): the REAL 对/错
 * buttons with each authoritative check_verdicts echo, ONE submit, the
 * honest review_result (all-correct), the battle summary fully acked by the
 * production player BEFORE session_ended(lesson_complete), and the ending
 * banner's real save copy. Returns the goodbye's log index.
 */
async function checkSubmitNaturalEnd(page, words) {
  const rows = page.locator('#rt-check .rt-check-row');
  for (let i = 0; i < words.length; i++) {
    await rows.nth(i).locator('.rt-check-btn', { hasText: /^对$/ }).click();
    await waitWs(page, (log) => log.some((e) => e.dir === 'out' && e.type === 'check_verdict'
      && e.msg.word_index === i)
      && log.some((e) => e.dir === 'in' && e.type === 'check_verdicts'
        && (e.msg.verdicts || []).some((v) => v.word_index === i && v.manual && v.correct)),
    { label: 'verdict ' + (i + 1) + ' tapped and echoed' });
  }
  await expect(page.locator('#rt-check .rt-check-submit')).toBeEnabled();
  await page.locator('#rt-check .rt-check-submit').click();

  const submitI = await waitIndex(page, (e) => e.dir === 'out' && e.type === 'check_submit',
    'check_submit sent');
  const resI = await waitIndex(page, (e) => e.dir === 'in' && e.type === 'review_result'
    && e.msg.requested === words.length && e.msg.updated === words.length
    && e.msg.unchecked === false,
  'honest review_result: ' + words.length + ' requested, all really saved');
  expect(resI).toBeGreaterThan(submitI);

  // The natural ending WAITS for the summary audio: both sentences ack
  // naturally, and session_ended(lesson_complete) follows the LAST ack.
  const summary = await awaitRound(page, resI, 2, 'battle summary');
  await awaitAcked(page, summary, 'battle summary', { settle: false });
  const lastAckI = Math.max(...(await controls(page))
    .filter((e) => e.dir === 'out' && e.type === 'sentence_ack'
      && summary.sids.includes(e.msg.sentence_id))
    .map((e) => e.i));
  const endedI = await waitIndex(page, (e) => e.dir === 'in' && e.type === 'session_ended'
    && e.msg.reason === 'lesson_complete', 'session_ended(lesson_complete)');
  expect(endedI, 'the goodbye follows the summary\'s last real ack').toBeGreaterThan(lastAckI);

  await expect(page.locator('#rt-banner')).toContainText('今天的 ' + words.length + ' 个词都默写完了');
  await expect(page.locator('#rt-banner'))
    .toContainText('学习记录保存好了，' + words.length + ' 个词都记上啦。');
  await expect(page.locator('#rt-banner .rt-practice-btn')).toBeVisible();
  return endedI;
}

// The server synthesizes a whole announcement AHEAD of playback (per-sentence
// streaming, ack-gated only at the round's SETTLE), so an announcement that
// was killed mid-playback still shows 1..N consumed TTS entries — N is racy.
// The deterministic post-end invariant is STABILITY: nothing new is requested
// once the session is gone (a phantom reopening WOULD request more and also
// exhaust the script → a violation).
async function ttsCountStable() {
  const count = async () => (await stubState()).requests.filter((r) => r.category === 'tts').length;
  const first = await count();
  await new Promise((resolve) => setTimeout(resolve, 1200));
  const second = await count();
  return { first, second, stable: first === second };
}

test('J15 active end stops the classroom: ask-park, mid-greeting, listening', async ({ page }) => {
  await installRtObserver(page);
  await installFakeMic(page);
  // BOTH subjects hold due words → the generic entry parks on the subject ask.
  for (const [category, unit, words] of [
    ['chinese', 'e2e-m1-fin-zh', ['e2e-fin-zh-a', 'e2e-fin-zh-b']],
    ['english', 'e2e-m1-fin-en', ['e2e-fin-en-a', 'e2e-fin-en-b']],
  ]) {
    const seed = await svc.seedWords({ category, unit, entries: words.map((word) => ({ word })) });
    expect(seed.ok, 'seed ' + category).toBeTruthy();
  }
  const askStreaming = (log) => log.some((e) => e.dir === 'in' && e.type === 'subject_ask')
    && log.some((e) => e.dir === 'in' && e.type === 'audio_start')
    && log.some((e) => e.dir === 'in' && e.kind === 'binary' && e.bytes > 0);

  // ---- 选科中: the page offers NO end affordance before session_ready — the
  // 结束 button is still disabled while the ask parks. The child's only exit
  // is leaving the page; that must stop the ask with no classroom created.
  // The whole ask is scripted: an in-flight round may legitimately finish
  // synthesizing, but NOTHING new may follow once the socket is gone.
  await stubScripts({
    asr: [], llm: [], image: [],
    tts: [
      { ms: 1200, match: '都有要默写的词语' },
      { ms: 1200, match: '想先默写哪一科' },
      { ms: 1200, match: '也可以点屏幕上的按钮' },
    ],
  });
  await page.goto(svc.url + 'tutor/');
  await page.locator('#rt-start').click();
  await waitWs(page, askStreaming, { label: 'ask audio streaming' });
  await expect(page.locator('#rt-subject-ask')).toBeVisible();
  await expect(page.locator('#rt-end'), 'no end button pre-session_ready').toBeDisabled();
  expect(await lessonsNow(), 'no classroom row while parked on the ask').toHaveLength(0);
  await page.reload();
  await page.waitForTimeout(1500); // let the server see the dead socket
  expect(await lessonsNow(), 'leaving the ask creates no classroom').toHaveLength(0);
  const afterAsk = await ttsCountStable();
  expect(afterAsk.stable, 'no vendor traffic revives after the page left (' + afterAsk.first + '→' + afterAsk.second + ')').toBe(true);
  expect(afterAsk.second, 'at most the one scripted ask round was served').toBeLessThanOrEqual(3);
  expect((await stubState()).violations).toEqual([]);

  // ---- 播报中: end MID-GREETING playback → the goodbye, the classroom row
  // stands but never advances, live audio stops, nothing follows the goodbye.
  // The mid-ask pick interrupts the ask after 1..3 synthesized sentences, so
  // the ask/greeting FIFO split is racy — matched-free tones cover both
  // rounds; any speech BEYOND them exhausts the script → a violation.
  await stubScripts({
    asr: [], llm: [], image: [],
    tts: [{ ms: 1200 }, { ms: 1200 }, { ms: 1200 }, { ms: 1200 }, { ms: 1200 }, { ms: 1200 }],
  });
  await page.locator('#rt-start').click();
  await waitWs(page, askStreaming, { label: 'ask audio streaming (2nd session)' });
  await page.locator('#rt-subject-en').click();
  const readyI = await waitIndex(page, (e) => e.dir === 'in' && e.type === 'session_ready'
    && e.msg.category === 'english'
    && Array.isArray(e.msg.lesson.words) && e.msg.lesson.words.length === 2,
  'the picked english classroom opens');
  await waitWs(page, (log) => log.some((e) => e.dir === 'in' && e.kind === 'binary' && e.bytes > 0
    && e.i > readyI), { label: 'greeting audio streaming' });
  await page.locator('#rt-end').click();
  const ended2I = await waitIndex(page, (e) => e.dir === 'in' && e.type === 'session_ended'
    && e.msg.reason === 'client_end', 'session_ended(client_end) mid-greeting');
  expect(ended2I, 'the goodbye follows the classroom it closes').toBeGreaterThan(readyI);
  await waitWs(page, async () => (await playbackState(page)).activeSourceKeys.length === 0,
    { label: 'the explicit end cleared every live audio source' });
  expect(await page.evaluate(() => window.__rtTutor.capture.isCapturing),
    'capture stopped by the explicit end').toBe(false);
  const rowsAfter = await lessonsNow();
  expect(rowsAfter, 'the classroom row stands — no re-created classroom').toHaveLength(1);
  expect(rowsAfter[0]).toMatchObject({ category: 'english', word_count: 2, lesson_id: 'e2e-m1-browser' });
  await page.waitForTimeout(1200);
  expect((await controls(page)).filter((e) => e.i > ended2I && e.dir === 'in' && e.type !== 'pong'),
    'no control frame follows the goodbye — no new opening').toEqual([]);
  const afterMid = await ttsCountStable();
  expect(afterMid.stable, 'no vendor traffic revives after the end').toBe(true);
  expect(afterMid.second, 'at most the ask + greeting rounds were served').toBeLessThanOrEqual(9);
  expect((await stubState()).violations).toEqual([]);

  // ---- 收听中: the full greeting acks, listening settles, THEN end — no
  // advance ever happened, the mic is released, the uplink freezes. An
  // IN-PAGE restart adopts the classroom's resolved subject (no re-ask), so
  // this leg needs only the greeting.
  await stubScripts({
    asr: [], llm: [], image: [],
    tts: [
      { ms: 1200, match: '你好呀' }, { ms: 1200, match: '个词要写' }, { ms: 1200, match: '第 1 个词' },
    ],
  });
  await page.locator('#rt-start').click();
  const ready3I = await waitIndex(page, (e) => e.dir === 'in' && e.type === 'session_ready'
    && e.msg.category === 'english' && e.i > ended2I, 'the english classroom resumes directly');
  expect((await controls(page)).filter((e) => e.dir === 'in' && e.type === 'subject_ask' && e.i > ended2I),
    'an in-page restart never re-asks the subject').toHaveLength(0);
  const greeting = await awaitRound(page, ready3I, 3, 'greeting (resume)');
  await awaitAcked(page, greeting, 'greeting (resume)');
  await page.locator('#rt-end').click();
  const ended3I = await waitIndex(page, (e) => e.i > ended2I && e.dir === 'in' && e.type === 'session_ended'
    && e.msg.reason === 'client_end', 'session_ended(client_end) while listening');
  await page.waitForTimeout(1200);
  const log = await controls(page);
  expect(log.filter((e) => e.dir === 'in' && e.type === 'lesson_update'),
    'no confirm ever advanced a word in this spec').toHaveLength(0);
  // ping/pong is the transport keepalive (the socket stays open after
  // client_end); every OTHER downlink frame must stop at the goodbye.
  expect(log.filter((e) => e.i > ended3I && e.dir === 'in' && e.type !== 'pong'),
    'no server frame after the goodbye').toEqual([]);
  for (const t of (await micInfo(page)).tracks) expect(t.state, 'mic released by the end').toBe('ended');
  const frozen = await wsStats(page);
  await page.waitForTimeout(1200);
  expect((await wsStats(page)).outBinaryFrames, 'uplink frozen after the end')
    .toBe(frozen.outBinaryFrames);
  const afterListen = await ttsCountStable();
  expect(afterListen.stable, 'no vendor traffic revives after the end').toBe(true);
  expect(afterListen.second, 'at most the scripted rounds were served').toBeLessThanOrEqual(12);
  expect((await stubState()).violations, 'the strict stub saw only scripted traffic').toEqual([]);
  expect(await lessonsNow(), 'still exactly the one english row').toHaveLength(1);
});

test('J13+J14 last word → one check entry, masked answers, real check submit, natural end', async ({ page }) => {
  await installRtObserver(page);
  await installFakeMic(page);
  const words = ['e2e-fin-a', 'e2e-fin-b'];
  const seed = await svc.seedWords({
    category: 'english', unit: 'e2e-m1-finish', entries: words.map((word) => ({ word })),
  });
  expect(seed.ok, 'english seed').toBeTruthy();

  // EXACTLY the vendor traffic the journey needs (FIFO + per-entry match):
  // greeting 3 + word-2 1 + check bridge 2 + battle summary 2. A phantom
  // word 3 or an extra sentence cannot be served — it would exhaust the
  // script and surface as a stub violation.
  await stubScripts({
    asr: [{ content: '好了' }, { content: '好了' }],
    llm: [
      { content: '{"intent":"confirm","reply":"好"}' },
      { content: '{"intent":"confirm","reply":"好"}' },
    ],
    image: [],
    tts: [
      { ms: 1200, match: '你好呀' }, { ms: 1200, match: '个词要写' }, { ms: 1200, match: '第 1 个词' },
      { ms: 1200, match: '第 2 个词' },
      { ms: 1200, match: '全都写完啦' }, { ms: 1200, match: '我们一起检查' },
      { ms: 1200, match: '都默写完了' }, { ms: 1200, match: '全部都写对了' },
    ],
  });

  await page.goto(svc.url + 'tutor/');
  await page.locator('#rt-start').click();

  const readyI = await waitIndex(page, (e) => e.dir === 'in' && e.type === 'session_ready'
    && e.msg.category === 'english'
    && Array.isArray(e.msg.lesson.words) && e.msg.lesson.words.length === 2
    && e.msg.lesson.words.every((w) => w === ''), 'session_ready: english, answers masked');

  const greeting = await awaitRound(page, readyI, 3, 'greeting');
  await awaitAcked(page, greeting, 'greeting');

  // J13 masking BEFORE the check stage: chips stay numbers, panel closed, no
  // plaintext anywhere on the page (subtitles carry the masked copies).
  await expect(page.locator('#rt-words .rt-word-chip')).toHaveText(['1', '2']);
  await expect(page.locator('#rt-check')).toBeHidden();

  await childSaysDone(page);
  const lu1I = await waitIndex(page, (e) => e.dir === 'in' && e.type === 'lesson_update'
    && e.msg.current_index === 1, 'first 好了 advances to word 2');
  const word2 = await awaitRound(page, lu1I, 1, 'word-2 announcement');
  await awaitAcked(page, word2, 'word-2 announcement');
  await expect(page.locator('#rt-words .rt-word-chip')).toHaveText(['1', '2']);
  for (const w of words) {
    expect((await page.locator('body').innerText()).includes(w), 'still masked: ' + w).toBe(false);
  }

  // The LAST confirm: index reaches the word-COUNT boundary and check_started
  // follows it (reason dictation_done, full plaintext — the only unmask).
  await childSaysDone(page);
  let csI = -1;
  await waitWs(page, (log) => {
    const lu = log.find((e) => e.dir === 'in' && e.type === 'lesson_update' && e.msg.current_index === 2);
    const cs = log.find((e) => e.dir === 'in' && e.type === 'check_started');
    if (lu && cs && cs.i > lu.i) { csI = cs.i; return true; }
    return false;
  }, { timeoutMs: 30000, label: 'boundary lesson_update precedes check_started' });
  const started = (await controls(page)).find((e) => e.type === 'check_started');
  expect(started.msg.words).toEqual(words);
  expect(started.msg.reason).toBe('dictation_done');
  await expect(page.locator('#rt-progress-text')).toHaveText('已完成');
  const rows = page.locator('#rt-check .rt-check-row');
  await expect(rows).toHaveCount(2);
  await expect(rows.nth(0).locator('.rt-check-word')).toHaveText('1. ' + words[0]);
  await expect(page.locator('#rt-words .rt-word-chip')).toHaveText(words);

  const bridge = await awaitRound(page, csI, 2, 'check bridge');
  await awaitAcked(page, bridge, 'check bridge');

  // J13: no fabricated save state before the submit.
  expect((await controls(page)).filter((e) => e.type === 'review_result')).toHaveLength(0);
  await expect(page.locator('#rt-banner')).toBeHidden();
  expect((await observe(page)).check.resultText, 'no save note before submit').toBe('');

  // J14's tail: real verdict buttons, one submit, honest review_result, the
  // summary acked before the goodbye, the banner's real save copy.
  const endedI = await checkSubmitNaturalEnd(page, words);
  await waitWs(page, async () => (await observe(page)).playbackResourceReleased === true,
    { label: 'terminal drain released the playback resources' });
  await expect(page.locator('#rt-banner .rt-btn-home')).toBeVisible();

  // J14 resource release: mic stopped (tracks ended by production), playback
  // drained with nothing live, uplink frozen, nothing follows the goodbye.
  expect((await observe(page)).terminalDraining).toBe(true);
  expect((await playbackState(page)).activeSourceKeys).toEqual([]);
  const teardown = await page.evaluate(() => ({
    capturing: window.__rtTutor.capture.isCapturing,
    ctxState: window.__rtTutor.capture.audioCtx ? window.__rtTutor.capture.audioCtx.state : 'none',
  }));
  expect(teardown.capturing, 'production capture stopped').toBe(false);
  expect(teardown.ctxState, 'the shared AudioContext survives (D-06)').toBe('running');
  for (const t of (await micInfo(page)).tracks) expect(t.state, 'synthetic track stopped').toBe('ended');
  const frozen = await wsStats(page);
  await page.waitForTimeout(1200);
  expect((await wsStats(page)).outBinaryFrames, 'uplink frozen after the ending')
    .toBe(frozen.outBinaryFrames);
  expect((await controls(page)).filter((e) => e.i > endedI && e.dir === 'in'),
    'no server frame after session_ended').toEqual([]);

  // J13 closing invariants: ONE check entry, no advance past the boundary, no
  // word 3 anywhere, exact vendor traffic consumed.
  const final = await controls(page);
  expect(final.filter((e) => e.dir === 'in' && e.type === 'check_started'),
    'the check stage was entered exactly once').toHaveLength(1);
  expect(final.filter((e) => e.dir === 'in' && e.type === 'lesson_update'
    && e.msg.current_index > words.length), 'no advance past the word count').toHaveLength(0);
  const state = await stubState();
  const ttsTexts = state.requests.filter((r) => r.category === 'tts').map((r) => r.text);
  expect(ttsTexts.some((t) => t.includes('第 3 个词')), 'no phantom next word spoken').toBe(false);
  expect(state.remaining.tts, 'every scripted sentence served, none extra').toBe(0);
  expect(state.violations, 'the strict stub saw only scripted traffic').toEqual([]);

  // Persistence: one submitted english row with the real verdict counts.
  const lessonRows = await lessonsNow();
  expect(lessonRows).toHaveLength(1);
  expect(lessonRows[0]).toMatchObject({
    category: 'english', word_count: 2, lesson_id: 'e2e-m1-browser',
    stage: 'submitted', correct: 2, wrong: 0,
  });
});

/** Answer the subject ask by button; returns the picked session_ready index. */
async function openViaAsk(page, pickButton, expectLessonId, words) {
  await waitWs(page, (log) => log.some((e) => e.dir === 'in' && e.type === 'subject_ask'),
    { label: 'the subject ask arrives' });
  await page.locator(pickButton).click();
  return waitIndex(page, (e) => e.dir === 'in' && e.type === 'session_ready'
    && e.msg.lesson && e.msg.lesson.id === expectLessonId
    && Array.isArray(e.msg.lesson.words) && e.msg.lesson.words.length === words.length
    && e.msg.lesson.words.every((w) => w === ''),
  'the picked classroom opens: ' + expectLessonId + ', answers masked');
}

/**
 * One full lesson journey from an OPEN classroom (readyI) to the natural
 * ending: greeting → confirm ×N (each advance really acked) → the check
 * stage → submit → summary → session_ended(lesson_complete). onGreetingSettled
 * fires after the greeting round acked and listening settled — the J16 leg-1
 * seam swaps the TTS scripts there (the ask+greeting split is 0..3/3..6
 * nondeterministic under a mid-ask pick, so only matched-free tones can
 * cover it; everything after the greeting is deterministic).
 */
async function driveLessonToNaturalEnd(page, words, readyI, { onGreetingSettled } = {}) {
  const greeting = await awaitRound(page, readyI, 3, 'greeting');
  await awaitAcked(page, greeting, 'greeting');
  for (const w of words) {
    expect((await page.locator('body').innerText()).includes(w), 'masked pre-check: ' + w).toBe(false);
  }
  if (onGreetingSettled) await onGreetingSettled();
  for (let i = 1; i <= words.length; i++) {
    await childSaysDone(page);
    const luI = await waitIndex(page, (e) => e.dir === 'in' && e.type === 'lesson_update'
      && e.msg.current_index === i, 'confirm ' + i + ' advances to index ' + i);
    if (i < words.length) {
      const next = await awaitRound(page, luI, 1, 'word-' + (i + 1) + ' announcement');
      await awaitAcked(page, next, 'word-' + (i + 1) + ' announcement');
    } else {
      // The LAST confirm: boundary lesson_update then check_started.
      let csI = -1;
      await waitWs(page, (log) => {
        const cs = log.find((e) => e.dir === 'in' && e.type === 'check_started'
          && e.msg.reason === 'dictation_done' && e.i > luI);
        if (cs) { csI = cs.i; return true; }
        return false;
      }, { timeoutMs: 30000, label: 'the last confirm opens the check stage' });
      const started = (await controls(page)).find((e) => e.type === 'check_started');
      expect(started.msg.words).toEqual(words);
      const bridge = await awaitRound(page, csI, 2, 'check bridge');
      await awaitAcked(page, bridge, 'check bridge');
    }
  }
  return checkSubmitNaturalEnd(page, words);
}

test('J16 finish chinese, reload → the english classroom opens on its own words, both submit independently', async ({ page }) => {
  await installRtObserver(page);
  await installFakeMic(page);
  const zhWords = ['e2e-fin-zh-a', 'e2e-fin-zh-b'];
  const enWords = ['e2e-fin-en-a', 'e2e-fin-en-b'];
  for (const [category, unit, words] of [
    ['chinese', 'e2e-m1-j16-zh', zhWords],
    ['english', 'e2e-m1-j16-en', enWords],
  ]) {
    const seed = await svc.seedWords({ category, unit, entries: words.map((word) => ({ word })) });
    expect(seed.ok, 'seed ' + category).toBeTruthy();
  }
  // Leg 1 opens through the ASK: a pick that races the ask's synthesis makes
  // the ask/greeting TTS split 0..3/3..6, so that stretch rides matched-free
  // tones; the deterministic matched tail is installed AFTER the greeting
  // settles (stub.scripts replaces the FIFO wholesale).
  const confirmJson = { content: '{"intent":"confirm","reply":"好"}' };
  await stubScripts({
    asr: [{ content: '好了' }, { content: '好了' }, { content: '好了' }, { content: '好了' }],
    llm: [confirmJson, confirmJson, confirmJson, confirmJson],
    image: [],
    tts: [{ ms: 1200 }, { ms: 1200 }, { ms: 1200 }, { ms: 1200 }, { ms: 1200 }, { ms: 1200 }],
  });
  const installMatchedTail = () => stubScripts({
    tts: [
      { ms: 1200, match: '第 2 个词' },
      { ms: 1200, match: '全都写完啦' }, { ms: 1200, match: '我们一起检查' },
      { ms: 1200, match: '都默写完了' }, { ms: 1200, match: '全部都写对了' },
      { ms: 1200, match: '你好呀' }, { ms: 1200, match: '个词要写' }, { ms: 1200, match: '第 1 个词' },
      { ms: 1200, match: '第 2 个词' },
      { ms: 1200, match: '全都写完啦' }, { ms: 1200, match: '我们一起检查' },
      { ms: 1200, match: '都默写完了' }, { ms: 1200, match: '全部都写对了' },
    ],
  });

  // Leg 1: pick chinese, finish the whole lesson naturally.
  await page.goto(svc.url + 'tutor/');
  await page.locator('#rt-start').click();
  const zhReadyI = await openViaAsk(page, '#rt-subject-zh', 'e2e-m1-browser-chinese', zhWords);
  await driveLessonToNaturalEnd(page, zhWords, zhReadyI, { onGreetingSettled: installMatchedTail });
  await waitWs(page, async () => (await observe(page)).playbackResourceReleased === true,
    { label: 'chinese lesson drained and released' });
  const zhRows = await lessonsNow();
  expect(zhRows).toHaveLength(1);
  expect(zhRows[0]).toMatchObject({ category: 'chinese', word_count: 2, lesson_id: 'e2e-m1-browser-chinese', stage: 'submitted', correct: 2 });
  const zhTtsCount = (await stubState()).requests.filter((r) => r.category === 'tts').length;

  // Leg 2: a fresh page (auto entry) — chinese is no longer due, so the
  // ENGLISH classroom must open DIRECTLY: no re-ask, its own row, its own
  // words, index from 1 — nothing of the finished lesson carries over.
  await page.reload();
  await page.locator('#rt-start').click();
  const enReadyI = await waitIndex(page, (e) => e.dir === 'in' && e.type === 'session_ready'
    && e.msg.category === 'english' && e.msg.lesson.id === 'e2e-m1-browser',
  'the english classroom opens directly after the reload');
  expect((await controls(page)).filter((e) => e.dir === 'in' && e.type === 'subject_ask'),
    'no subject ask on the restart').toHaveLength(0);
  await expect(page.locator('#rt-progress-text')).toHaveText('第 1 / 2 个词');
  await driveLessonToNaturalEnd(page, enWords, enReadyI);

  // Subject isolation, vendor-side: nothing spoken after the reload names a
  // chinese word; the english greeting names its own first word.
  const state = await stubState();
  const postReloadTts = state.requests.filter((r) => r.category === 'tts').slice(zhTtsCount).map((r) => r.text);
  expect(postReloadTts.some((t) => zhWords.some((w) => t.includes(w))),
    'no chinese word leaks into the english session').toBe(false);
  expect(postReloadTts.some((t) => t.includes(enWords[0])),
    'the english greeting speaks its own first word').toBe(true);
  expect(state.remaining.tts).toBe(0);
  expect(state.violations, 'the strict stub saw only scripted traffic').toEqual([]);

  // Persistence: TWO submitted rows — each subject its own, both real.
  const rows = await lessonsNow();
  expect(rows).toHaveLength(2);
  expect(rows.find((r) => r.lesson_id === 'e2e-m1-browser-chinese'))
    .toMatchObject({ category: 'chinese', word_count: 2, stage: 'submitted', correct: 2 });
  expect(rows.find((r) => r.lesson_id === 'e2e-m1-browser'))
    .toMatchObject({ category: 'english', word_count: 2, stage: 'submitted', correct: 2 });
});

test('J16 empty vocabulary ends through the real no-due result', async ({ page }) => {
  await installRtObserver(page);
  // No seeds at all: the real RunDue decides, the teacher explains, the
  // session ends — never a fabricated empty classroom.
  await stubScripts({ asr: [], llm: [], image: [], tts: [{ ms: 1200, match: '没有要到期的单词' }] });
  await page.goto(svc.url + 'tutor/');
  await page.locator('#rt-start').click();

  const readyI = await waitIndex(page, (e) => e.dir === 'in' && e.type === 'session_ready'
    && e.msg.category === 'english'
    && Array.isArray(e.msg.lesson.words) && e.msg.lesson.words.length === 0,
  'session_ready: the honest empty word table');
  const explain = await awaitRound(page, readyI, 1, 'the no-due explanation');
  await awaitAcked(page, explain, 'the no-due explanation', { settle: false });
  const lastAckI = Math.max(...(await controls(page))
    .filter((e) => e.dir === 'out' && e.type === 'sentence_ack'
      && explain.sids.includes(e.msg.sentence_id))
    .map((e) => e.i));
  const endedI = await waitIndex(page, (e) => e.dir === 'in' && e.type === 'session_ended'
    && e.msg.reason === 'no_due_words', 'session_ended(no_due_words)');
  expect(endedI).toBeGreaterThan(lastAckI);

  await expect(page.locator('#rt-banner')).toContainText('词库暂无到期词');
  await expect(page.locator('#rt-banner .rt-practice-btn'), 'the page can start again').toBeVisible();
  expect(await lessonsNow(), 'no empty classroom row was created').toHaveLength(0);
  const state = await stubState();
  expect(state.remaining.tts).toBe(0);
  expect(state.violations).toEqual([]);
});
