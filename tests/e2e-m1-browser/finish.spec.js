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
//                     resources released, uplink frozen; plus the same tail
//                     under a REAL partial save (the parent deletes a word
//                     mid-check) — the honest 1/2 copy everywhere, never a
//                     dressed-up success;
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
 * J14's WS-release wait (review PR #72 P1): after the session's terminal
 * message the page itself must close the WebSocket. The server's read loop
 * only exits on OUR disconnect, so a socket left open keeps the connection,
 * its server-side session manager and heartbeat alive until the page
 * unloads — and an in-page restart orphans it outright (connect replaces
 * client.ws). Waits for the socket's real close EVENT after fromI, then
 * proves the production client's socket object reached CLOSED: silence on
 * the wire proves nothing (the server simply keeps waiting).
 */
async function awaitSocketClosed(page, fromI, label) {
  await waitWs(page, (log) => log.some((e) => e.i > fromI && e.kind === 'close'),
    { timeoutMs: 30000, label: label + ': the page closed the WebSocket' });
  expect(await page.evaluate(() => window.__rtTutor.client.ws.readyState),
    label + ': the production socket object is CLOSED (3)').toBe(3);
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
  await awaitSocketClosed(page, ended2I, 'the explicit mid-greeting end');
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
  // ping/pong is the transport keepalive and may race the goodbye; every
  // OTHER downlink frame must stop at it. The page then closes the socket
  // itself — J14's release contract reaches the explicit end too.
  expect(log.filter((e) => e.i > ended3I && e.dir === 'in' && e.type !== 'pong'),
    'no server frame after the goodbye').toEqual([]);
  await awaitSocketClosed(page, ended3I, 'the explicit listening end');
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
  // J14 (review P1): silence after the goodbye is NOT release evidence —
  // the server read loop simply keeps waiting. The transport itself must
  // close: the real close event, then the production socket at CLOSED.
  await awaitSocketClosed(page, endedI, 'the natural ending');

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

// J14's save-failure leg (review PR #72 P2): the only product-real way a
// BROWSER journey can stage a save failure — SubmitReview goes Python/SQLite
// and the stub layer cannot inject one — is the parent surface itself: while
// the check panel waits for the verdicts, DELETE /words really removes one
// of the two cards, so the submit's `review` batch finds it gone and skips
// it (updated 1 of 2 requested, missing 1). The page must then narrate the
// PARTIAL save honestly in every channel — resultText, banner, and the
// teacher's spoken summary — and the ending must still complete. (The hard
// error path — the python subprocess itself failing — has no browser-layer
// trigger; that boundary belongs to the Go tests.)
test('J14 partial save: the parent deletes a word mid-check → the honest 1/2 copy, the ending still completes', async ({ page }) => {
  await installRtObserver(page);
  await installFakeMic(page);
  const words = ['e2e-fin-ps-a', 'e2e-fin-ps-b'];
  const seed = await svc.seedWords({
    category: 'english', unit: 'e2e-m1-fin-ps', entries: words.map((word) => ({ word })),
  });
  expect(seed.ok, 'english seed').toBeTruthy();

  // Same deterministic corpus as the J13+J14 journey, EXCEPT the battle
  // summary gains a THIRD sentence: 「全部都写对了，你真棒！」 ends at ！, so
  // the partial-save suffix 「本次保存 1/2 个。」 is its own speakable
  // sentence (splitSentences) — the tts FIFO must script it too.
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
      { ms: 1200, match: '本次保存 1/2' },
    ],
  });

  // The lesson reaches its check stage through the REAL journey: greeting
  // fully acked, two 好了 confirms, the check bridge acked and settled.
  await page.goto(svc.url + 'tutor/');
  await page.locator('#rt-start').click();
  const readyI = await waitIndex(page, (e) => e.dir === 'in' && e.type === 'session_ready'
    && e.msg.category === 'english'
    && Array.isArray(e.msg.lesson.words) && e.msg.lesson.words.length === 2
    && e.msg.lesson.words.every((w) => w === ''), 'session_ready: english, answers masked');
  const greeting = await awaitRound(page, readyI, 3, 'greeting');
  await awaitAcked(page, greeting, 'greeting');
  await childSaysDone(page);
  const lu1I = await waitIndex(page, (e) => e.dir === 'in' && e.type === 'lesson_update'
    && e.msg.current_index === 1, 'first 好了 advances to word 2');
  const word2 = await awaitRound(page, lu1I, 1, 'word-2 announcement');
  await awaitAcked(page, word2, 'word-2 announcement');
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
  const bridge = await awaitRound(page, csI, 2, 'check bridge');
  await awaitAcked(page, bridge, 'check bridge');

  // THE REAL SAVE FAILURE: the parent deletes word 2 through the real parent
  // REST surface. The classroom keeps its in-session snapshot and drives on;
  // only the submit's FSRS write will find the card gone.
  const del = await svc.deleteWords({ category: 'english', words: [words[1]] });
  expect(del.ok, 'parent delete REST call').toBeTruthy();
  expect(del.body && del.body.deleted, 'one card really deleted mid-check').toBe(1);

  // J14's tail under the partial save: real verdicts, one submit, the
  // HONEST review_result sets — 2 requested / 1 updated / 1 missing, never
  // a fabricated full success (S-05, prohibition P3).
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
    && e.msg.requested === 2 && e.msg.updated === 1 && e.msg.missing === 1
    && e.msg.unchecked === false,
  'honest review_result: 2 requested, only 1 really saved');
  expect(resI).toBeGreaterThan(submitI);

  // The ending still completes — the summary (now THREE sentences, the last
  // one the partial-save note) fully acked by the production player before
  // session_ended(lesson_complete).
  const summary = await awaitRound(page, resI, 3, 'battle summary with the honest 1/2 sentence');
  await awaitAcked(page, summary, 'battle summary', { settle: false });
  const lastAckI = Math.max(...(await controls(page))
    .filter((e) => e.dir === 'out' && e.type === 'sentence_ack'
      && summary.sids.includes(e.msg.sentence_id))
    .map((e) => e.i));
  const endedI = await waitIndex(page, (e) => e.dir === 'in' && e.type === 'session_ended'
    && e.msg.reason === 'lesson_complete', 'session_ended(lesson_complete) despite the partial save');
  expect(endedI, 'the goodbye still follows the summary\'s last real ack').toBeGreaterThan(lastAckI);
  await awaitSocketClosed(page, endedI, 'the partial-save ending');

  // Honest copy in EVERY channel: the panel's resultText, the banner, and
  // the teacher really SPOKE the partial save (the third summary sentence
  // reached the vendor — not just the screen).
  expect((await observe(page)).check.resultText, 'the panel\'s honest partial-save copy')
    .toBe('保存了 1 / 2 个，别担心，下次会补上。');
  await expect(page.locator('#rt-banner')).toContainText('今天的 2 个词都默写完了');
  await expect(page.locator('#rt-banner'))
    .toContainText('保存了 1 / 2 个，别担心，下次会补上。');
  await expect(page.locator('#rt-banner .rt-practice-btn'), 'the ending still offers 再练一组')
    .toBeVisible();
  const state = await stubState();
  const spoken = state.requests.filter((r) => r.category === 'tts').map((r) => r.text);
  expect(spoken.some((t) => t.includes('本次保存 1/2 个')),
    'the teacher really SPOKE the partial save').toBe(true);
  expect(state.remaining.tts, 'every scripted sentence served, none extra').toBe(0);
  expect(state.violations, 'the strict stub saw only scripted traffic').toEqual([]);

  // The classroom row still lands — the VERDICTS were real (2 对); only the
  // FSRS refresh went 1/2, and that is review_result's story, not the row's.
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
  const zhEndedI = await driveLessonToNaturalEnd(page, zhWords, zhReadyI, { onGreetingSettled: installMatchedTail });
  await waitWs(page, async () => (await observe(page)).playbackResourceReleased === true,
    { label: 'chinese lesson drained and released' });
  await awaitSocketClosed(page, zhEndedI, 'the chinese lesson');
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
  const enEndedI = await driveLessonToNaturalEnd(page, enWords, enReadyI);
  await awaitSocketClosed(page, enEndedI, 'the english lesson (fresh page)');

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

// ---------------------------------------------------------------------------
// J16's in-page reopen (review PR #72 P2): the reload leg above proves the
// OTHER subject opens on a fresh page; the leg below proves the COMPLETED
// page can re-initiate ITSELF — a real 「再练一组」 tap on the SAME document,
// player and flags still warm from leg 1.
//
// The practice connection restarts the server's id namespace from scratch
// (fresh session → epoch 0, turn 1 — the SAME t1-s* ledger keys as leg 1),
// so every first-match helper would shadow leg-2 evidence with leg-1
// entries. The *Since helpers below bound the ws log (index > fromI) and
// the player trace (slice at the tap) instead — leg-2 evidence must live
// entirely after the tap.
async function waitIndexSince(page, fromI, pred, label) {
  let at = -1;
  await waitWs(page, (log) => {
    const hit = log.find((e) => e.i > fromI && pred(e));
    if (hit) at = hit.i;
    return at >= 0;
  }, { timeoutMs: 30000, label });
  return at;
}

async function ackedSince(page, round, fromI, traceMark, label, { settle = true } = {}) {
  await waitWs(page, (log) => {
    const acks = log.filter((e) => e.dir === 'out' && e.type === 'sentence_ack' && e.i > fromI
      && e.msg.epoch === round.epoch && round.sids.includes(e.msg.sentence_id));
    return acks.length === round.sids.length;
  }, { timeoutMs: 30000, label: label + ' acked by the production player (this round)' });
  const since = (await playbackState(page)).trace.slice(traceMark);
  for (const sid of round.sids) {
    expect(playedBeforeAck(since, round.epoch, sid), label + ': real onended before ack ' + sid).toBe(true);
  }
  if (settle) {
    await waitWs(page, (log) => listeningAfterPlayback(log.filter((e) => e.i > fromI), round.epoch, round.sids),
      { label: label + ': listening only after the last ack (this round)' });
  }
}

// The practice tail: the same manual 对/错 gate, but the wrap line is ONE
// sentence, review_result reports the practice-record counts, and the ending
// is practice_complete with NO 再练一组 (one tap = one set, bounded loop).
async function practiceSubmitNaturalEnd(page, words, fromI, traceMark) {
  const rows = page.locator('#rt-check .rt-check-row');
  for (let i = 0; i < words.length; i++) {
    await rows.nth(i).locator('.rt-check-btn', { hasText: /^对$/ }).click();
    await waitWs(page, (log) => log.some((e) => e.i > fromI && e.dir === 'out' && e.type === 'check_verdict'
      && e.msg.word_index === i)
      && log.some((e) => e.i > fromI && e.dir === 'in' && e.type === 'check_verdicts'
        && (e.msg.verdicts || []).some((v) => v.word_index === i && v.manual && v.correct)),
    { label: 'practice verdict ' + (i + 1) + ' tapped and echoed' });
  }
  await expect(page.locator('#rt-check .rt-check-submit')).toBeEnabled();
  await page.locator('#rt-check .rt-check-submit').click();

  const submitI = await waitIndexSince(page, fromI, (e) => e.dir === 'out' && e.type === 'check_submit',
    'practice check_submit sent');
  const resI = await waitIndexSince(page, fromI, (e) => e.dir === 'in' && e.type === 'review_result'
    && e.msg.requested === words.length && e.msg.updated === words.length && e.msg.unchecked === false,
  'honest practice review_result: ' + words.length + ' recorded');
  expect(resI).toBeGreaterThan(submitI);

  const summary = await awaitRound(page, resI, 1, 'practice wrap line');
  await ackedSince(page, summary, fromI, traceMark, 'practice wrap line', { settle: false });
  const lastAckI = Math.max(...(await controls(page))
    .filter((e) => e.i > fromI && e.dir === 'out' && e.type === 'sentence_ack'
      && summary.sids.includes(e.msg.sentence_id))
    .map((e) => e.i));
  const endedI = await waitIndexSince(page, fromI, (e) => e.dir === 'in' && e.type === 'session_ended'
    && e.msg.reason === 'practice_complete', 'session_ended(practice_complete)');
  expect(endedI, 'the wrap ending follows the wrap line\'s last real ack').toBeGreaterThan(lastAckI);

  await expect(page.locator('#rt-banner')).toContainText('这组练习完成啦');
  expect((await observe(page)).check.resultText, 'the practice records saved — honest copy')
    .toBe('学习记录保存好了，' + words.length + ' 个词都记上啦。');
  await expect(page.locator('#rt-banner .rt-practice-btn'), 'no second practice button (bounded loop)')
    .toHaveCount(0);
  return endedI;
}

test('J16 finish → real 再练一组: in-page second round, fresh progress/audio/acks, released again', async ({ page }) => {
  await installRtObserver(page);
  await installFakeMic(page);
  const zhWords = ['e2e-fin-rz-a', 'e2e-fin-rz-b'];
  const zhWords2 = ['e2e-fin-rz-c', 'e2e-fin-rz-d'];
  const seed = await svc.seedWords({
    category: 'chinese', unit: 'e2e-m1-j16r', entries: zhWords.map((word) => ({ word })),
  });
  expect(seed.ok, 'chinese seed').toBeTruthy();

  // One FIFO for BOTH rounds (only chinese is due → leg 1 opens DIRECTLY, no
  // ask race — every sentence is deterministic, so matched entries pin the
  // whole journey): leg 1 = greeting 3 + word-2 1 + bridge 2 + summary 2;
  // practice = greeting 3 + word-2 1 + bridge 2 + wrap 1.
  const confirmJson = { content: '{"intent":"confirm","reply":"好"}' };
  await stubScripts({
    asr: [{ content: '好了' }, { content: '好了' }, { content: '好了' }, { content: '好了' }],
    llm: [confirmJson, confirmJson, confirmJson, confirmJson],
    image: [],
    tts: [
      { ms: 1200, match: '你好呀' }, { ms: 1200, match: '个词要写' }, { ms: 1200, match: '第 1 个词' },
      { ms: 1200, match: '第 2 个词' },
      { ms: 1200, match: '全都写完啦' }, { ms: 1200, match: '我们一起检查' },
      { ms: 1200, match: '都默写完了' }, { ms: 1200, match: '全部都写对了' },
      { ms: 1200, match: '你好呀' }, { ms: 1200, match: '个词要写' }, { ms: 1200, match: '第 1 个词' },
      { ms: 1200, match: '第 2 个词' },
      { ms: 1200, match: '全都写完啦' }, { ms: 1200, match: '我们一起检查' },
      { ms: 1200, match: '这组练习' },
    ],
  });

  // Leg 1: finish the chinese daily lesson naturally.
  await page.goto(svc.url + 'tutor/');
  await page.locator('#rt-start').click();
  const zhReadyI = await waitIndex(page, (e) => e.dir === 'in' && e.type === 'session_ready'
    && e.msg.category === 'chinese' && e.msg.lesson.id === 'e2e-m1-browser-chinese'
    && Array.isArray(e.msg.lesson.words) && e.msg.lesson.words.length === 2
    && e.msg.lesson.words.every((w) => w === ''), 'chinese opens directly (one subject due)');
  const zhEndedI = await driveLessonToNaturalEnd(page, zhWords, zhReadyI);
  await waitWs(page, async () => (await observe(page)).playbackResourceReleased === true,
    { label: 'chinese lesson drained and released' });
  await awaitSocketClosed(page, zhEndedI, 'the daily lesson (leg 1)');
  const zhRows = await lessonsNow();
  expect(zhRows).toHaveLength(1);
  expect(zhRows[0]).toMatchObject({ category: 'chinese', word_count: 2, stage: 'submitted', correct: 2 });

  // The in-page reopen. A same-document marker proves no navigation sneaks in;
  // the tap bounds (ws log + player trace) scope every leg-2 assertion.
  await page.evaluate(() => { window.__j16SameDoc = true; });
  const beforeTap = await controls(page);
  const tapI = beforeTap.length ? beforeTap[beforeTap.length - 1].i : 0;
  const traceMark = (await playbackState(page)).trace.length;
  // The parent adds two fresh chinese words (real REST) — today's dictated
  // words are excluded from a practice pick, so these are the practice set.
  const seed2 = await svc.seedWords({
    category: 'chinese', unit: 'e2e-m1-j16r-2', entries: zhWords2.map((word) => ({ word })),
  });
  expect(seed2.ok, 'practice-set seed').toBeTruthy();

  await page.locator('#rt-banner .rt-practice-btn').click();
  expect((await observe(page)).playbackResourceReleased, 'the restart re-armed the release latch').toBe(false);
  // Review P1, the leak's sharpest shape: the in-page reopen must not
  // orphan leg 1's connection. The finished lesson's socket closed BEFORE
  // the practice session_init went out — the page never leaves a live ws
  // behind for connect() to overwrite.
  const initI = await waitIndexSince(page, tapI, (e) => e.dir === 'out' && e.type === 'session_init',
    'the practice connection sent its own session_init');
  expect((await controls(page)).find((e) => e.kind === 'close' && e.i > zhEndedI && e.i < initI),
    'the finished lesson\'s socket closed before the practice connection opened').toBeTruthy();

  // The practice classroom: chinese (the resolved subject rides the re-init),
  // its OWN r0x- row, its own words, index back from 1 — nothing of the
  // finished lesson carried over.
  const pReadyI = await waitIndexSince(page, tapI, (e) => e.dir === 'in' && e.type === 'session_ready'
    && e.msg.category === 'chinese' && /^r0x-/.test(e.msg.lesson.id || '')
    && Array.isArray(e.msg.lesson.words) && e.msg.lesson.words.length === 2
    && e.msg.lesson.words.every((w) => w === ''), 'the practice classroom opens in-page (r0x-, masked)');
  expect((await controls(page)).filter((e) => e.dir === 'in' && e.type === 'subject_ask'),
    'no subject ask anywhere in the journey').toHaveLength(0);
  await expect(page.locator('#rt-progress-text')).toHaveText('第 1 / 2 个词');

  const greeting = await awaitRound(page, pReadyI, 3, 'practice greeting');
  await ackedSince(page, greeting, tapI, traceMark, 'practice greeting');

  // Two confirms on the practice set; the pick order is random, so the check
  // stage's own words list is the authority for the verdict taps.
  let pWords = null;
  for (let i = 1; i <= 2; i++) {
    await childSaysDone(page);
    const luI = await waitIndexSince(page, tapI, (e) => e.dir === 'in' && e.type === 'lesson_update'
      && e.msg.current_index === i, 'practice confirm ' + i + ' advances to index ' + i);
    if (i < 2) {
      const next = await awaitRound(page, luI, 1, 'practice word-2 announcement');
      await ackedSince(page, next, tapI, traceMark, 'practice word-2 announcement');
    } else {
      const csI = await waitIndexSince(page, tapI, (e) => e.dir === 'in' && e.type === 'check_started'
        && e.msg.reason === 'dictation_done' && e.i > luI, 'the last practice confirm opens the check stage');
      const started = (await controls(page)).find((e) => e.type === 'check_started' && e.i > tapI);
      pWords = started.msg.words;
      expect([...pWords].sort()).toEqual([...zhWords2].sort());
      const bridge = await awaitRound(page, csI, 2, 'practice check bridge');
      await ackedSince(page, bridge, tapI, traceMark, 'practice check bridge');
    }
  }
  const endedI = await practiceSubmitNaturalEnd(page, pWords, tapI, traceMark);

  // Released AGAIN on the same page, and still the same document.
  await waitWs(page, async () => (await observe(page)).playbackResourceReleased === true,
    { label: 'the practice round drained and released again' });
  expect(await page.evaluate(() => window.__j16SameDoc === true), 'no reload — the same document').toBe(true);
  expect((await observe(page)).terminalDraining).toBe(true);
  expect((await playbackState(page)).activeSourceKeys).toEqual([]);
  for (const t of (await micInfo(page)).tracks) expect(t.state, 'mic released again').toBe('ended');
  const frozen = await wsStats(page);
  await page.waitForTimeout(1200);
  expect((await wsStats(page)).outBinaryFrames, 'uplink frozen after the practice ending')
    .toBe(frozen.outBinaryFrames);
  const final = await controls(page);
  expect(final.filter((e) => e.dir === 'in' && e.type === 'error'),
    'the in-page restart never tripped an error (fresh epoch adopted)').toEqual([]);
  expect(final.filter((e) => e.i > endedI && e.dir === 'in'),
    'no server frame after session_ended').toEqual([]);
  // J14 release, whole-document shape: the practice round's socket closed
  // too, and open/close parity holds on the SAME document — every
  // connection this page opened was closed by the page itself, none left
  // for unload to reap (review P1: 每轮遗留真实连接).
  await awaitSocketClosed(page, endedI, 'the practice round');
  const evts = await controls(page);
  const opens = evts.filter((e) => e.kind === 'open').length;
  const closes = evts.filter((e) => e.kind === 'close').length;
  expect(opens, 'this document opened exactly two connections').toBe(2);
  expect(closes, 'every connection the page opened was closed by the page').toBe(opens);
  // Old acks never reused: every post-tap ack belongs to the practice round
  // (greeting 3 + word-2 1 + bridge 2 + wrap 1) — none rides leg 1's keys.
  expect(final.filter((e) => e.i > tapI && e.dir === 'out' && e.type === 'sentence_ack'),
    'every post-tap ack belongs to the practice round').toHaveLength(7);

  // Independent progress: the daily row stands untouched, the practice row is
  // its own r0x- row with its own words.
  const rows = await lessonsNow();
  expect(rows).toHaveLength(2);
  expect(rows.find((r) => r.lesson_id === 'e2e-m1-browser-chinese'))
    .toMatchObject({ category: 'chinese', word_count: 2, stage: 'submitted', correct: 2 });
  const practiceRow = rows.find((r) => /^r0x-/.test(r.lesson_id));
  expect(practiceRow, 'the practice round landed its own row').toBeTruthy();
  expect(practiceRow).toMatchObject({ category: 'chinese', word_count: 2, stage: 'submitted', correct: 2 });

  const state = await stubState();
  expect(state.remaining.tts, 'every scripted sentence served, none extra').toBe(0);
  expect(state.violations, 'the strict stub saw only scripted traffic').toEqual([]);
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
  await awaitSocketClosed(page, endedI, 'the no-due ending');
  expect(await lessonsNow(), 'no empty classroom row was created').toHaveLength(0);
  const state = await stubState();
  expect(state.remaining.tts).toBe(0);
  expect(state.violations).toEqual([]);
});
