// tests/test-rt-playback.js — pure-logic contracts for the /tutor/ capture
// and playback trio (Phase 13, Plan 05, Task 3):
//   1. resampleTo16k — 48 kHz sine 1 s → exactly 16000 output samples with
//      <2% amplitude error; remainder-tail block semantics at 16 kHz.
//   2. floatToInt16 — clamping (±1.5 → ±full scale).
//   3. RMS interruption window — silent frames never trigger, sustained
//      above-threshold frames trigger, intermittent frames reset.
//   4. playedUntilMs — table-driven estimates.
//   5. Generation token — onended after stopNow is a no-op (no stale
//      sentence_ack, no revival); late chunks after stopNow are dropped;
//      the queue cap (T-13-06c) bounds scheduled-ahead sources.
// Runs on stock Node with no framework and no installs:
// `node tests/test-rt-playback.js`.
'use strict';

const assert = require('node:assert');

const {
  resampleTo16k,
  floatToInt16,
  EMIT_BLOCK_SAMPLES
} = require('../pkg/web/static/tutor/js/rt-capture.js');

const {
  RTCPlayback,
  playedUntilMs,
  applyTuning,
  TTS_RATE,
  MAX_QUEUED_CHUNKS
} = require('../pkg/web/static/tutor/js/rt-playback.js');

// ---------------------------------------------------------------------------
// Fake audio plumbing (same style as tests/test-timing.js FakeAudioContext)
// ---------------------------------------------------------------------------

function FakeAudioContext() {
  this.currentTime = 100;
  this.destination = {};
  this.state = 'running';
  this.sources = [];
}
FakeAudioContext.prototype.createBuffer = function (channels, numSamples, rate) {
  return {
    duration: numSamples / rate,
    getChannelData: function () { return new Float32Array(numSamples); }
  };
};
FakeAudioContext.prototype.createBufferSource = function () {
  const ctx = this;
  const src = {
    buffer: null,
    onended: null,
    onerror: null,
    startTimes: [],
    stopped: false,
    connect: function () {},
    start: function (t) { this.startTimes.push(t); },
    stop: function () { this.stopped = true; }
  };
  ctx.sources.push(src);
  return src;
};

function chunkBuffer(numSamples) {
  return new Int16Array(numSamples).buffer; // s16le bytes of zeros is fine
}

function makeClient() {
  return {
    sends: [],
    sendJSON(obj) { this.sends.push(obj); return true; }
  };
}

function makeTiming() {
  return {
    events: [],
    push(event, extra) { this.events.push(Object.assign({ event }, extra || {})); },
    count(event) { return this.events.filter((e) => e.event === event).length; }
  };
}

// Playback wired for logic tests: fake ctx + injectable clock.
function makePlayback(opts) {
  opts = opts || {};
  const client = opts.client || makeClient();
  const timing = opts.timing || makeTiming();
  let t = 0;
  const pb = new RTCPlayback({
    client,
    timing,
    now: () => t,
    setTimeoutFn: opts.setTimeoutFn,
    clearTimeoutFn: opts.clearTimeoutFn,
    tuning: Object.assign({ interruptRms: 0.08, interruptSustainMs: 200, maxQueuedChunks: MAX_QUEUED_CHUNKS }, opts.tuning || {})
  });
  pb.setAudioContext(new FakeAudioContext());
  pb._tick = (ms) => { t += ms; };
  pb.testClient = client;
  pb.testTiming = timing;
  return pb;
}

/** Manual timer queue — drives the drain watchdog without real waits. */
function FakeTimers() {
  this.pending = new Map();
  this._next = 0;
}
FakeTimers.prototype.setTimeoutFn = function (fn, ms) {
  const id = ++this._next;
  this.pending.set(id, { fn, ms });
  return id;
};
FakeTimers.prototype.clearTimeoutFn = function (id) {
  this.pending.delete(id);
};
FakeTimers.prototype.fireAll = function () {
  const items = Array.from(this.pending.values());
  this.pending.clear();
  for (const it of items) it.fn();
};
FakeTimers.prototype.pendingMs = function () {
  return Array.from(this.pending.values()).map((it) => it.ms);
};

// ---------------------------------------------------------------------------
// 1. resampleTo16k — 48 kHz sine → 16 kHz
// ---------------------------------------------------------------------------
{
  // 440 Hz, amplitude 0.9, one full second at 48 kHz.
  const input = new Float32Array(48000);
  for (let j = 0; j < input.length; j++) {
    input[j] = 0.9 * Math.sin(2 * Math.PI * 440 * j / 48000);
  }
  const r = resampleTo16k(input, 48000, new Int16Array(0));
  assert.strictEqual(r.output.length, 16000, '1 s at 48 kHz → 16000 samples (50 exact blocks)');
  assert.strictEqual(r.tail.length, 0, '16000 divides evenly into 320-sample blocks');
  // ratio is exactly 3 → each output sample is the input at time j/16000
  // (fraction 0); the only error is int16 quantization. Sample every 100th.
  for (let i = 0; i < 16000; i += 100) {
    const expected = 0.9 * Math.sin(2 * Math.PI * 440 * i / 16000);
    const got = r.output[i] / 32768;
    assert.ok(
      Math.abs(got - expected) < 0.02,
      `sample ${i}: |${got} - ${expected}| < 2%`
    );
  }
}

// ---------------------------------------------------------------------------
// 1b. resampleTo16k — tail/remainder semantics + identity rate + constants
// ---------------------------------------------------------------------------
{
  // Identity rate (16 kHz in): 1000 samples → 3 blocks emitted, 40 kept.
  const a = resampleTo16k(new Float32Array(1000).fill(0.25), 16000, new Int16Array(0));
  assert.strictEqual(a.output.length, 3 * EMIT_BLOCK_SAMPLES);
  assert.strictEqual(a.tail.length, 40);

  // The tail joins the next call: 40 + 280 = exactly one block.
  const b = resampleTo16k(new Float32Array(280).fill(0.25), 16000, a.tail);
  assert.strictEqual(b.output.length, EMIT_BLOCK_SAMPLES);
  assert.strictEqual(b.tail.length, 0);

  // 4800 @ 48 kHz → 1600 → 5 blocks, no remainder (mirrors the legacy test).
  const c = resampleTo16k(new Float32Array(4800).fill(0.5), 48000, new Int16Array(0));
  assert.strictEqual(c.output.length, 1600);
  assert.strictEqual(c.tail.length, 0);
  const first = c.output[0];
  assert.ok(Math.abs(first - 16384) <= 1, 'constant 0.5 resamples to ~16384');

  // Downsampled amplitude rides along: 1600 @ 16 kHz out of 4800 @ 48 kHz.
  const loud = new Float32Array(4800);
  for (let j = 0; j < loud.length; j++) loud[j] = 0.9 * Math.sin(2 * Math.PI * 300 * j / 48000);
  const d = resampleTo16k(loud, 48000, new Int16Array(0));
  assert.strictEqual(d.output.length, 1600);
  assert.ok(Math.abs(d.output[100] / 32768 - 0.9 * Math.sin(2 * Math.PI * 300 * 100 / 16000)) < 0.02);
}

// ---------------------------------------------------------------------------
// 2. floatToInt16 — clamping
// ---------------------------------------------------------------------------
{
  const out = floatToInt16(new Float32Array([1.5, -1.5, 0.5, 0, 1.0, -1.0]));
  assert.strictEqual(out[0], 32767, '+1.5 clamps to 32767');
  assert.strictEqual(out[1], -32768, '-1.5 clamps to -32768');
  assert.ok(Math.abs(out[2] - 16384) <= 1, '0.5 → ~16384');
  assert.strictEqual(out[3], 0, '0 → 0');
  assert.strictEqual(out[4], 32767, '+1.0 → 32767');
  assert.strictEqual(out[5], -32768, '-1.0 → -32768');
}

// ---------------------------------------------------------------------------
// 3. RMS interruption window (fake clock, 40 ms per frame)
// ---------------------------------------------------------------------------
{
  // 3a. Silent frames never trigger — even after a long time.
  const pb = makePlayback();
  pb.onTurnStart(1, 1);
  pb.onAudioStart(1, 's1');
  pb.playing = true; // simulate live playback without scheduling audio
  for (let i = 0; i < 30; i++) {
    pb.onFrame(new Float32Array(1024).fill(0));
    pb._tick(40);
  }
  assert.strictEqual(pb.testClient.sends.filter((m) => m.type === 'interrupt').length, 0,
    'silence never interrupts');
  assert.strictEqual(pb.testTiming.count('interrupt_start'), 0);

  // 3b. Sustained loud frames trigger after INTERRUPT_SUSTAIN_MS.
  const pb2 = makePlayback();
  pb2.onTurnStart(1, 1);
  pb2.onAudioStart(1, 's1');
  pb2.playing = true;
  const loud = new Float32Array(1024).fill(0.5); // rms 0.5 ≥ 0.08
  let firedAt = -1;
  for (let i = 0; i < 20 && firedAt < 0; i++) {
    pb2.onFrame(loud);
    if (pb2.testClient.sends.some((m) => m.type === 'interrupt')) firedAt = i;
    pb2._tick(40);
  }
  assert.ok(firedAt >= 0, 'sustained energy interrupts');
  assert.ok(firedAt >= 5, `sustain window honored (fired on frame ${firedAt}, not before 200ms)`);
  const msg = pb2.testClient.sends.find((m) => m.type === 'interrupt');
  assert.strictEqual(msg.epoch, 1);
  assert.strictEqual(msg.last_sentence_id, 's1');
  assert.strictEqual(typeof msg.played_until_ms, 'number');
  assert.strictEqual(pb2.testTiming.count('interrupt_start'), 1);
  assert.strictEqual(pb2.testTiming.count('interrupt_stop'), 1);
  assert.strictEqual(pb2.playing, false, 'stopNow ran');

  // 3c. Intermittent frames reset the window and never trigger.
  const pb3 = makePlayback();
  pb3.onTurnStart(1, 1);
  pb3.onAudioStart(1, 's1');
  pb3.playing = true;
  for (let i = 0; i < 40; i++) {
    pb3.onFrame(loud);
    pb3._tick(40);
    pb3.onFrame(new Float32Array(1024).fill(0)); // below threshold resets
    pb3._tick(40);
  }
  assert.strictEqual(pb3.testClient.sends.filter((m) => m.type === 'interrupt').length, 0,
    'intermittent energy never sustains');
}

// ---------------------------------------------------------------------------
// 4. playedUntilMs — table-driven
// ---------------------------------------------------------------------------
{
  const cases = [
    // [scheduledEnd, currentTime, samples, rate, expectedMs]
    [10.0, 10.0, 24000, 24000, 1000],  // fully played
    [10.0, 9.5, 24000, 24000, 500],    // half the sentence still queued
    [10.0, 11.0, 24000, 24000, 1000],  // past the end → clamped to total
    [5.0, 4.9, 12000, 24000, 400],     // 500ms total − 100ms remaining
    [0.0, 0.0, 0, 24000, 0]            // nothing scheduled
  ];
  for (const [end, now, samples, rate, want] of cases) {
    assert.strictEqual(
      playedUntilMs(end, now, samples, rate),
      want,
      `playedUntilMs(${end}, ${now}, ${samples}, ${rate}) === ${want}`
    );
  }
  assert.strictEqual(TTS_RATE, 24000);

  // URL tuning overrides parse; garbage is ignored.
  const t1 = applyTuning({ get: (k) => ({ interrupt_rms: '2.5', interrupt_sustain_ms: '500', queue_max: '8' }[k]) });
  assert.strictEqual(t1.interruptRms, 2.5);
  assert.strictEqual(t1.interruptSustainMs, 500);
  assert.strictEqual(t1.maxQueuedChunks, 8);
  const t2 = applyTuning({ get: (k) => ({ interrupt_rms: 'abc', interrupt_sustain_ms: '-3' }[k]) });
  assert.strictEqual(t2.interruptRms, 0.08, 'garbage rms ignored');
  assert.strictEqual(t2.interruptSustainMs, 200, 'negative sustain ignored');
}

// ---------------------------------------------------------------------------
// 5. Generation token + epoch gate + queue cap
// ---------------------------------------------------------------------------
{
  // 5a. Stale onended after stopNow: no sentence_ack, no revival.
  const pb = makePlayback();
  pb.onTurnStart(1, 1);
  pb.onAudioStart(1, 's1');
  pb.onAudioChunk(chunkBuffer(2400));
  pb.onAudioChunk(chunkBuffer(2400));
  assert.strictEqual(pb.audioCtx.sources.length, 2, 'two chunks scheduled');
  assert.strictEqual(pb.playing, true);
  assert.strictEqual(pb.testTiming.count('first_audio_played'), 1,
    'first chunk of the turn logged exactly once');
  pb.onAudioEnd(1, 's1');
  assert.strictEqual(pb.testClient.sends.filter((m) => m.type === 'sentence_ack').length, 0,
    'ack waits for sources to end');

  const staleOnEnded = pb.audioCtx.sources[0].onended;
  pb.interruptNow(); // manual interrupt → stopNow
  assert.ok(pb.audioCtx.sources[0].stopped, 'in-flight source stopped');
  assert.strictEqual(pb.playing, false);
  staleOnEnded(); // the browser would still deliver this
  assert.strictEqual(pb.testClient.sends.filter((m) => m.type === 'sentence_ack').length, 0,
    'stale onended cannot ack a cancelled sentence');
  const interruptMsg = pb.testClient.sends.find((m) => m.type === 'interrupt');
  assert.ok(interruptMsg, 'manual interrupt sends the control message');
  assert.deepStrictEqual(
    [interruptMsg.epoch, interruptMsg.last_sentence_id],
    [1, 's1'],
    'interrupt carries epoch + sentence id'
  );

  // Late chunk for the cancelled epoch: dropped, never scheduled.
  const before = pb.audioCtx.sources.length;
  pb.onAudioChunk(chunkBuffer(2400));
  assert.strictEqual(pb.audioCtx.sources.length, before, 'late chunk not scheduled');
  assert.strictEqual(pb.droppedChunks, 1);

  // Re-enable via the NEXT epoch's audio_start (interrupt_ack bumps server epoch).
  pb.onTurnStart(2, 2);
  pb.onAudioStart(2, 's2');
  pb.onAudioChunk(chunkBuffer(2400));
  assert.strictEqual(pb.audioCtx.sources.length, before + 1, 'new epoch accepted');
  assert.strictEqual(pb.testTiming.count('first_audio_played'), 2, 'new turn logs its own first audio');
}

{
  // 5a-bis. Server-driven epoch bump (interrupt_ack): onEpochBump stops the
  // OLD epoch's queued/live sources outright (the epoch gate alone only
  // drops future chunks — it never stops what is already scheduled) and
  // adopts the new epoch, so the NEXT announcement's audio_start — which,
  // like every announcement, carries no turn_start — is accepted and can
  // ack normally. Without the adopt the picked classroom's greeting would
  // be rejected as a stale epoch and die on the playback-ack timeout.
  const pb = makePlayback();
  pb.onTurnStart(0, 0); // the clarify question rides the initial epoch
  pb.onAudioStart(0, 'ask-s0');
  pb.onAudioChunk(chunkBuffer(2400));
  pb.onAudioChunk(chunkBuffer(2400));
  assert.strictEqual(pb.audioCtx.sources.length, 2, 'ask chunks scheduled');

  // The child taps a subject button: the server kills the ask and ships
  // interrupt_ack(old=0, new=1).
  pb.onEpochBump(1);
  assert.ok(pb.audioCtx.sources.every((src) => src.stopped),
    'queued ask sources stopped, the child does not sit through the rest of the question');
  assert.strictEqual(pb.playing, false);
  const staleOnEnded = pb.audioCtx.sources[0].onended;
  staleOnEnded();
  assert.strictEqual(pb.testClient.sends.filter((m) => m.type === 'sentence_ack').length, 0,
    'a stopped ask source can never ack');

  // Late chunk of the dead ask: dropped by the (still closed) acceptance gate.
  const before = pb.audioCtx.sources.length;
  pb.onAudioChunk(chunkBuffer(2400));
  assert.strictEqual(pb.audioCtx.sources.length, before, 'dead ask chunk not scheduled');

  // The picked classroom's greeting arrives WITHOUT a turn_start (server
  // announcements never carry one) under the NEW epoch: accepted, plays,
  // and acks — the classroom lives.
  pb.onAudioStart(1, 'greet-s0');
  pb.onAudioChunk(chunkBuffer(2400));
  assert.strictEqual(pb.audioCtx.sources.length, before + 1, 'new-epoch greeting accepted without turn_start');
  pb.onAudioEnd(1, 'greet-s0');
  pb.audioCtx.sources[pb.audioCtx.sources.length - 1].onended();
  const acks = pb.testClient.sends.filter((m) => m.type === 'sentence_ack');
  assert.strictEqual(acks.length, 1, 'greeting acked under the new epoch');
  assert.deepStrictEqual([acks[0].epoch, acks[0].sentence_id], [1, 'greet-s0']);
}

{
  // 5b. Happy path: valid onended after audio_end sends exactly one ack.
  const pb = makePlayback();
  pb.onTurnStart(1, 1);
  pb.onAudioStart(1, 's1');
  pb.onAudioChunk(chunkBuffer(2400));
  pb.onAudioEnd(1, 's1');
  pb.audioCtx.sources[0].onended();
  const acks = pb.testClient.sends.filter((m) => m.type === 'sentence_ack');
  assert.strictEqual(acks.length, 1, 'sentence completed → exactly one ack');
  assert.strictEqual(acks[0].sentence_id, 's1');
  assert.strictEqual(acks[0].epoch, 1);

  // A second sentence in the same turn acks separately.
  pb.onAudioStart(1, 's2');
  pb.onAudioChunk(chunkBuffer(2400));
  pb.onAudioEnd(1, 's2');
  pb.audioCtx.sources[1].onended();
  assert.strictEqual(pb.testClient.sends.filter((m) => m.type === 'sentence_ack').length, 2);

  // Gapless scheduling: the second sentence starts after the first ends.
  assert.strictEqual(
    pb.audioCtx.sources[1].startTimes[0],
    pb.audioCtx.sources[0].startTimes[0] + 2400 / TTS_RATE,
    'sentence 2 scheduled behind sentence 1'
  );
}

{
  // 5c. Queue cap (T-13-06c): a flooding server cannot schedule unbounded.
  const pb = makePlayback({ tuning: { maxQueuedChunks: 4 } });
  pb.onTurnStart(1, 1);
  pb.onAudioStart(1, 's1');
  for (let i = 0; i < 10; i++) pb.onAudioChunk(chunkBuffer(2400));
  assert.strictEqual(pb.audioCtx.sources.length, 4, 'scheduled-ahead sources capped');
  assert.strictEqual(pb.droppedChunks, 6, 'excess chunks dropped and counted');
}

// ---------------------------------------------------------------------------
// 6. Per-sentence ledger (13-11): overlapping sentences ack independently
// ---------------------------------------------------------------------------
{
  const pb = makePlayback();
  pb.onTurnStart(1, 1);
  pb.onAudioStart(1, 's1');
  pb.onAudioChunk(chunkBuffer(2400)); // s1 src0
  pb.onAudioChunk(chunkBuffer(2400)); // s1 src1
  // Adjacent-sentence overlap: s2 announces BEFORE s1's last onended.
  pb.onAudioStart(1, 's2');
  pb.onAudioChunk(chunkBuffer(2400)); // s2 src2
  pb.onAudioEnd(1, 's1');
  pb.audioCtx.sources[0].onended();
  pb.audioCtx.sources[1].onended();
  let acks = pb.testClient.sends.filter((m) => m.type === 'sentence_ack');
  assert.strictEqual(acks.length, 1, 's1 acks while s2 is still scheduled');
  assert.strictEqual(acks[0].sentence_id, 's1');
  assert.strictEqual(acks[0].epoch, 1);
  assert.strictEqual(pb._acceptingAudio, true, 's1 completion does not close s2 chunk gate');

  pb.onAudioEnd(1, 's2');
  pb.audioCtx.sources[2].onended();
  acks = pb.testClient.sends.filter((m) => m.type === 'sentence_ack');
  assert.strictEqual(acks.length, 2, 's2 acks after its own sources end');
  assert.strictEqual(acks[1].sentence_id, 's2');

  // Duplicate audio_end + extra onended for the same key: still one ack.
  pb.onAudioEnd(1, 's2');
  pb.audioCtx.sources[2].onended();
  acks = pb.testClient.sends.filter((m) => m.type === 'sentence_ack');
  assert.strictEqual(acks.length, 2, 'duplicate events never double-ack');

  // Ack ordering is observable: each sentence's first onended precedes its
  // ack entry (a duplicate onended may append after — irrelevant).
  const trace = pb.trace;
  assert.ok(trace.indexOf('onended:1:s1') >= 0 &&
    trace.indexOf('onended:1:s1') < trace.indexOf('ack:1:s1'),
    's1 onended precedes its ack in the trace');
  assert.ok(trace.indexOf('onended:1:s2') >= 0 &&
    trace.indexOf('onended:1:s2') < trace.indexOf('ack:1:s2'),
    's2 onended precedes its ack in the trace');
}

// ---------------------------------------------------------------------------
// 7. Old-epoch late onended must never ack the new sentence (13-11)
// ---------------------------------------------------------------------------
{
  const pb = makePlayback();
  pb.onTurnStart(1, 1);
  pb.onAudioStart(1, 's1');
  pb.onAudioChunk(chunkBuffer(2400));
  const oldSource = pb.audioCtx.sources[0];
  // Server-driven epoch change without a stop: the old entry stays behind.
  pb.onTurnStart(2, 2);
  pb.onAudioStart(2, 's2');
  pb.onAudioChunk(chunkBuffer(2400));
  pb.onAudioEnd(2, 's2');
  pb.audioCtx.sources[1].onended();
  let acks = pb.testClient.sends.filter((m) => m.type === 'sentence_ack');
  assert.strictEqual(acks.length, 1);
  assert.deepStrictEqual([acks[0].epoch, acks[0].sentence_id], [2, 's2'],
    'only the new-epoch sentence acks');

  oldSource.onended(); // old epoch's source ends late
  acks = pb.testClient.sends.filter((m) => m.type === 'sentence_ack');
  assert.strictEqual(acks.length, 1, 'old-epoch onended acks nothing');

  // audio_end for the OLD epoch is ignored outright (epoch gate).
  pb.onAudioEnd(1, 's1');
  assert.strictEqual(pb.testClient.sends.filter((m) => m.type === 'sentence_ack').length, 1,
    'old-epoch audio_end records no ack');
}

// ---------------------------------------------------------------------------
// 7b. Restart on a FRESH AudioContext: setAudioContext resets the scheduling
// cursor (J16 再练一组 after a natural ending — releaseSharedResources keeps
// the old cursor; only stopNow zeroes it, so the explicit-end restart was
// never affected).
// ---------------------------------------------------------------------------
{
  const pb = makePlayback();
  // Session 1 schedules 60 s of audio, then ends NATURALLY (every source's
  // real onended → drain 'drained' → release: ledger cleared, audioCtx
  // dropped — the cursor survives on the old clock).
  pb.onTurnStart(1, 1);
  pb.onAudioStart(1, 's1');
  for (let i = 0; i < 600; i++) pb.onAudioChunk(chunkBuffer(2400)); // 6 s each
  pb.onAudioEnd(1, 's1');
  for (const src of pb.audioCtx.sources) src.onended();
  assert.ok(pb._cursor > 100, 'session 1 leaves a large cursor on its clock');
  pb.releaseSharedResources();

  // Session 2 (再练一组): a FRESH context whose clock starts over. The first
  // chunk must schedule at NOW + lead — not at the dead clock's cursor.
  pb.resetSession();
  pb.setAudioContext(new FakeAudioContext());
  pb.onTurnStart(1, 0); // fresh connection restarts the id namespace
  pb.onAudioStart(0, 't1-s0');
  pb.onAudioChunk(chunkBuffer(2400));
  const ctx = pb.audioCtx;
  const scheduled = ctx.sources[ctx.sources.length - 1].startTimes[0];
  assert.ok(scheduled <= ctx.currentTime + 0.5,
    'first restart chunk schedules on the new clock (' + scheduled.toFixed(2)
    + ' vs now ' + ctx.currentTime.toFixed(2) + '), not the old cursor');
  // And it still acks through the real onended, not the old ledger. Session
  // 1's natural ending acked first (its own real onendeds), so the restart's
  // evidence is its OWN key acking exactly once — through THIS source.
  pb.onAudioEnd(0, 't1-s0');
  ctx.sources[ctx.sources.length - 1].onended();
  const restartAcks = pb.testClient.sends.filter((m) => m.type === 'sentence_ack'
    && m.epoch === 0 && m.sentence_id === 't1-s0');
  assert.strictEqual(restartAcks.length, 1, 'the restart round acks its own key exactly once');
}

// ---------------------------------------------------------------------------
// 8-10. drainNaturally + watchdog (13-11, T-13-24b) — async contract
// ---------------------------------------------------------------------------
(async function testDrainContracts() {
  // 8a. Normal drain: nothing is stopped, resolves after the real last
  // onended, repeat calls share one promise, post-completion call is instant.
  {
    const pb = makePlayback();
    pb.onTurnStart(1, 1);
    pb.onAudioStart(1, 's1');
    pb.onAudioChunk(chunkBuffer(2400));
    pb.onAudioChunk(chunkBuffer(2400));
    const p1 = pb.drainNaturally();
    const p2 = pb.drainNaturally();
    assert.strictEqual(p1, p2, 'repeat drain calls return the same promise');
    assert.strictEqual(pb.audioCtx.sources[0].stopped, false, 'drain stops no source');
    assert.strictEqual(pb.stopNowCalls, 0, 'drain never calls stopNow');
    pb.audioCtx.sources[0].onended();
    assert.strictEqual(pb.testClient.sends.filter((m) => m.type === 'sentence_ack').length, 0,
      'no audio_end seen yet — no ack even after every source ended');
    pb.onAudioEnd(1, 's1');
    pb.audioCtx.sources[1].onended();
    assert.strictEqual(await p1, 'drained', 'drain resolves after the real last onended');
    assert.ok(pb.trace.indexOf('drain:drained') > pb.trace.lastIndexOf('onended:1:s1'),
      'drain completion follows the real last onended in the trace');
    assert.strictEqual(await pb.drainNaturally(), 'drained', 'post-drain call resolves instantly');
  }

  // 8b. Explicit stopNow mid-drain resolves 'stopped' and stops sources.
  {
    const pb = makePlayback();
    pb.onTurnStart(1, 1);
    pb.onAudioStart(1, 's1');
    pb.onAudioChunk(chunkBuffer(2400));
    const p = pb.drainNaturally();
    pb.interruptNow();
    assert.strictEqual(await p, 'stopped', 'explicit cancel resolves stopped');
    assert.ok(pb.audioCtx.sources[0].stopped, 'mid-drain stopNow stops the source');
    assert.strictEqual(pb.stopNowCalls, 1);
  }

  // 9. Watchdog — remaining-schedule derivation with both clamp bounds,
  // and the timed-out path never records a sentence_ack.
  {
    // 9a. Floor clamp (3 s): cursor already behind the audio clock.
    const timers = new FakeTimers();
    const pb = makePlayback({ setTimeoutFn: (fn, ms) => timers.setTimeoutFn(fn, ms), clearTimeoutFn: (id) => timers.clearTimeoutFn(id) });
    pb.onTurnStart(1, 1);
    pb.onAudioStart(1, 's1');
    pb.onAudioChunk(chunkBuffer(2400));
    pb.audioCtx.currentTime = 200; // audio clock ran past the cursor
    const p = pb.drainNaturally();
    assert.deepStrictEqual(timers.pendingMs(), [3000],
      'watchdog = remaining + 3 s, floored at 3 s');
    timers.fireAll();
    assert.strictEqual(await p, 'timed_out', 'watchdog resolves timed_out');
    assert.ok(pb.audioCtx.sources[0].stopped, 'watchdog cleanup stops sources');
    assert.strictEqual(pb.stopNowCalls, 1, 'watchdog stopNow is counted');
    assert.strictEqual(pb.testClient.sends.filter((m) => m.type === 'sentence_ack').length, 0,
      'watchdog path never fakes a sentence_ack');
    assert.ok(pb.trace.includes('drain:timed_out'));

    // 9b. Ceiling clamp (45 s): 500 chunks = 50 s scheduled ahead.
    const timers2 = new FakeTimers();
    const pb2 = makePlayback({
      setTimeoutFn: (fn, ms) => timers2.setTimeoutFn(fn, ms),
      clearTimeoutFn: (id) => timers2.clearTimeoutFn(id),
      tuning: { maxQueuedChunks: 1000 }
    });
    pb2.onTurnStart(1, 1);
    pb2.onAudioStart(1, 's1');
    for (let i = 0; i < 500; i++) pb2.onAudioChunk(chunkBuffer(2400));
    pb2.onAudioEnd(1, 's1');
    const p2 = pb2.drainNaturally();
    assert.deepStrictEqual(timers2.pendingMs(), [45000],
      'watchdog derived from 50 s remaining, capped at 45 s');
    // Before the watchdog: sources are untouched (no stopNow).
    assert.strictEqual(pb2.stopNowCalls, 0);
    assert.strictEqual(pb2.audioCtx.sources[0].stopped, false);
    timers2.fireAll();
    assert.strictEqual(await p2, 'timed_out');
    assert.strictEqual(pb2.testClient.sends.filter((m) => m.type === 'sentence_ack').length, 0,
      'capped watchdog path also never fakes a sentence_ack');
  }

  console.log('test-rt-playback.js: all assertions passed');
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
