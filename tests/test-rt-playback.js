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
    tuning: Object.assign({ interruptRms: 0.08, interruptSustainMs: 200, maxQueuedChunks: MAX_QUEUED_CHUNKS }, opts.tuning || {})
  });
  pb.setAudioContext(new FakeAudioContext());
  pb._tick = (ms) => { t += ms; };
  pb.testClient = client;
  pb.testTiming = timing;
  return pb;
}

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

console.log('test-rt-playback.js: all assertions passed');
