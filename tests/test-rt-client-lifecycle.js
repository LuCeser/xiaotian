// tests/test-rt-client-lifecycle.js — RTCClient connection-lifecycle
// contracts (J14 WS release, PR #72 review P1):
//   1. close() ends the ping cadence with the socket, and a NEW
//      connect+session_ready probes the clock offset again — the cadence
//      latch must reset with the timer, or every connection after the
//      first real close stays pingless forever (the leak used to hide this
//      by never closing the old socket, whose still-running interval kept
//      pinging whatever ws was current).
//   2. connect() supersedes a still-open socket instead of orphaning it:
//      the old ws is really closed, its handlers detached (a superseded
//      socket's close event must never fire the page-level teardown), and
//      the client moves to the fresh socket. The server's read loop only
//      exits when THIS side disconnects, so an overwritten-but-open ws
//      leaks the connection, its server-side session manager and the
//      heartbeat until the page unloads.
// Runs on stock Node with no framework: `node tests/test-rt-client-lifecycle.js`.
'use strict';

const assert = require('node:assert');

// The IIFE assigns onto its `global` arg — in Node's CommonJS that is the
// module's top-level this (module.exports). Same pattern as test-rt-timing.js.
const { RTCClient } = require('../pkg/web/static/tutor/js/rt-client.js');

/** Minimal WebSocket double: born OPEN, records sends, close() is honest. */
function FakeWS(url) {
  this.url = url;
  this.readyState = 1;
  this.sent = [];
}
FakeWS.prototype.send = function (data) { this.sent.push(String(data)); };
FakeWS.prototype.close = function () {
  if (this.readyState !== 0 && this.readyState !== 1) return;
  this.readyState = 3;
  if (typeof this.onclose === 'function') this.onclose({ code: 1000 });
};

/** Drive one connection to its ping cadence: open, then session_ready. */
function sessionReady(ws, id) {
  ws.onopen();
  ws.onmessage({ data: JSON.stringify({ type: 'session_ready', session_id: id }) });
}

function pings(ws) {
  return ws.sent.filter((s) => s.includes('"type":"ping"'));
}

// ---------------------------------------------------------------------------
// 1. Ping cadence is per-connection: close() ends it, the next one restarts
// ---------------------------------------------------------------------------
{
  const client = new RTCClient({ wsFactory: FakeWS });
  try {
    assert.strictEqual(client.connect('ws://test/rt'), true);
    const ws1 = client.ws;
    sessionReady(ws1, 's1');
    assert.ok(pings(ws1).length >= 1, 'session_ready probes the clock immediately');
    assert.strictEqual(ws1.sent.some((s) => s.includes('"type":"session_init"')), true,
      'open sends session_init');

    client.close();
    assert.strictEqual(ws1.readyState, 3, 'close() really closed the socket');

    assert.strictEqual(client.connect('ws://test/rt'), true);
    const ws2 = client.ws;
    assert.notStrictEqual(ws2, ws1, 'connect replaces the socket object');
    sessionReady(ws2, 's2');
    assert.ok(pings(ws2).length >= 1,
      'the NEW connection probes again — _pingStarted reset with the timer');
  } finally {
    client.close(); // never leave the 10 s interval keeping Node alive
  }
}

// ---------------------------------------------------------------------------
// 2. connect() supersedes a live socket: closed, detached, never tears down
//    the session that replaces it
// ---------------------------------------------------------------------------
{
  const teardown = [];
  const client = new RTCClient({ wsFactory: FakeWS });
  client.onClose(function () { teardown.push('close-callback'); });
  try {
    client.connect('ws://test/rt');
    const ws1 = client.ws;
    sessionReady(ws1, 's1');

    // A retry while ws1 is still OPEN (the lesson_unavailable shape: the
    // classroom never started, so no session_ended ever closed the socket).
    client.connect('ws://test/rt');
    assert.strictEqual(ws1.readyState, 3, 'the superseded socket was really closed');
    assert.strictEqual(client.ws === ws1, false, 'the client moved to the fresh socket');
    assert.deepStrictEqual(teardown, [],
      "a superseded socket's close must not fire the page-level teardown");

    const ws2 = client.ws;
    sessionReady(ws2, 's2');
    assert.ok(pings(ws2).length >= 1, 'the replacement connection keeps a live cadence');
    client.close();
    assert.deepStrictEqual(teardown, ['close-callback'],
      'closing the CURRENT socket still surfaces the normal close');
  } finally {
    client.close();
  }
}

console.log('test-rt-client-lifecycle.js: all assertions passed');
