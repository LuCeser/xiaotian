// tests/test-timing.js — pure-function contracts for the P0-1/P1-1 pacing fix:
//   1. adaptiveWritingMs (pkg/web/static/js/timing.js) mirrors
//      pkg/session/timing.go AdaptiveTimeout: 8s base, +1s/char beyond 4,
//      capped at 15s.
//   2. AudioController double-play schedule math (audio_plays === 2) against
//      a mocked AudioContext — two BufferSources, 1s gap, end callback only
//      after the last source, stale onended neutralized by stop().
//   2R. AudioController rolling accumulator routing — frames→complete
//      association with wordId null throughout, fresh accumulation after a
//      complete (backend self-advance immunity), REPEAT_WORD replay using
//      only new frames, and stop() clearing the buffer.
//   3. WebSocketClient batch-mode gating (P0-1): per-word mode neither
//      advances nor sends next_word; batch mode schedules the adaptive gap
//      and session_complete clears the pending timer.
// Runs on stock Node with no framework and no installs:
// `node tests/test-timing.js`.
'use strict';

const assert = require('node:assert');

// The IIFE assigns onto its `global` arg — in Node's CommonJS that is the
// module's top-level `this` (module.exports). Same pattern as test-batch-form.js.
const { adaptiveWritingMs } = require('../pkg/web/static/js/timing.js');
const { AudioController } = require('../pkg/web/static/js/audio-controller.js');

// ---------------------------------------------------------------------------
// 1. adaptiveWritingMs — formula pins (Go: AdaptiveTimeout)
// ---------------------------------------------------------------------------
assert.strictEqual(adaptiveWritingMs(''), 8000);        // 0 chars → base
assert.strictEqual(adaptiveWritingMs('cat'), 8000);     // 3 chars → base
assert.strictEqual(adaptiveWritingMs('jump'), 8000);    // exactly 4 chars → base
assert.strictEqual(adaptiveWritingMs('apple'), 9000);   // 5 chars → base + 1s
assert.strictEqual(adaptiveWritingMs('watermelon'), 14000); // 10 chars → base + 6s
assert.strictEqual(adaptiveWritingMs('a'.repeat(11)), 15000); // 8+7 → exactly at cap
assert.strictEqual(adaptiveWritingMs('a'.repeat(12)), 15000); // 8+8 → capped
assert.strictEqual(adaptiveWritingMs('a'.repeat(20)), 15000); // 20 chars → capped
assert.strictEqual(adaptiveWritingMs(undefined), 8000); // non-string → length 0
assert.strictEqual(adaptiveWritingMs(null), 8000);

// ---------------------------------------------------------------------------
// 2. AudioController double-play schedule math (mocked audioCtx)
// ---------------------------------------------------------------------------

// Minimal AudioContext mock: 16kHz, records every BufferSource's start times.
// _scheduleWordPlayback only uses currentTime/destination/state/createBuffer/
// createBufferSource — all stubbed here.
function FakeAudioContext() {
  this.currentTime = 100; // arbitrary nonzero audio-clock base
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
  var ctx = this;
  var src = {
    buffer: null,
    onended: null,
    onerror: null,
    startTimes: [],
    connect: function () {},
    start: function (t) { this.startTimes.push(t); }
  };
  ctx.sources.push(src);
  return src;
};

// 4 bytes = 2 samples @16kHz. The math is rate-independent.
// Conforming frame: 640 bytes = 20ms @ 16kHz 16-bit mono — the controller's
// AUDIO_FRAME_SIZE. One frame → 320 samples → DUR seconds in the mock
// (computed identically to the mock's duration = numSamples/rate, so the
// assertions below are exact float equality).
const FRAME_BYTES = 640;
const DUR = (FRAME_BYTES / 2) / 16000;

// Expected mock buffer duration for `frames` conforming frames.
function framesDuration(frames) {
  return (frames * FRAME_BYTES / 2) / 16000;
}

function makeController() {
  var ac = new AudioController();
  ac.audioCtx = new FakeAudioContext();
  return ac;
}

// Feed `count` conforming frames through the public routing entry point
// (handleAudioFrame) — the same path app.js's binary-frame dispatch uses.
// `wordId` is log-only under the rolling accumulator; tests pass real ids or
// null to prove routing never depends on it.
function feedFrames(ac, count, startSeq, wordId) {
  for (var i = 0; i < count; i++) {
    ac.handleAudioFrame(new Uint8Array(FRAME_BYTES), startSeq + i, wordId);
  }
}

// 2a. Default (plays absent) → single source, today's behavior exactly.
{
  const ac = makeController();
  let ended = 0;
  ac.setOnPlaybackEnd(function () { ended += 1; });
  feedFrames(ac, 1, 1, 'apple'); // word frames arrive BEFORE word_complete
  ac.playWordComplete('apple');
  assert.strictEqual(ac.audioCtx.sources.length, 1);
  assert.deepStrictEqual(ac.audioCtx.sources[0].startTimes, [100]);
  assert.strictEqual(ac.scheduledTime, 100 + DUR);
  assert.strictEqual(ac.isPlaying, true);
  ac.audioCtx.sources[0].onended();
  assert.strictEqual(ac.isPlaying, false);
  assert.strictEqual(ended, 1);
}

// 2b. Explicit plays === 1 → same as default.
{
  const ac = makeController();
  feedFrames(ac, 1, 1, 'apple');
  ac.playWordComplete('apple', 1);
  assert.strictEqual(ac.audioCtx.sources.length, 1);
  assert.deepStrictEqual(ac.audioCtx.sources[0].startTimes, [100]);
}

// 2c. plays === 2 → two sources from the same buffer, 1s gap, scheduledTime
//     at the second play's end, end callback fires only after the LAST source.
{
  const ac = makeController();
  let ended = 0;
  ac.setOnPlaybackEnd(function () { ended += 1; });
  feedFrames(ac, 1, 1, 'apple');
  ac.playWordComplete('apple', 2);
  assert.strictEqual(ac.audioCtx.sources.length, 2);
  const firstStart = 100;
  const secondStart = 100 + DUR + 1.0; // 1s gap between pronunciations
  assert.deepStrictEqual(ac.audioCtx.sources[0].startTimes, [firstStart]);
  assert.deepStrictEqual(ac.audioCtx.sources[1].startTimes, [secondStart]);
  assert.strictEqual(ac.scheduledTime, secondStart + DUR);
  assert.strictEqual(ac.isPlaying, true);
  ac.audioCtx.sources[0].onended(); // first pronunciation ends…
  assert.strictEqual(ac.isPlaying, true); // …second still pending
  assert.strictEqual(ended, 0);
  ac.audioCtx.sources[1].onended(); // last source ends
  assert.strictEqual(ac.isPlaying, false);
  assert.strictEqual(ended, 1);
}

// 2d. stop() between the two plays → both onended handlers go stale; the
//     end callback must never fire and state stays reset.
{
  const ac = makeController();
  let ended = 0;
  ac.setOnPlaybackEnd(function () { ended += 1; });
  feedFrames(ac, 1, 1, 'apple');
  ac.playWordComplete('apple', 2);
  const sources = ac.audioCtx.sources.slice();
  ac.stop();
  sources[0].onended(); // stale — ignored
  sources[1].onended(); // stale — must NOT fire callbacks
  assert.strictEqual(ended, 0);
  assert.strictEqual(ac.isPlaying, false);
  assert.strictEqual(ac.scheduledTime, 0);
}

// ---------------------------------------------------------------------------
// 2R. Rolling accumulator routing — word_complete delimits the word
//     (audio association is immune to currentWordId drift / backend
//     self-advance; app.js passes wordId = null since the routing fix)
// ---------------------------------------------------------------------------

// 2R-a. Frames→complete association works with wordId NULL throughout: every
//     frame since the last word_complete plays, none dropped.
{
  const ac = makeController();
  feedFrames(ac, 3, 1, null);
  ac.playWordComplete('apple', 1);
  assert.strictEqual(ac.audioCtx.sources.length, 1);
  assert.strictEqual(ac.audioCtx.sources[0].buffer.duration, framesDuration(3));
}

// 2R-b. Word 2's frames accumulate FRESH after word 1's complete consumed the
//     buffer — even when the streamed word's id no longer matches any
//     frontend bookkeeping (the iPad bug: backend self-advanced to 'banana'
//     while currentWordId still pointed at 'apple'). The second complete must
//     play ONLY word 2's frames, not word 1's leftovers.
{
  const ac = makeController();
  feedFrames(ac, 2, 1, 'apple');
  ac.playWordComplete('apple', 1);
  feedFrames(ac, 4, 3, 'apple'); // drifted id — routing must not care
  ac.playWordComplete('banana', 1);
  assert.strictEqual(ac.audioCtx.sources.length, 2);
  assert.strictEqual(ac.audioCtx.sources[0].buffer.duration, framesDuration(2));
  assert.strictEqual(ac.audioCtx.sources[1].buffer.duration, framesDuration(4)); // NOT 6 frames
}

// 2R-c. REPEAT_WORD: the re-stream appends fresh frames to the cleared buffer;
//     the repeat's word_complete plays ONLY the new audio — no stale
//     double-append of the original play.
{
  const ac = makeController();
  feedFrames(ac, 2, 1, 'apple');
  ac.playWordComplete('apple', 1);
  feedFrames(ac, 2, 3, 'apple'); // backend re-streams after repeat_word
  ac.playWordComplete('apple', 1);
  assert.strictEqual(ac.audioCtx.sources.length, 2);
  assert.strictEqual(ac.audioCtx.sources[1].buffer.duration, framesDuration(2)); // NOT 4 frames
}

// 2R-d. stop() clears the rolling buffer: pre-stop frames never play, and
//     post-stop frames (lastSeq reset included) still schedule normally.
{
  const ac = makeController();
  feedFrames(ac, 2, 1, 'apple');
  ac.stop();
  ac.playWordComplete('apple', 1);
  assert.strictEqual(ac.audioCtx.sources.length, 0); // buffer emptied by stop()
  feedFrames(ac, 1, 2, 'apple'); // lastSeq was reset too, so seq=2 is accepted
  ac.playWordComplete('apple', 1);
  assert.strictEqual(ac.audioCtx.sources.length, 1);
  assert.strictEqual(ac.audioCtx.sources[0].buffer.duration, framesDuration(1));
}

// ---------------------------------------------------------------------------
// 2R'. playWordComplete return value = total scheduled playback ms
//     (2026-09-24 pacing fix: the batch writing gap is extended by this so
//     the double play no longer eats the student's writing time)
// ---------------------------------------------------------------------------

// 2P-a. Single play of 3 frames (60ms) → returns 60; double play → 60*2+1000.
{
  const ac = makeController();
  feedFrames(ac, 3, 1, 'apple');
  const ms1 = ac.playWordComplete('apple', 1);
  assert.strictEqual(ms1, Math.round(framesDuration(3) * 1000));
  feedFrames(ac, 3, 4, 'apple');
  const ms2 = ac.playWordComplete('apple', 2);
  assert.strictEqual(ms2, Math.round((framesDuration(3) * 2 + 1.0) * 1000));
}

// 2P-b. Nothing accumulated / scheduling refused → 0, never undefined.
{
  const ac = makeController();
  assert.strictEqual(ac.playWordComplete('apple', 1), 0);
  const acNoCtx = new AudioController(); // no audioCtx at all
  acNoCtx.handleAudioFrame(new Uint8Array(FRAME_BYTES), 1, 'apple');
  assert.strictEqual(acNoCtx.playWordComplete('apple', 2), 0);
}

// 2P-c. Play-phase transitions: 1 at schedule, 2 when the first of a double
//     play ends, 0 after the last; single play is 1 → 0. Live 'feedback'
//     frames never fire phases.
{
  const ac = makeController();
  const phases = [];
  ac.setOnPlayPhase(function (p) { phases.push(p); });
  feedFrames(ac, 1, 1, 'apple');
  ac.playWordComplete('apple', 2);
  assert.deepStrictEqual(phases, [1]);
  ac.audioCtx.sources[0].onended();
  assert.deepStrictEqual(phases, [1, 2]);
  ac.audioCtx.sources[1].onended();
  assert.deepStrictEqual(phases, [1, 2, 0]);

  phases.length = 0;
  feedFrames(ac, 1, 2, 'apple');
  ac.playWordComplete('apple', 1);
  ac.audioCtx.sources[2].onended();
  assert.deepStrictEqual(phases, [1, 0]);

  phases.length = 0;
  ac.playLiveFrame(new Uint8Array(FRAME_BYTES), 3);
  ac.audioCtx.sources[3].onended();
  assert.deepStrictEqual(phases, []); // 'feedback' frames stay silent
}

// 2P-d. stop() between plays: stale sources fire no phases either.
{
  const ac = makeController();
  const phases = [];
  ac.setOnPlayPhase(function (p) { phases.push(p); });
  feedFrames(ac, 1, 1, 'apple');
  ac.playWordComplete('apple', 2);
  const sources = ac.audioCtx.sources.slice();
  ac.stop();
  sources[0].onended();
  sources[1].onended();
  assert.deepStrictEqual(phases, [1]); // only the schedule-time phase
}

// ---------------------------------------------------------------------------
// 3. WebSocketClient batch-mode gating (P0-1)
// ---------------------------------------------------------------------------
const wsMod = require('../pkg/web/static/js/websocket-client.js');
const { SessionStateManager } = require('../pkg/web/static/js/state-manager.js');

// Wire the runtime fakes the client reads off its captured `global`
// (module.exports in Node). send() while disconnected only queues, so no
// FrameCodec/WebSocket stubs are needed on these paths.
wsMod.adaptiveWritingMs = adaptiveWritingMs;

const timers = [];
wsMod.setTimeout = function (fn, ms) {
  timers.push({ fn: fn, ms: ms, cleared: false });
  return timers.length - 1;
};
wsMod.clearTimeout = function (id) {
  timers[id].cleared = true;
};
function fireTimer(id) {
  if (!timers[id].cleared) {
    timers[id].fn();
  }
}

function makeClient(words) {
  const wc = new wsMod.WebSocketClient('ws://test');
  wc.setStateManager(new SessionStateManager());
  wc._applySessionReady({ type: 'session_ready', session_id: 's1', words: words });
  return wc;
}

// 3a. Per-word mode: no timer, no next_word on the wire, currentWordId stays
//     on the just-played word (advancement belongs to app.js's answer flow).
{
  timers.length = 0;
  const wc = makeClient(['apple', 'banana']);
  wc._applyWordComplete({ type: 'word_complete', word_id: 'apple' });
  const st = wc.stateManager.getState();
  assert.strictEqual(st.completedWords, 1);
  assert.strictEqual(st.currentWordId, 'apple');
  assert.strictEqual(timers.length, 0);
  const sent = wc.messageQueue.filter(function (m) { return m.type === 'next_word'; });
  assert.strictEqual(sent.length, 0);
}

// 3b. Batch mode: one timer with the adaptive gap for 'apple' (5 chars → 9s);
//     firing it advances currentWordId and queues exactly one next_word.
{
  timers.length = 0;
  const wc = makeClient(['apple', 'banana']);
  wc.stateManager.setState({ batchMode: true });
  wc._applyWordComplete({ type: 'word_complete', word_id: 'apple' });
  assert.strictEqual(timers.length, 1);
  assert.strictEqual(timers[0].ms, 9000);
  fireTimer(0);
  const st = wc.stateManager.getState();
  assert.strictEqual(st.currentWordId, 'banana');
  const sent = wc.messageQueue.filter(function (m) { return m.type === 'next_word'; });
  assert.strictEqual(sent.length, 1);
  assert.deepStrictEqual(sent[0], { type: 'next_word', params: { word_id: 'banana' } });
}

// 3c. session_complete clears a pending batch timer — never a stale next_word
//     after the session ended (belt: cleared flag; braces: sessionActive guard).
{
  timers.length = 0;
  const wc = makeClient(['apple', 'banana']);
  wc.stateManager.setState({ batchMode: true });
  wc._applyWordComplete({ type: 'word_complete', word_id: 'apple' });
  assert.strictEqual(timers.length, 1);
  wc._applySessionComplete({ type: 'session_complete', totalWords: 2 });
  assert.strictEqual(timers[0].cleared, true);
  const queuedBefore = wc.messageQueue.length;
  fireTimer(0); // even a late fire must be a no-op
  assert.strictEqual(wc.messageQueue.length, queuedBefore);
}

// 3d. Batch gap playback compensation (2026-09-24): when an AudioController
//     is present, the writing gap = adaptiveWritingMs(word) + the actual
//     scheduled playback ms (double play included), so listening time no
//     longer eats writing time. Without an AudioController the gap is the
//     bare formula (3b pins 9000).
{
  timers.length = 0;
  globalThis.window = {
    audioController: {
      stop: function () {}, // CR-4 session-reset hook in _applySessionReady
      playWordComplete: function () { return 3200; } // e.g. ~1.1s word x2 + 1s gap
    }
  };
  try {
    const wc = makeClient(['apple', 'banana']);
    wc.stateManager.setState({ batchMode: true });
    wc._applyWordComplete({ type: 'word_complete', word_id: 'apple', audio_plays: 2 });
    assert.strictEqual(timers.length, 1);
    assert.strictEqual(timers[0].ms, 9000 + 3200);
    fireTimer(0);
    const st = wc.stateManager.getState();
    assert.strictEqual(st.currentWordId, 'banana');
  } finally {
    delete globalThis.window;
  }
}

console.log('test-timing.js: all assertions passed');
process.exit(0);
