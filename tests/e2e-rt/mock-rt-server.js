#!/usr/bin/env node
/**
 * tests/e2e-rt/mock-rt-server.js — mock /rt server + static host for the
 * tutor-page e2e (Phase 13, Plan 05, Task 3).
 *
 * One node process, one port (default 8099):
 *   - HTTP static file server rooted at pkg/web/static, so the page loads
 *     from /tutor/ with EXACTLY the production URL shape (relative js/ and
 *     styles/ paths included). .js/.css carry explicit MIME types — Chromium
 *     refuses to execute a script served without a JavaScript MIME type
 *     (same lesson as pkg/infra/static.go).
 *   - WebSocket /rt endpoint implementing the 13-03 protocol
 *     (pkg/rt/protocol.go is the reference; this file imports no Go
 *     artifacts — conformance is asserted by rt-loop.spec.js).
 *
 * Harness loop: after session_init is validated and the FIRST binary audio
 * frame arrives, the server drives ROUNDS scripted turns (turn_start →
 * transcript → teacher_sentence → audio_start → 6 sine PCM chunks →
 * audio_end → state → lesson_update), 300 ms between turns. It answers
 * ping with pong (t_client_ms echo + process.hrtime.bigint()), handles
 * interrupt (records, replies interrupt_ack with epoch+1, cancels the
 * cancelled round's pending chunks, continues the loop with the new
 * epoch), records sentence_ack, and answers end_session with
 * session_ended. Test introspection: GET /__test/state returns every
 * session's recorded state keyed by session_id.
 *
 * Usage: node tests/e2e-rt/mock-rt-server.js [--port 8099] [--rounds 10]
 */
'use strict';

const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const { WebSocketServer } = require('ws');

// ---------------------------------------------------------------------
// CLI
// ---------------------------------------------------------------------

function argValue(name, fallback) {
  const argv = process.argv.slice(2);
  const idx = argv.indexOf('--' + name);
  if (idx >= 0 && idx + 1 < argv.length) return argv[idx + 1];
  return fallback;
}
const PORT = parseInt(argValue('port', '8099'), 10);
const ROUNDS = parseInt(argValue('rounds', '10'), 10);
const STATIC_ROOT = path.resolve(__dirname, '..', '..', 'pkg', 'web', 'static');

// Protocol constants (mirror pkg/rt/protocol.go).
const TTS_RATE = 24000;
const WORDS = ['apple', 'banana', 'cat'];
const LESSON_ID = 'mock-lesson-13-05';

// ---------------------------------------------------------------------
// Static file serving (production URL shape: /tutor/ + relative assets)
// ---------------------------------------------------------------------

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.htm': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.ico': 'image/x-icon'
};

function serveStatic(req, res) {
  let urlPath;
  try {
    urlPath = decodeURIComponent(new URL(req.url, 'http://mock').pathname);
  } catch (urlErr) {
    res.writeHead(400).end('bad url');
    return;
  }
  if (urlPath === '/') urlPath = '/index.html';
  if (urlPath.endsWith('/')) urlPath += 'index.html';

  const filePath = path.resolve(STATIC_ROOT, '.' + path.posix.normalize('/' + urlPath));
  // Path-traversal containment (threat T-08-03 mirror).
  if (filePath !== STATIC_ROOT && !filePath.startsWith(STATIC_ROOT + path.sep)) {
    res.writeHead(403).end('forbidden');
    return;
  }

  fs.stat(filePath, (statErr, st) => {
    if (statErr || !st.isFile()) {
      res.writeHead(404).end('not found: ' + urlPath);
      return;
    }
    const type = MIME[path.extname(filePath).toLowerCase()] || 'application/octet-stream';
    res.writeHead(200, {
      'Content-Type': type,
      'Content-Length': st.size,
      // HTML entry documents must not cache aggressively (static.go parity);
      // assets keep default ETag/Last-Modified behavior.
      ...(urlPath.endsWith('.html') ? { 'Cache-Control': 'no-cache, must-revalidate' } : {})
    });
    fs.createReadStream(filePath).pipe(res);
  });
}

// ---------------------------------------------------------------------
// Introspection endpoint for rt-loop.spec.js
// ---------------------------------------------------------------------

/** @type {Map<string, Object>} session_id → recorded state */
const sessions = new Map();
let sessionCounter = 0;

function handleTestState(res) {
  const out = {};
  for (const [id, s] of sessions) {
    out[id] = {
      ready: s.ready,
      epoch: s.epoch,
      turn: s.turn,
      roundsStarted: s.roundsStarted,
      roundsBudget: ROUNDS,
      audioFrames: s.audioFrames,
      interrupts: s.interrupts,
      sentenceAcks: s.sentenceAcks,
      endReceived: s.endReceived,
      sessionEndedSent: s.sessionEndedSent
    };
  }
  const body = JSON.stringify({ sessions: out });
  res.writeHead(200, { 'Content-Type': 'application/json; charset=utf-8' });
  res.end(body);
}

// ---------------------------------------------------------------------
// Scripted PCM: 100 ms 440 Hz sine chunks at 24 kHz s16le mono
// ---------------------------------------------------------------------

const SAMPLES_PER_CHUNK = TTS_RATE / 10; // 2400 samples = 100 ms

function sineChunk(chunkIndex) {
  const buf = Buffer.alloc(SAMPLES_PER_CHUNK * 2);
  for (let i = 0; i < SAMPLES_PER_CHUNK; i++) {
    const t = (chunkIndex * SAMPLES_PER_CHUNK + i) / TTS_RATE;
    let v = Math.round(0.5 * 32767 * Math.sin(2 * Math.PI * 440 * t));
    if (v > 32767) v = 32767;
    if (v < -32768) v = -32768;
    buf.writeInt16LE(v, i * 2);
  }
  return buf;
}

// ---------------------------------------------------------------------
// /rt session state machine
// ---------------------------------------------------------------------

const server = http.createServer((req, res) => {
  if (req.url && req.url.split('?')[0] === '/__test/state') {
    handleTestState(res);
    return;
  }
  serveStatic(req, res);
});

const wss = new WebSocketServer({ noServer: true });

server.on('upgrade', (req, socket, head) => {
  let pathname = '/';
  try {
    pathname = new URL(req.url, 'http://mock').pathname;
  } catch (urlErr) { /* leave '/' → rejected below */ }
  if (pathname === '/rt') {
    wss.handleUpgrade(req, socket, head, (ws) => wss.emit('connection', ws, req));
  } else {
    socket.destroy();
  }
});

wss.on('connection', (ws) => {
  const state = {
    id: 'mock-' + Date.now().toString(36) + '-' + (++sessionCounter),
    ready: false,
    epoch: 1,
    turn: 0,
    roundsStarted: 0,
    audioFrames: 0,
    interrupts: [],
    sentenceAcks: [],
    endReceived: false,
    sessionEndedSent: false,
    loopStarted: false,
    timers: new Set()
  };
  sessions.set(state.id, state);

  function send(msg) {
    if (ws.readyState !== ws.OPEN) return;
    try { ws.send(JSON.stringify(msg)); } catch (sendErr) { /* closing */ }
  }

  function sendBinary(buf) {
    if (ws.readyState !== ws.OPEN) return;
    try { ws.send(buf); } catch (sendErr) { /* closing */ }
  }

  function later(ms, fn) {
    const id = setTimeout(() => { state.timers.delete(id); fn(); }, ms);
    state.timers.add(id);
  }

  function cancelPending() {
    for (const id of state.timers) clearTimeout(id);
    state.timers.clear();
  }

  function wordFor(round) { return WORDS[(round - 1) % WORDS.length]; }

  // One scripted turn (see file header). `round` is 1-based.
  function startRound(round) {
    if (state.ended || round > ROUNDS) return;
    state.roundsStarted = round;
    state.turn = round;
    const epoch = state.epoch;
    const sid = 's' + round;
    const chunkBase = round * 100; // distinct sine phase per round

    send({ type: 'turn_start', turn_id: round, epoch });
    send({ type: 'state', value: 'processing' });
    later(40, () => send({ type: 'transcript', turn_id: round, text: '（第' + round + '轮）学生念了一遍' }));

    later(90, () => {
      send({
        type: 'teacher_sentence', turn_id: round, epoch,
        sentence_id: sid, text: '第' + round + '句：请听写 ' + wordFor(round)
      });
      send({ type: 'audio_start', turn_id: round, epoch, sentence_id: sid });
      send({ type: 'state', value: 'speaking' });
      for (let c = 0; c < 6; c++) {
        later(30 + c * 90, () => sendBinary(sineChunk(chunkBase + c)));
      }
      later(30 + 6 * 90 + 60, () => {
        send({ type: 'audio_end', turn_id: round, epoch, sentence_id: sid });
        send({ type: 'state', value: 'listening' });
        send({
          type: 'lesson_update', lesson_id: LESSON_ID, version: 1 + round,
          current_index: (round - 1) % WORDS.length, request_id: 'req-' + round
        });
        later(300, () => startRound(round + 1));
      });
    });
  }

  ws.on('message', (data, isBinary) => {
    if (isBinary) {
      state.audioFrames += 1;
      if (state.ready && !state.loopStarted && !state.ended) {
        state.loopStarted = true;
        later(100, () => startRound(1));
      }
      return;
    }

    let msg;
    try {
      msg = JSON.parse(data.toString('utf8'));
    } catch (parseErr) {
      send({ type: 'error', code: 'invalid_message', message: 'mock: unparseable control frame', recoverable: true });
      return;
    }

    switch (msg.type) {
      case 'session_init': {
        const a = msg.audio || {};
        if (a.sampleRate !== 16000 || a.format !== 's16le' || a.channels !== 1) {
          // ValidateAudioInit parity: only 16000/s16le/1ch.
          send({
            type: 'error', code: 'unsupported_format',
            message: 'mock: got ' + a.sampleRate + '/' + a.format + '/' + a.channels + ', want 16000/s16le/1ch',
            recoverable: false
          });
          return;
        }
        state.ready = true;
        send({
          type: 'session_ready',
          session_id: state.id,
          audio: { sampleRate: 16000, format: 's16le', channels: 1 },
          tts: { sampleRate: TTS_RATE },
          lesson: { id: LESSON_ID, version: 1, current_index: 0, words: WORDS }
        });
        break;
      }

      case 'ping': {
        // PongMsg parity: echo t_client_ms verbatim + server monotonic ns.
        send({
          type: 'pong',
          t_client_ms: msg.t_client_ms,
          t_server_mono_ns: Number(process.hrtime.bigint())
        });
        break;
      }

      case 'interrupt': {
        state.interrupts.push({
          epoch: msg.epoch,
          last_sentence_id: msg.last_sentence_id || '',
          played_until_ms: msg.played_until_ms || 0,
          atTurn: state.turn
        });
        cancelPending(); // stop delivering the cancelled round's audio
        const oldEpoch = state.epoch;
        state.epoch += 1;
        send({
          type: 'interrupt_ack',
          old_epoch: oldEpoch,
          new_epoch: state.epoch,
          last_sentence_id: msg.last_sentence_id || '',
          played_until_ms: msg.played_until_ms || 0
        });
        const next = state.roundsStarted + 1;
        if (next <= ROUNDS && !state.ended) {
          later(300, () => startRound(next));
        }
        break;
      }

      case 'sentence_ack': {
        state.sentenceAcks.push({ sentence_id: msg.sentence_id, epoch: msg.epoch });
        break;
      }

      case 'end_session': {
        if (state.ended) return;
        state.endReceived = true;
        state.ended = true;
        state.sessionEndedSent = true;
        cancelPending();
        send({ type: 'session_ended', reason: 'client_end' });
        setTimeout(() => { try { ws.close(); } catch (closeErr) { /* gone */ } }, 50);
        break;
      }

      default:
        // Unknown/other types are ignored (transport moves bytes; the turn
        // manager's direction rules are out of the mock's scope).
        break;
    }
  });

  ws.on('close', () => cancelPending());
  ws.on('error', () => { /* closed sockets are expected during teardown */ });
});

server.listen(PORT, '127.0.0.1', () => {
  console.log('[mock-rt-server] http+ws on http://127.0.0.1:' + PORT +
    ' (static root ' + STATIC_ROOT + ', ' + ROUNDS + ' rounds, /rt ws endpoint, /__test/state introspection)');
});
