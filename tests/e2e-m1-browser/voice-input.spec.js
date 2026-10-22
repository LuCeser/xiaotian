// voice-input.spec.js — the controllable-MediaStream tool's acceptance
// (issue #58, e2e-test-plan §7 step 7). The synthetic mic replaces ONLY the
// getUserMedia source (plan §2 B-layer); everything else is production:
// the page's RTCapture graph, resampleTo16k, the s16le uplink, the server's
// VAD/WAV-wrap, and the player. Proves, in one real classroom:
//
//   1. format    — session_init declares 16000/s16le/1ch, and the strict
//                  stub's ASR validation sees exactly that container, so the
//                  clip really rode the PRODUCTION capture conversion;
//   2. silence   — sustained synthetic silence segments nothing (no ASR,
//                  no fabricated confirmation) across the listening window;
//   3. isolation — teacher playback never leaks into the input: zero child
//                  interrupts fire while it plays (the page's own RMS
//                  detector watches capture frames), and production keeps
//                  the input muted (gain 0) so input never hits speakers;
//   4. release   — after 结束 the production capture stops (isCapturing
//                  false, every synthetic track stopped by production, the
//                  AudioContext survives per D-06) and the uplink freezes.
'use strict';

const { test, expect } = require('@playwright/test');
const { installRtObserver, waitWs, controls, playbackTrace, wsStats } = require('./helpers/rt-observe');
const { installFakeMic, micInfo, playPcm16 } = require('./helpers/fake-mic');
const { startSpecService, makeStubControl } = require('./helpers/spec-service');

const stub = makeStubControl();
let svc;

test.beforeEach(async () => { svc = await startSpecService(); });
test.afterEach(async () => {
  try { if (svc) await svc.stop(); } finally {
    svc = null;
    await stub.reset();
  }
});

// One deterministic corpus clip: 2 s of 440 Hz s16le @16 kHz — loud enough
// for the server VAD, long enough for one segment (+700 ms hangover).
function tonePcm16Base64(ms = 2000, freq = 440, rate = 16000, amp = 0.6) {
  const n = Math.round((ms * rate) / 1000);
  const buf = Buffer.alloc(n * 2);
  for (let i = 0; i < n; i++) {
    buf.writeInt16LE(Math.round(Math.sin((2 * Math.PI * freq * i) / rate) * amp * 32767), i * 2);
  }
  return buf.toString('base64');
}

test('synthetic mic rides the production capture path: format, silence, isolation, release', async ({ page }) => {
  await installRtObserver(page);
  await installFakeMic(page);

  // Single-subject vocabulary → the auto entry opens english directly (no
  // subject ask): the classroom under test is a plain dictation opening.
  const seed = await svc.seedWords({
    category: 'english', unit: 'e2e-m1-mic', entries: [{ word: 'e2e-m1-mic-a' }, { word: 'e2e-m1-mic-b' }],
  });
  expect(seed.ok, 'english seed').toBeTruthy();

  // Greeting = 3 sentences. ONE asr answer: empty transcription — silence
  // that DID segment must settle cleanly with no classification LLM call.
  const install = await stub.scripts({
    asr: [{ empty: true }], llm: [], image: [], tts: [{ ms: 1200 }, { ms: 1200 }, { ms: 1200 }],
  });
  expect(install.ok, 'stub scripts install').toBeTruthy();
  const stubState = () => stub.state();

  await page.goto(svc.url + 'tutor/');
  await page.locator('#rt-start').click();

  // Format (client half): the production client declares the uplink spec.
  await waitWs(page, (log) => log.some((e) => e.dir === 'out' && e.type === 'session_init'
    && e.msg.audio && e.msg.audio.sampleRate === 16000 && e.msg.audio.format === 's16le'
    && e.msg.audio.channels === 1 && e.msg.category === 'auto'),
  { label: 'session_init declares 16000/s16le/1ch' });

  await waitWs(page, (log) => log.some((e) => e.dir === 'in' && e.type === 'session_ready'
    && e.msg.category === 'english'
    && Array.isArray(e.msg.lesson.words) && e.msg.lesson.words.length === 2
    && e.msg.lesson.words.every((w) => w === '')),
  { label: 'session_ready for the english classroom' });

  // The greeting acks NATURALLY (production onended → sentence_ack), then
  // the server settles listening — the same acceptance shape as J05.
  await waitWs(page, (log) => {
    const starts = log.filter((e) => e.dir === 'in' && e.type === 'audio_start');
    const ends = log.filter((e) => e.dir === 'in' && e.type === 'audio_end');
    const acks = log.filter((e) => e.dir === 'out' && e.type === 'sentence_ack');
    return starts.length > 0 && ends.length === starts.length && acks.length === starts.length;
  }, { timeoutMs: 30000, label: 'greeting fully acked by the production player' });
  await waitWs(page, (log) => {
    const ends = log.filter((e) => e.dir === 'in' && e.type === 'audio_end');
    const lastEnd = ends.length ? ends[ends.length - 1].i : -1;
    return log.some((e) => e.i > lastEnd && e.dir === 'in' && e.type === 'state' && e.msg.value === 'listening');
  }, { label: 'listening settled after the greeting acks' });

  // Isolation, playback→input half: while the teacher spoke, the page's own
  // RMS detector (fed by PRODUCTION capture frames) must never have fired.
  await waitWs(page, (log) => log.some((e) => e.dir === 'in' && e.type === 'state' && e.msg.value === 'listening'));
  const controlsNow = await controls(page);
  expect(controlsNow.filter((e) => e.dir === 'out' && e.type === 'interrupt'),
    'teacher audio never leaked into the capture frames').toHaveLength(0);
  // Isolation, input→playback half: production mutes its capture graph.
  const inputGain = await page.evaluate(() => (window.__rtTutor.capture._mute && window.__rtTutor.capture._mute.gain.value));
  expect(inputGain, 'the synthetic mic is never played to the child').toBe(0);

  // Sustained silence: hold the listening state ~2.5 s (also covering the
  // post-ack echo tail) — no segment, no ASR, no fabricated turn.
  await page.waitForTimeout(2500);
  let state = await stubState();
  expect(state.requests.filter((r) => r.category === 'asr'), 'silence segments nothing').toHaveLength(0);
  expect(state.violations).toEqual([]);

  // The clip: play it, and the production path must deliver EXACTLY the
  // declared container to the vendor — the strict stub validates fmt/data
  // invariants itself, so a lying format fails there, not here.
  const clipMs = 2000;
  await playPcm16(page, tonePcm16Base64(clipMs), 16000);

  const wavOk = (r) => r.category === 'asr' && r.sampleRateHz === 16000 && r.channels === 1
    && r.bitsPerSample === 16 && r.durationMs >= clipMs && r.durationMs <= clipMs + 2500;
  await waitWs(page, () => stubState().then((s) => s.requests.some(wavOk)), { timeoutMs: 15000, label: 'asr saw valid 16k/1ch/s16 WAV' });
  // The turn was real server-side (processing → the empty answer settles
  // listening again), and it never needed the classification LLM.
  await waitWs(page, (log) => {
    const proc = log.filter((e) => e.dir === 'in' && e.type === 'state' && e.msg.value === 'processing');
    const lastProc = proc.length ? proc[proc.length - 1].i : -1;
    return log.some((e) => e.i > lastProc && e.dir === 'in' && e.type === 'state' && e.msg.value === 'listening');
  }, { label: 'the spoken turn settled back to listening' });
  state = await stubState();
  expect(state.requests.filter((r) => r.category === 'asr')).toHaveLength(1);
  expect(state.requests.filter((r) => r.category === 'llm'), 'empty transcription never classifies').toHaveLength(0);

  // Release: explicit end → session_ended, production stops the capture.
  await page.locator('#rt-end').click();
  await waitWs(page, (log) => log.some((e) => e.dir === 'in' && e.type === 'session_ended'),
    { label: 'session_ended after explicit end' });
  const teardown = await page.evaluate(() => ({
    capturing: window.__rtTutor.capture.isCapturing,
    ctxState: window.__rtTutor.capture.audioCtx ? window.__rtTutor.capture.audioCtx.state : 'none',
  }));
  expect(teardown.capturing, 'production capture stopped').toBe(false);
  expect(teardown.ctxState, 'the shared AudioContext survives (D-06)').not.toBe('closed');
  const info = await micInfo(page);
  expect(info.tracks.length).toBeGreaterThan(0);
  for (const t of info.tracks) expect(t.state, 'production stopped every synthetic track').toBe('ended');
  expect(info.pcmPlays).toBe(1);

  // The uplink freezes: no further binary frames after the teardown.
  const first = await wsStats(page);
  await page.waitForTimeout(1200);
  const second = await wsStats(page);
  expect(second.outBinaryFrames, 'no audio leaves the device after stop (privacy boundary)')
    .toBe(first.outBinaryFrames);

  state = await stubState();
  expect(state.violations, 'the strict stub saw only scripted traffic').toEqual([]);
});
