// rt-observe.js — READ-ONLY WebSocket + playback observation for the m1
// browser line (issue #58, e2e-test-plan §7 step 6). Plan §2's B-layer
// rules forbid the test from sending, mutating or fabricating anything on
// the classroom's behalf — so this helper only WATCHES:
//
//   - installRtObserver(page) plants an init script BEFORE any page script.
//     It wraps window.WebSocket so every frame of every socket the page
//     opens is recorded (direction, kind, parsed control type, byte size).
//     The wrapper always delegates to the REAL socket: the page keeps its
//     production send/onmessage semantics untouched — observation can never
//     alter, drop or inject a frame.
//   - wsView(page) fetches the compact view (all controls + downlink binary
//     sizes; the ~50 Hz uplink-silence frames are dropped from the view —
//     they stay in the page-side log).
//   - waitWs(page, pred) polls that view in NODE until pred(view) holds
//     (plain closures allowed); on timeout it dumps the view tail + the
//     production playback trace for diagnosis.
//   - controls(page) fetches the non-binary entries with text controls
//     parsed into .msg; playbackState(page) reads the production player's
//     ordered trace and dropped-chunk counter.
//
// The log lives on window.__rtWsLog; entries are
//   {i, t, dir:'in'|'out'|'evt', kind:'text'|'binary'|'open'|'close',
//    type?, msg?, text?, bytes?, code?}
// where i is the insertion index (stable under the old-entry cap) and t is
// performance.now() at record time.
'use strict';

const RT_OBSERVE_INIT = String.raw`
(function () {
  'use strict';
  if (window.__rtWsLog) return;
  var log = [];
  var nextIndex = 0;
  window.__rtWsLog = log;
  function push(rec) {
    rec.i = nextIndex++;
    rec.t = performance.now();
    log.push(rec);
    if (log.length > 8000) log.splice(0, 4000); // bounded, index keeps growing
  }
  function byteLen(data) {
    if (data instanceof ArrayBuffer) return data.byteLength;
    if (data && typeof data.byteLength === 'number') return data.byteLength;
    if (data && typeof data.size === 'number') return data.size; // Blob
    return 0;
  }
  function textRec(dir, data) {
    var type = '?';
    var msg = null;
    try {
      msg = JSON.parse(data);
      if (msg && msg.type) type = String(msg.type);
    } catch (e) { /* leave type '?' */ }
    var rec = { dir: dir, kind: 'text', type: type };
    if (msg) rec.msg = msg; else rec.text = data;
    return rec;
  }
  var Native = window.WebSocket;
  if (!Native) return;
  function Wrapped(url, protocols) {
    var ws = protocols === undefined ? new Native(url) : new Native(url, protocols);
    try {
      var nativeSend = ws.send.bind(ws);
      ws.send = function (data) {
        push(typeof data === 'string'
          ? textRec('out', data)
          : { dir: 'out', kind: 'binary', bytes: byteLen(data) });
        return nativeSend(data);
      };
      ws.addEventListener('open', function () { push({ dir: 'evt', kind: 'open' }); });
      ws.addEventListener('close', function (ev) {
        push({ dir: 'evt', kind: 'close', code: ev.code });
      });
      ws.addEventListener('message', function (ev) {
        var data = ev.data;
        push(typeof data === 'string'
          ? textRec('in', data)
          : { dir: 'in', kind: 'binary', bytes: byteLen(data) });
      });
    } catch (e) { /* observation must never break the page */ }
    return ws;
  }
  Wrapped.prototype = Native.prototype;
  ['CONNECTING', 'OPEN', 'CLOSING', 'CLOSED'].forEach(function (k) {
    Wrapped[k] = Native[k];
  });
  window.WebSocket = Wrapped;
})();
`;

/** Plant the observer before any page script runs (once per page). */
async function installRtObserver(page) {
  await page.addInitScript(RT_OBSERVE_INIT);
}

/**
 * Compact log view for node-side assertions: every control/open/close entry
 * plus DOWNLINK binary frames — only the ~50 Hz uplink-silence frames are
 * dropped (they'd dominate the payload; no assertion needs them). Carries
 * each entry's t (performance.now() at record time) so specs can order ws
 * evidence against audio-observe's same-clock timestamps.
 */
async function wsView(page) {
  return page.evaluate(() => (window.__rtWsLog || [])
    .filter((e) => !(e.dir === 'out' && e.kind === 'binary'))
    .map((e) => ({ i: e.i, t: e.t, dir: e.dir, kind: e.kind, type: e.type, msg: e.msg || null, bytes: e.bytes || 0, code: e.code })));
}

/**
 * Poll the log until pred(view) is true. pred runs in NODE over the compact
 * wsView — plain closures allowed, no arg threading; it may return a promise
 * (e.g. polling the stub's control plane). On timeout, throws with the view
 * tail and the production playback trace.
 */
async function waitWs(page, pred, { timeoutMs = 20000, label = 'ws condition' } = {}) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    let ok = false;
    let tail = [];
    try {
      tail = await wsView(page);
      ok = !!(await pred(tail));
    } catch (_) { /* page navigating or pred not yet satisfiable */ }
    if (ok) return;
    if (Date.now() > deadline) {
      const trace = await playbackTrace(page);
      throw new Error(
        label + ' not met within ' + timeoutMs + 'ms\n'
        + 'ws tail: ' + JSON.stringify(tail.slice(-30)) + '\n'
        + 'playback trace tail: ' + JSON.stringify(trace.slice(-15)));
    }
    await page.waitForTimeout(100);
  }
}

/** Non-binary log entries, control JSON parsed into .msg (assertion view). */
async function controls(page) {
  return page.evaluate(() => (window.__rtWsLog || [])
    .filter((e) => e.kind !== 'binary')
    .map((e) => ({ i: e.i, t: e.t, dir: e.dir, kind: e.kind, type: e.type, msg: e.msg || null, code: e.code })));
}

/** The production player's ordered trace (rt-playback.js). */
async function playbackTrace(page) {
  return page.evaluate(() => (window.__rtTutor && window.__rtTutor.playback && Array.isArray(window.__rtTutor.playback.trace))
    ? window.__rtTutor.playback.trace.slice()
    : []);
}

/** Production playback evidence needed by browser acceptance assertions. */
async function playbackState(page) {
  return page.evaluate(() => {
    const playback = window.__rtTutor && window.__rtTutor.playback;
    return {
      trace: playback && Array.isArray(playback.trace) ? playback.trace.slice() : [],
      droppedChunks: playback && Number.isFinite(playback.droppedChunks)
        ? playback.droppedChunks : -1,
    };
  });
}

/** True only when one sentence's real source completion precedes its ACK. */
function playedBeforeAck(trace, epoch, sentenceId) {
  const onended = 'onended:' + epoch + ':' + sentenceId;
  const ack = 'ack:' + epoch + ':' + sentenceId;
  const lastOnended = trace.lastIndexOf(onended);
  const ackAt = trace.indexOf(ack);
  return lastOnended >= 0 && ackAt > lastOnended;
}

/** Aggregate binary-frame counters (uplink freeze/release evidence). */
async function wsStats(page) {
  return page.evaluate(() => {
    const out = { inBinaryFrames: 0, inBinaryBytes: 0, outBinaryFrames: 0, outBinaryBytes: 0 };
    for (const e of window.__rtWsLog || []) {
      if (e.kind !== 'binary') continue;
      const k = e.dir === 'in' ? 'inBinary' : 'outBinary';
      out[k + 'Frames'] += 1;
      out[k + 'Bytes'] += e.bytes || 0;
    }
    return out;
  });
}

module.exports = {
  installRtObserver, wsView, waitWs, controls, playbackTrace, playbackState,
  playedBeforeAck, wsStats, RT_OBSERVE_INIT,
};
