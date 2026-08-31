// tests/test-voice-command-controller.js — headless verification of the
// VoiceCommandController sample path (Ticket 03): Float32→Int16 conversion,
// native→16kHz resampling, 320-sample chunking with tail retention, the
// zero-padded final chunk (640-byte wire size, D-09), sequence numbering,
// and the client-side overflow cap. Runs on stock Node with no framework
// and no installs: `node tests/test-voice-command-controller.js`.
'use strict';

const assert = require('node:assert');

// The controller IIFE assigns onto its `global` arg — in Node's CommonJS
// that is the module's top-level `this` (module.exports), so the class
// arrives as a named export (no DOM needed for the sample-path methods).
const { VoiceCommandController } = require('../pkg/web/static/js/voice-command-controller.js');

/** Recording wsClient double: captures control sends and binary frames. */
function makeFakeWs() {
  return {
    sends: [],
    frames: [],
    send(payload) { this.sends.push(payload); },
    sendFrame(msgType, seq, bytes) {
      this.frames.push({ msgType, seq, bytes: Buffer.from(bytes) });
      return true;
    }
  };
}

/** Bare AudioContext stand-in: only sampleRate/close are on the sample path. */
function makeCtx(rate) {
  return { sampleRate: rate, state: 'running', close() { this.closed = true; } };
}

/** Build a controller wired to fakes with recording state forced on. */
function makeController(rate) {
  const c = new VoiceCommandController(makeFakeWs());
  c.supported = true;
  c.isRecording = true;
  c._audioCtx = makeCtx(rate);
  return c;
}

// ---------------------------------------------------------------------
// 1. Float32 → Int16 conversion bounds (identity rate: 6 samples → _tail)
// ---------------------------------------------------------------------
{
  const c = makeController(16000);
  c._handleAudio(new Float32Array([1.0, -1.0, 0.5, 0.0, 2.0, -2.0]));
  assert.strictEqual(c._pendingNative.length, 0, 'pending drained synchronously');
  const t = c._tail;
  assert.strictEqual(t[0], 32767, '+1.0 → 32767');
  assert.strictEqual(t[1], -32768, '-1.0 → -32768');
  assert.ok(Math.abs(t[2] - 16384) <= 1, '0.5 → ~16384');
  assert.strictEqual(t[3], 0, '0.0 → 0');
  assert.strictEqual(t[4], 32767, 'clamp above +1');
  assert.strictEqual(t[5], -32768, 'clamp below -1');
}

// ---------------------------------------------------------------------
// 2. Resample 48kHz → 16kHz: length thirds, interpolation on constants
// ---------------------------------------------------------------------
{
  const c = makeController(48000);
  // 4800 native samples → 1600 target samples (already flushed as frames).
  c._handleAudio(new Float32Array(4800).fill(0.5));
  assert.strictEqual(c._pendingNative.length, 0, 'pending drained');
  const shipped = c.wsClient.frames.length * 320;
  const queued = c._chunkQueue.reduce((n, ch) => n + ch.length, 0);
  assert.strictEqual(shipped + queued + c._tail.length, 1600, '4800@48k → 1600@16k');
  const first = c.wsClient.frames[0].bytes.readInt16LE(0);
  assert.ok(Math.abs(first - 16384) <= 1, 'constant signal resamples to constant (got ' + first + ')');
}

// ---------------------------------------------------------------------
// 3. Chunking: full 320-sample chunks ship immediately; remainder waits
// ---------------------------------------------------------------------
{
  const c = makeController(16000); // native == target: no resampling
  c._handleAudio(new Float32Array(750)); // 2 full chunks + 110 tail
  assert.strictEqual(c._chunkQueue.length, 0, 'complete chunks already shipped');
  assert.strictEqual(c.wsClient.frames.length, 2, 'two frames shipped');
  assert.strictEqual(c._tail.length, 110, 'remainder < 320 retained in tail');
  for (const f of c.wsClient.frames) {
    assert.strictEqual(f.bytes.length, 640, 'audio frame payload is 640 bytes (D-09)');
    assert.strictEqual(f.msgType, 1, 'msg type AUDIO');
  }
  assert.strictEqual(c.wsClient.frames[0].seq, 1, 'sequences start at 1');
  assert.strictEqual(c.wsClient.frames[1].seq, 2, 'sequences increase');
}

// ---------------------------------------------------------------------
// 4. stop(): padded final chunk + voice_command trigger + lifecycle flags
// ---------------------------------------------------------------------
{
  const c = makeController(16000);
  c._handleAudio(new Float32Array(750)); // 2 full + 110 tail
  const framesBefore = c.wsClient.frames.length;
  c.stop();
  const frames = c.wsClient.frames;
  assert.strictEqual(frames.length, framesBefore + 1, 'padded tail shipped as final frame');
  const last = frames[frames.length - 1];
  assert.strictEqual(last.bytes.length, 640, 'final chunk zero-padded to 640 bytes');
  // The padded tail is zero beyond sample 110: bytes 220..639 all zero.
  let zeros = true;
  for (let i = 220; i < 640; i++) if (last.bytes[i] !== 0) zeros = false;
  assert.ok(zeros, 'padding region is zero-filled');
  assert.strictEqual(last.seq, 3, 'sequence continues monotonically');
  assert.strictEqual(c.wsClient.sends.length, 1, 'voice_command trigger sent exactly once');
  assert.deepStrictEqual(c.wsClient.sends[0], { type: 'voice_command', params: {} });
  assert.strictEqual(c.isRecording, false, 'recording stopped');
  assert.strictEqual(c.isProcessing, true, 'processing lock held until voice_result');
}

// ---------------------------------------------------------------------
// 5. Tail continuity across processor buffers (no dropped/dupe samples)
// ---------------------------------------------------------------------
{
  const c = makeController(44100);
  c._handleAudio(new Float32Array(4096).fill(0.25));
  c._handleAudio(new Float32Array(4096).fill(0.25));
  const total = c.wsClient.frames.length * 320 +
    c._chunkQueue.reduce((n, ch) => n + ch.length, 0) + c._tail.length;
  assert.strictEqual(total, Math.floor(8192 / (44100 / 16000)),
    '8192@44.1k → floor(8192/2.75625)=2972@16k, boundary-spanning interpolation intact');
}

// ---------------------------------------------------------------------
// 6. Overflow: cap stops shipping, stop() reports the error instead of
//    triggering ASR, and buffers reset
// ---------------------------------------------------------------------
{
  const c = makeController(16000);
  // MAX_RECORDING_BYTES = 327680 → 163840 samples; feed well past it.
  const big = new Float32Array(640 * 40); // 25600 samples per buffer
  while (!c._overflowed) c._handleAudio(big);
  assert.ok(c._overflowed, 'overflow flagged at cap');
  assert.strictEqual(c._chunkQueue.length, 0, 'queue dropped at overflow');
  c.stop();
  assert.strictEqual(c.wsClient.sends.length, 0, 'no voice_command after overflow');
  assert.strictEqual(c.isProcessing, false, 'processing lock not held');
  // Buffers reset: a fresh recording starts from sequence 1 again.
  c.isRecording = true;
  c._handleAudio(new Float32Array(320));
  assert.strictEqual(c.wsClient.frames[c.wsClient.frames.length - 1].seq, 1,
    'sequence counter reset for the next recording');
}

// ---------------------------------------------------------------------
// 7. cancel(): no trigger, everything released
// ---------------------------------------------------------------------
{
  const c = makeController(16000);
  c._handleAudio(new Float32Array(320));
  c.cancel();
  assert.strictEqual(c.wsClient.sends.length, 0, 'cancel does not trigger ASR');
  assert.strictEqual(c.isRecording, false);
  assert.strictEqual(c.isProcessing, false);
  assert.strictEqual(c._tail.length, 0);
  assert.strictEqual(c._chunkQueue.length, 0);
}

// ---------------------------------------------------------------------
// 8. resultReceived(): clears the processing lock (mic re-enables)
// ---------------------------------------------------------------------
{
  const c = makeController(16000);
  c._handleAudio(new Float32Array(320));
  c.stop();
  assert.strictEqual(c.isProcessing, true);
  c.resultReceived();
  assert.strictEqual(c.isProcessing, false);
}

console.log('test-voice-command-controller: all assertions passed');
