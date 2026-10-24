// rt-observe.test.js — PR #66 review P2: specs order WS evidence causally
// with e.i comparisons (e.g. "listening settled AFTER the last audio_end"),
// so the page-side log's entry numbers must stay strictly monotonic even
// when the bounded log truncates its prefix. The old push() derived i from
// log.length, so after splice(0, 4000) new entries re-issued numbers the
// retained entries already held (~160 s of 50 Hz uplink silence overflows
// the cap) — a later entry could compare as OLDER than one it follows.
// Run: node --test tests/e2e-m1-browser/helpers/rt-observe.test.js
'use strict';

const assert = require('node:assert/strict');
const vm = require('node:vm');
const { test } = require('node:test');
const { playedBeforeAck, listeningAfterPlayback, RT_OBSERVE_INIT } = require('./rt-observe.js');

// Boot the observer init script in a bare sandbox with a fake NATIVE
// WebSocket: just enough surface for the wrapper to install (constructor,
// send, addEventListener, ready-state statics), no real network. The
// wrapper must still delegate every send to the native socket — the
// observation layer stays read-only.
function bootObserver() {
  class FakeNative {
    constructor(url) { this.url = url; this.sent = []; }
    send(data) { this.sent.push(data); }
    addEventListener() { /* no events in the sandbox */ }
  }
  FakeNative.CONNECTING = 0;
  FakeNative.OPEN = 1;
  FakeNative.CLOSING = 2;
  FakeNative.CLOSED = 3;
  const sandbox = {
    performance: { now: () => 0 },
    window: { WebSocket: FakeNative },
  };
  vm.createContext(sandbox);
  vm.runInContext(RT_OBSERVE_INIT, sandbox);
  return sandbox.window;
}

test('entry numbers stay strictly monotonic across log truncation', () => {
  const win = bootObserver();
  const ws = new win.WebSocket('ws://127.0.0.1:1/rt');
  // Push past the 8000 cap and past a SECOND splice, then some — the
  // region where the old length-derived numbering collapsed.
  const N = 8000 + 4000 + 10;
  for (let k = 0; k < N; k++) ws.send('{"type":"ping"}');
  const log = win.__rtWsLog;
  assert.ok(log.length <= 8001, 'log stays bounded (got ' + log.length + ')');
  // splice drops a PREFIX, so survivors keep their original numbers and
  // every consecutive pair must still differ by exactly +1.
  for (let k = 1; k < log.length; k++) {
    assert.equal(log[k].i, log[k - 1].i + 1,
      'entry #' + k + ' (i=' + log[k].i + ') must follow #' + (k - 1) + ' (i=' + log[k - 1].i + ') by exactly 1');
  }
  assert.equal(log[log.length - 1].i, N - 1, 'youngest entry carries the global counter');
  // Read-only wrapper: every frame really reached the native socket.
  assert.equal(ws.sent.length, N);
});

test('WS counts alone accept missing playback, but natural-playback evidence rejects it', () => {
  const sid = 'opening-1';
  const wire = [
    { dir: 'in', type: 'audio_start', msg: { epoch: 4, sentence_id: sid } },
    { dir: 'in', type: 'audio_end', msg: { epoch: 4, sentence_id: sid } },
    { dir: 'out', type: 'sentence_ack', msg: { epoch: 4, sentence_id: sid } },
  ];
  const oldCountAcceptance = wire.filter((e) => e.type === 'audio_start').length > 0
    && wire.filter((e) => e.type === 'audio_end').length === wire.filter((e) => e.type === 'audio_start').length
    && wire.filter((e) => e.type === 'sentence_ack').length === wire.filter((e) => e.type === 'audio_start').length;

  assert.equal(oldCountAcceptance, true, '旧验收会把完全未播放的句子判为通过');
  assert.equal(playedBeforeAck(['ack:4:' + sid], 4, sid), false,
    '缺少 onended 时新验收必须失败');
  assert.equal(playedBeforeAck(['ack:4:' + sid, 'onended:4:' + sid], 4, sid), false,
    'onended 晚于 ack 时新验收必须失败');
  assert.equal(playedBeforeAck(['onended:4:' + sid, 'ack:4:' + sid], 4, sid), true);
});

test('listening resumes only after every matching playback ACK', () => {
  const event = (i, dir, type, msg) => ({ i, dir, type, msg });
  const ids = ['opening-1', 'opening-2'];
  const prefix = [
    event(1, 'in', 'audio_start', { epoch: 4, sentence_id: ids[0] }),
    event(2, 'in', 'audio_end', { epoch: 4, sentence_id: ids[0] }),
    event(3, 'out', 'sentence_ack', { epoch: 4, sentence_id: ids[0] }),
    event(4, 'in', 'audio_start', { epoch: 4, sentence_id: ids[1] }),
    event(5, 'in', 'audio_end', { epoch: 4, sentence_id: ids[1] }),
  ];
  const listening = (i) => event(i, 'in', 'state', { value: 'listening' });
  const lastAck = event(7, 'out', 'sentence_ack', { epoch: 4, sentence_id: ids[1] });

  assert.equal(listeningAfterPlayback([...prefix, lastAck, listening(8)], 4, ids), true);
  assert.equal(listeningAfterPlayback([...prefix, listening(6), lastAck, listening(8)], 4, ids), false,
    'a later valid resume must not hide listening between audio_end and the final ACK');
  assert.equal(listeningAfterPlayback([...prefix, listening(6), lastAck], 4, ids), false,
    'audio_end is not playback completion');
  assert.equal(listeningAfterPlayback([...prefix, lastAck, listening(8)], 3, ids), false,
    'ACKs from another epoch never satisfy this playback round');
});

// Codex review @1c0620c P2: the old check only rejected listening between
// the round's LAST audio_end and the last ACK, so a resume that happened
// earlier — while the round was still playing — was invisible whenever the
// log later showed one valid post-ACK resume. Both counter-examples below
// returned true against the old implementation.
test("early resume from the round's first audio_start is rejected even with a later valid resume", () => {
  const event = (i, dir, type, msg) => ({ i, dir, type, msg });
  const listening = (i) => event(i, 'in', 'state', { value: 'listening' });
  const start = (i, sid) => event(i, 'in', 'audio_start', { epoch: 4, sentence_id: sid });
  const end = (i, sid) => event(i, 'in', 'audio_end', { epoch: 4, sentence_id: sid });
  const ack = (i, sid) => event(i, 'out', 'sentence_ack', { epoch: 4, sentence_id: sid });

  // 评审反例 1：本轮开始后、唯一句子 audio_end 之前就恢复收听，
  // 最后又出现一次“合法”恢复 —— 旧实现假绿。
  const one = ['opening-1'];
  const earlyMidPlayback = [
    start(1, one[0]), listening(2), end(3, one[0]), ack(4, one[0]), listening(5),
  ];
  assert.equal(listeningAfterPlayback(earlyMidPlayback, 4, one), false,
    'listening before the only audio_end must reject the round');

  // 评审反例 2：多句播报中途（第一句 ack 后、第二句还在播）恢复收听，
  // 最后一句话正常落定 —— 中途的提前收听在最后一个 audio_end 之前，旧实现假绿。
  const ids = ['opening-1', 'opening-2'];
  const midRoundResume = [
    start(1, ids[0]), end(2, ids[0]), ack(3, ids[0]),
    listening(4),
    start(5, ids[1]), end(6, ids[1]), ack(7, ids[1]),
    listening(8),
  ];
  assert.equal(listeningAfterPlayback(midRoundResume, 4, ids), false,
    'a mid-round resume must not be hidden by the later valid one');

  // 边界：本轮首个 audio_start 之前的 listening 属于上一轮的落定，不算提前。
  const previousRoundSettle = [
    listening(1),
    start(2, ids[0]), end(3, ids[0]), ack(4, ids[0]),
    start(5, ids[1]), end(6, ids[1]), ack(7, ids[1]),
    listening(8),
  ];
  assert.equal(listeningAfterPlayback(previousRoundSettle, 4, ids), true,
    "the previous round's settle does not reject this round");

  // 本轮没有 audio_start 证据时起点不可知，不得放行。
  assert.equal(listeningAfterPlayback([end(2, ids[0]), ack(3, ids[0]), listening(4)], 4, [ids[0]]), false,
    'a round without its audio_start cannot be verified');
});
