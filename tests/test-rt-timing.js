// tests/test-rt-timing.js — pure-logic contracts for the /rt tutor client
// (Phase 13, Plan 05, Task 1):
//   1. TimingLog.push/toJSONL — every exported line parses as JSON and
//      carries {event, t_ms}; extra fields ride along; entries are ordered.
//   2. Exported lines contain NO server-side fields — the raw
//      t_server_mono_ns stamp never leaves rt-client.js; only the derived
//      offset_ms/rtt_ms pair is recorded (T-13-07 redaction boundary).
//   3. estimateClockOffset — known-value cases under the symmetric RTT/2
//      assumption, including the zero-RTT symmetric case.
//   4. humanizeError / isVoiceCaptureAvailable / deriveRtUrl — the small
//      pure helpers rt-client.js exports for the page.
// Runs on stock Node with no framework and no installs:
// `node tests/test-rt-timing.js`.
'use strict';

const assert = require('node:assert');

// The IIFE assigns onto its `global` arg — in Node's CommonJS that is the
// module's top-level `this` (module.exports). Same pattern as timing.js.
const {
  TimingLog,
  estimateClockOffset,
  humanizeError,
  isVoiceCaptureAvailable,
  deriveRtUrl,
  RT_EVENTS,
  ERROR_MESSAGES,
  UPLINK_AUDIO_SPEC
} = require('../pkg/web/static/tutor/js/rt-client.js');

// ---------------------------------------------------------------------------
// 1. TimingLog.push / toJSONL line format
// ---------------------------------------------------------------------------
{
  const log = new TimingLog();
  assert.strictEqual(log.toJSONL(), '', 'empty log exports zero lines');

  log.push(RT_EVENTS.PAGE_READY);
  log.push(RT_EVENTS.AUTH_COMPLETE, { turn_id: 0 });
  log.push(RT_EVENTS.CONNECTED);
  log.push(RT_EVENTS.FIRST_AUDIO_PLAYED, { turn_id: 1, epoch: 1, sentence_id: 's1' });
  log.push(RT_EVENTS.INTERRUPT_START, { epoch: 2 });
  log.push(RT_EVENTS.INTERRUPT_STOP);
  log.push(RT_EVENTS.CLOCK_OFFSET, { offset_ms: 3.5, rtt_ms: 42 });

  const text = log.toJSONL();
  assert.ok(text.endsWith('\n'), 'JSONL ends with a newline');
  const lines = text.split('\n').filter((l) => l.length > 0);
  assert.strictEqual(lines.length, 7, 'one line per event');
  assert.strictEqual(log.count(RT_EVENTS.CONNECTED), 1);
  assert.strictEqual(log.count('nope'), 0);

  const events = [];
  lines.forEach((line, idx) => {
    let obj;
    assert.doesNotThrow(() => { obj = JSON.parse(line); }, 'line ' + idx + ' parses');
    assert.strictEqual(typeof obj.event, 'string', 'line ' + idx + ' has event');
    assert.strictEqual(typeof obj.t_ms, 'number', 'line ' + idx + ' has t_ms');
    assert.ok(obj.t_ms >= 0, 't_ms is a plausible clock value');
    events.push(obj.event);
  });
  assert.deepStrictEqual(events, [
    RT_EVENTS.PAGE_READY,
    RT_EVENTS.AUTH_COMPLETE,
    RT_EVENTS.CONNECTED,
    RT_EVENTS.FIRST_AUDIO_PLAYED,
    RT_EVENTS.INTERRUPT_START,
    RT_EVENTS.INTERRUPT_STOP,
    RT_EVENTS.CLOCK_OFFSET
  ], 'entries keep push order');

  // Extra fields ride along verbatim.
  const firstAudio = JSON.parse(lines[3]);
  assert.strictEqual(firstAudio.turn_id, 1);
  assert.strictEqual(firstAudio.epoch, 1);
  assert.strictEqual(firstAudio.sentence_id, 's1');

  // push guards against extra clobbering the frozen core fields.
  const guarded = new TimingLog();
  guarded.push('x', { event: 'evil', t_ms: -1 });
  const gLine = JSON.parse(guarded.toJSONL().split('\n')[0]);
  assert.strictEqual(gLine.event, 'x');
  assert.strictEqual(gLine.t_ms >= 0, true);

  // entries() returns a copy — mutation of the log does not alias.
  const snap = log.entries();
  assert.strictEqual(snap.length, 7);
  log.push('later');
  assert.strictEqual(snap.length, 7, 'snapshot detached from log');
}

// ---------------------------------------------------------------------------
// 2. Exported lines contain no server-side fields
// ---------------------------------------------------------------------------
{
  const log = new TimingLog();
  log.push(RT_EVENTS.CLOCK_OFFSET, { offset_ms: -12.25, rtt_ms: 88.5 });
  log.push(RT_EVENTS.FIRST_AUDIO_PLAYED, { turn_id: 3, epoch: 3, sentence_id: 's9' });
  const text = log.toJSONL();
  assert.ok(!text.includes('t_server_mono_ns'), 'raw server monotonic stamp never exported');
  assert.ok(!text.includes('session_id'), 'session identifiers never exported');
  assert.ok(!/"pcm"|samples|transcript/.test(text), 'no audio/transcript payloads in the export');
  const offsetLine = JSON.parse(text.split('\n')[0]);
  assert.strictEqual(offsetLine.offset_ms, -12.25);
  assert.strictEqual(offsetLine.rtt_ms, 88.5);
}

// ---------------------------------------------------------------------------
// 3. estimateClockOffset — symmetric-RTT known values
// ---------------------------------------------------------------------------
{
  // Plan's worked example: t0=1000, t1=1100, server stamp 1060ms →
  // midpoint 1050, offset +10ms, RTT 100ms.
  const a = estimateClockOffset(1000, 1100, 1060_000_000);
  assert.strictEqual(a.offset_ms, 10, 'offset = server - midpoint');
  assert.strictEqual(a.rtt_ms, 100, 'rtt = t1 - t0');

  // Zero-RTT symmetric case: everything at t=2000 → offset 0, rtt 0.
  const b = estimateClockOffset(2000, 2000, 2000_000_000);
  assert.strictEqual(b.offset_ms, 0);
  assert.strictEqual(b.rtt_ms, 0);

  // Server behind the client midpoint by 25ms with 50ms RTT.
  const c = estimateClockOffset(5000, 5050, 5025_000_000 - 25_000_000);
  assert.strictEqual(c.offset_ms, -25);
  assert.strictEqual(c.rtt_ms, 50);

  // Symmetric-RTT assumption: the same server stamp against different
  // midpoints shifts the estimate 1:1 with the midpoint move.
  const d1 = estimateClockOffset(0, 100, 1000_000_000);
  const d2 = estimateClockOffset(50, 150, 1000_000_000);
  assert.strictEqual(d1.offset_ms - d2.offset_ms, 50, 'midpoint shift moves estimate');
  assert.strictEqual(d1.rtt_ms, 100);
  assert.strictEqual(d2.rtt_ms, 100);
}

// ---------------------------------------------------------------------------
// 4. Page helper contracts
// ---------------------------------------------------------------------------
{
  // humanizeError: every vocabulary code resolves to non-empty Chinese text,
  // client-local hyphenated mic types included; unknown codes fall back.
  ['mic-denied', 'mic-unavailable', 'voice_unavailable', 'provider_failed',
    'unsupported_format', 'invalid_message', 'session_closed'].forEach((code) => {
    const msg = humanizeError(code);
    assert.strictEqual(typeof msg, 'string');
    assert.ok(msg.length > 4, code + ' has a human message');
    assert.ok(!/^ *[a-z_]+$/.test(msg), code + ' message is not the raw code');
  });
  assert.strictEqual(humanizeError('made-up-code', '备用说明'), '备用说明');
  assert.ok(humanizeError('made-up-code').includes('made-up-code'));
  assert.strictEqual(Object.keys(ERROR_MESSAGES).length >= 5, true);

  // isVoiceCaptureAvailable: the RT-01 safe-context probe.
  assert.strictEqual(isVoiceCaptureAvailable({ mediaDevices: { getUserMedia: function () {} } }), true);
  assert.strictEqual(isVoiceCaptureAvailable({}), false);
  assert.strictEqual(isVoiceCaptureAvailable(null), false);
  assert.strictEqual(isVoiceCaptureAvailable({ mediaDevices: {} }), false);

  // deriveRtUrl: scheme follows page protocol, path is /rt.
  assert.strictEqual(deriveRtUrl({ protocol: 'https:', host: 'tutor.example.com' }), 'wss://tutor.example.com/rt');
  assert.strictEqual(deriveRtUrl({ protocol: 'http:', host: '127.0.0.1:8099' }), 'ws://127.0.0.1:8099/rt');
  assert.strictEqual(deriveRtUrl(null), null);
  assert.strictEqual(deriveRtUrl({}), null);

  // Uplink spec is frozen to the only configuration ValidateAudioInit accepts.
  assert.deepStrictEqual(UPLINK_AUDIO_SPEC, { sampleRate: 16000, format: 's16le', channels: 1 });
}

console.log('test-rt-timing.js: all assertions passed');
