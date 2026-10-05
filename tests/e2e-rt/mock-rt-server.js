#!/usr/bin/env node
/**
 * tests/e2e-rt/mock-rt-server.js — mock /rt server + static host for the
 * tutor-page e2e (Phase 13, Plan 05 → rewritten for 13-11).
 *
 * One node process, one port (default 8099):
 *   - HTTP static file server rooted at pkg/web/static, so the page loads
 *     from /tutor/ with EXACTLY the production URL shape (relative js/ and
 *     styles/ paths included). .js/.css carry explicit MIME types — Chromium
 *     refuses to execute a script served without a JavaScript MIME type
 *     (same lesson as pkg/infra/static.go).
 *   - WebSocket /rt endpoint implementing the dictation-classroom flow of
 *     the 2026-10-03 pivot (pkg/rt/protocol.go is the reference; 13-08
 *     SUMMARY is the FSM behavior this mirrors; this file imports no Go
 *     artifacts — conformance is asserted by rt-loop.spec.js).
 *
 * Script (default): session_init validated → session_ready (3-word
 * LessonView, field-for-field) → state(listening) + opening announcement
 * (turn_start → teacher_sentence → audio_start → PCM chunks → audio_end).
 * A mock-side timer (?mock_step_ms=, default 1500) then simulates one
 * student confirmation per tick: lesson_update(version+1, index+1) + the
 * next word's announcement. After the 3rd confirmation the wrap-up summary
 * is announced and session_ended(reason=lesson_complete) is gated on the
 * summary's MATCHING sentence_ack(epoch, sentence_id), followed by a normal
 * WebSocket close. Interrupt keeps the old semantics: epoch+1, interrupt_ack,
 * pending timers of the cancelled announcement dropped, current word
 * re-announced under the new epoch (the lesson still completes).
 *
 * Query-string script selectors (read from the /rt UPGRADE request; the
 * page forwards its location.search onto the socket URL, the real Go mux
 * matches /rt by path and ignores the query):
 *   ?mock_step_ms=N     progression tick interval (default 1500)
 *   ?mock_pause=1       after the opening: pause notice → state(paused);
 *                       stays paused until the __test_resume control frame
 *                       (test-only channel — voice resume is 13-12/real
 *                       backend territory) → state(listening) → progress
 *   ?mock_empty=1       zero due words: session_ready with words:[] →
 *                       no-due explainer sentence → session_ended gated on
 *                       its matching ack → reason=no_due_words
 *   ?mock_ack_timeout=1 deterministic degraded order: the summary audio is
 *                       fully scheduled (6 s) but the server "stops waiting"
 *                       ~800 ms after audio_end → ErrorMsg(code=
 *                       playback_ack_timeout, recoverable=true) →
 *                       SessionEndedMsg(reason=lesson_complete — the
 *                       ORIGINAL natural reason; the timeout code is NEVER
 *                       a reason) → normal close, while sources are still
 *                       queued client-side
 *
 * Unknown/wrong-epoch/duplicate sentence_acks are recorded as REJECTED
 * observations and never end the session (T-13-24: a forged or stale ack
 * must not become completion evidence). Test introspection: GET
 * /__test/state returns every session's recorded state keyed by session_id.
 * The mock only proves FRONTEND protocol behavior; 13-12 must re-verify the
 * same ordering against the real backend.
 *
 * Usage: node tests/e2e-rt/mock-rt-server.js [--port 8099]
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
const STATIC_ROOT = path.resolve(__dirname, '..', '..', 'pkg', 'web', 'static');

// Protocol constants (mirror pkg/rt/protocol.go).
const TTS_RATE = 24000;
const WORDS = ['apple', 'banana', 'cat'];
const LESSON_ID = 'mock-lesson-13-11';

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
      mockMode: s.mockMode,
      epoch: s.epoch,
      turn: s.turn,
      words: s.words,
      version: s.version,
      currentIndex: s.currentIndex,
      lessonUpdates: s.lessonUpdates,
      lessonCompleteSent: s.lessonCompleteSent,
      noDueSent: s.noDueSent,
      pauseSent: s.pauseSent,
      resumed: s.resumed,
      audioFrames: s.audioFrames,
      interrupts: s.interrupts,
      acks: s.acks,
      acksRejected: s.acksRejected,
      summaryAcked: s.summaryAcked,
      terminalOrder: s.terminalOrder,
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

// Announcement chunk plans. Word/opening/notice sentences are short; the
// ack-timeout summary is LONG on purpose: the close must arrive while
// client-side sources are still scheduled (plan: 保留足够长的排程音频).
const PLAN_WORD = { chunks: 8, chunkGapMs: 90, firstDelayMs: 30 };
const PLAN_NOTICE = { chunks: 8, chunkGapMs: 90, firstDelayMs: 30 };
const PLAN_SUMMARY = { chunks: 20, chunkGapMs: 90, firstDelayMs: 30 };
const PLAN_SUMMARY_TIMEOUT = { chunks: 60, chunkGapMs: 20, firstDelayMs: 20 };

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
  let query = new URLSearchParams();
  try {
    const u = new URL(req.url, 'http://mock');
    pathname = u.pathname;
    query = u.searchParams;
  } catch (urlErr) { /* leave '/' → rejected below */ }
  if (pathname === '/rt') {
    wss.handleUpgrade(req, socket, head, (ws) => wss.emit('connection', ws, req, query));
  } else {
    socket.destroy();
  }
});

wss.on('connection', (ws, req, query) => {
  const mockMode = {
    stepMs: Math.max(50, parseInt(query.get('mock_step_ms') || '1500', 10) || 1500),
    pause: query.get('mock_pause') === '1',
    empty: query.get('mock_empty') === '1',
    ackTimeout: query.get('mock_ack_timeout') === '1'
  };
  mockMode.name = mockMode.ackTimeout ? 'ack_timeout'
    : mockMode.empty ? 'empty'
      : mockMode.pause ? 'pause' : 'default';

  const state = {
    id: 'mock-' + Date.now().toString(36) + '-' + (++sessionCounter),
    ready: false,
    mockMode: mockMode.name,
    epoch: 1,
    turn: 0,
    words: mockMode.empty ? [] : WORDS.slice(),
    version: 1,
    currentIndex: 0,
    lessonUpdates: 0,
    lessonCompleteSent: false,
    noDueSent: false,
    pauseSent: false,
    resumed: false,
    audioFrames: 0,
    interrupts: [],
    acks: [],
    acksRejected: [],
    summaryAcked: false,
    terminalOrder: [],
    endReceived: false,
    sessionEndedSent: false,
    started: false,
    ended: false,
    pauseActive: false,
    /** epoch:sentence_id → {epoch, sentence_id, acked} */
    announced: new Map(),
    /** {reason, epoch, sentence_id} | null — natural end waiting on its ack */
    pendingTerminal: null,
    confirmTimerArmed: false,
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
    const id = setTimeout(() => {
      state.timers.delete(id);
      if (!state.ended) fn();
    }, ms);
    state.timers.add(id);
    return id;
  }

  // Terminal close: fires even though the session is already `ended` (the
  // plain `later` guard exists to stop classroom chatter, not the goodbye).
  function closeSoon() {
    setTimeout(() => {
      state.terminalOrder.push('close');
      try { ws.close(); } catch (closeErr) { /* gone */ }
    }, 50);
  }

  function cancelPending() {
    for (const id of state.timers) clearTimeout(id);
    state.timers.clear();
    state.confirmTimerArmed = false;
  }

  // One scripted announcement: subtitle-first, then chunked PCM, then
  // audio_end. Registers the (epoch, sentence_id) entry for ack gating.
  // `done` fires after audio_end is on the wire (unless cancelled).
  function announce(sid, text, plan, chunkBase, done) {
    if (state.ended) return;
    const epoch = state.epoch;
    state.turn += 1;
    const turnId = state.turn;
    state.announced.set(epoch + ':' + sid, { epoch, sentence_id: sid, acked: false });

    send({ type: 'turn_start', turn_id: turnId, epoch });
    send({ type: 'state', value: 'speaking' });
    send({ type: 'teacher_sentence', turn_id: turnId, epoch, sentence_id: sid, text });
    send({ type: 'audio_start', turn_id: turnId, epoch, sentence_id: sid });
    for (let c = 0; c < plan.chunks; c++) {
      later(plan.firstDelayMs + c * plan.chunkGapMs, () => sendBinary(sineChunk(chunkBase + c)));
    }
    later(plan.firstDelayMs + (plan.chunks - 1) * plan.chunkGapMs + 60, () => {
      send({ type: 'audio_end', turn_id: turnId, epoch, sentence_id: sid });
      if (done) done(epoch, sid);
    });
  }

  function armConfirmTimer() {
    if (state.ended || state.confirmTimerArmed) return;
    state.confirmTimerArmed = true;
    later(mockMode.stepMs, () => {
      state.confirmTimerArmed = false;
      confirmAndAdvance();
    });
  }

  // One simulated student confirmation: advance the cursor, publish the
  // lesson_update, then announce the next word — or, at the end of the
  // word list, the wrap-up summary (ack-gated / timeout-scripted).
  function confirmAndAdvance() {
    if (state.ended || state.pauseActive) return;
    state.currentIndex += 1;
    state.version += 1;
    state.lessonUpdates += 1;
    send({
      type: 'lesson_update', lesson_id: LESSON_ID, version: state.version,
      current_index: state.currentIndex, request_id: 'advance-' + state.currentIndex
    });
    send({ type: 'state', value: 'listening' });
    if (state.currentIndex >= state.words.length) {
      announceSummary();
    } else {
      announce('w' + (state.currentIndex + 1),
        '第' + (state.currentIndex + 1) + '个单词：' + state.words[state.currentIndex],
        PLAN_WORD, (state.currentIndex + 1) * 100,
        () => { send({ type: 'state', value: 'listening' }); armConfirmTimer(); });
    }
  }

  function announceSummary() {
    const plan = mockMode.ackTimeout ? PLAN_SUMMARY_TIMEOUT : PLAN_SUMMARY;
    const text = '今天的默写课堂结束啦，' + state.words.length +
      '个单词都完成了，我们下次再见。';
    announce('sum', text, plan, 900, (epoch, sid) => {
      state.lessonCompleteSent = true;
      if (mockMode.ackTimeout) {
        // Deterministic degraded order (13-07 protocol godoc): the server
        // finished SENDING the summary but stops waiting BEFORE any
        // real playback completion — ErrorMsg first, THEN the original
        // natural reason, THEN a normal close. The timeout code is never
        // filled into the reason.
        later(800, () => {
          state.terminalOrder.push('timeout_error');
          send({
            type: 'error', code: 'playback_ack_timeout',
            message: 'mock: bounded playback-ack wait expired', recoverable: true
          });
          state.ended = true;
          state.sessionEndedSent = true;
          state.terminalOrder.push('session_ended');
          send({ type: 'session_ended', reason: 'lesson_complete' });
          closeSoon();
        });
      } else {
        // Natural ending: gate session_ended on the summary's MATCHING ack.
        state.pendingTerminal = { reason: 'lesson_complete', epoch, sentence_id: sid };
      }
    });
  }

  function fireNaturalEnd(reason) {
    if (state.ended) return;
    state.ended = true;
    state.sessionEndedSent = true;
    state.terminalOrder.push('session_ended');
    if (reason === 'lesson_complete') state.lessonCompleteSent = true;
    if (reason === 'no_due_words') state.noDueSent = true;
    send({ type: 'session_ended', reason });
    closeSoon();
  }

  function startSessionFlow() {
    if (state.started || state.ended) return;
    state.started = true;
    send({ type: 'state', value: 'listening' });
    if (mockMode.empty) {
      // Zero due words: the explainer sentence, then reason=no_due_words
      // strictly after that sentence's matching ack.
      announce('nodue', '今天没有到期单词，休息一天，明天再来吧。', PLAN_NOTICE, 500, (epoch, sid) => {
        state.pendingTerminal = { reason: 'no_due_words', epoch, sentence_id: sid };
      });
      return;
    }
    // Opening: the first word announcement IS the greeting (13-08: zero LLM,
    // template wording — the mock mirrors the shape, not the model).
    announce('w1', '我们开始默写课堂，第一个单词：' + state.words[0], PLAN_WORD, 100, () => {
      send({ type: 'state', value: 'listening' });
      if (mockMode.pause) {
        // Pause path: confirmation speech plays, THEN paused — and it
        // STAYS paused until the test-only resume frame arrives.
        announce('p1', '好的，我们先休息一下。', PLAN_NOTICE, 200, () => {
          state.pauseSent = true;
          state.pauseActive = true;
          send({ type: 'state', value: 'paused' });
        });
        return;
      }
      armConfirmTimer();
    });
  }

  ws.on('message', (data, isBinary) => {
    if (isBinary) {
      state.audioFrames += 1;
      if (state.ready && !state.started && !state.ended) startSessionFlow();
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
        // LessonView field-for-field (id / version / current_index / words).
        send({
          type: 'session_ready',
          session_id: state.id,
          audio: { sampleRate: 16000, format: 's16le', channels: 1 },
          tts: { sampleRate: TTS_RATE },
          lesson: { id: LESSON_ID, version: state.version, current_index: state.currentIndex, words: state.words }
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
        cancelPending(); // stop delivering the cancelled announcement
        const oldEpoch = state.epoch;
        state.epoch += 1;
        send({
          type: 'interrupt_ack',
          old_epoch: oldEpoch,
          new_epoch: state.epoch,
          last_sentence_id: msg.last_sentence_id || '',
          played_until_ms: msg.played_until_ms || 0
        });
        // Resume the classroom at the CURRENT cursor under the new epoch —
        // an interrupt never skips or advances a word.
        later(300, () => {
          if (state.ended || state.pauseActive) return;
          if (state.currentIndex === 0) {
            announce('w1', '我们继续，第一个单词：' + state.words[0], PLAN_WORD, 100, () => {
              send({ type: 'state', value: 'listening' });
              armConfirmTimer();
            });
          } else if (state.currentIndex >= state.words.length) {
            announceSummary();
          } else {
            announce('w' + (state.currentIndex + 1),
              '我们继续，第' + (state.currentIndex + 1) + '个单词：' + state.words[state.currentIndex],
              PLAN_WORD, (state.currentIndex + 1) * 100,
              () => { send({ type: 'state', value: 'listening' }); armConfirmTimer(); });
          }
        });
        break;
      }

      case 'sentence_ack': {
        const key = msg.epoch + ':' + msg.sentence_id;
        const entry = state.announced.get(key);
        if (!entry) {
          let reason = 'unknown_sentence_id';
          for (const k of state.announced.keys()) {
            if (k.endsWith(':' + msg.sentence_id)) { reason = 'stale_epoch'; break; }
          }
          state.acksRejected.push({ sentence_id: msg.sentence_id, epoch: msg.epoch, reason });
          break;
        }
        if (entry.acked) {
          state.acksRejected.push({ sentence_id: msg.sentence_id, epoch: msg.epoch, reason: 'duplicate' });
          break;
        }
        entry.acked = true;
        state.acks.push({ sentence_id: msg.sentence_id, epoch: msg.epoch });
        const pending = state.pendingTerminal;
        if (pending && pending.epoch === msg.epoch && pending.sentence_id === msg.sentence_id) {
          state.pendingTerminal = null;
          state.summaryAcked = true;
          state.terminalOrder.push('summary_ack');
          fireNaturalEnd(pending.reason);
        }
        break;
      }

      case '__test_resume': {
        // Test-only channel (NOT a protocol type): mock stand-in for the
        // voice "continue" command. 13-12 proves real voice resume against
        // the real backend; the mock only proves the UI state machine.
        if (state.pauseActive) {
          state.pauseActive = false;
          state.resumed = true;
          send({ type: 'state', value: 'listening' });
          armConfirmTimer();
        }
        break;
      }

      case 'end_session': {
        if (state.ended) return;
        state.endReceived = true;
        cancelPending();
        send({ type: 'session_ended', reason: 'client_end' });
        state.ended = true;
        state.sessionEndedSent = true;
        closeSoon();
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
    ' (static root ' + STATIC_ROOT + ', dictation flow 13-11, /rt ws endpoint,' +
    ' script selectors ?mock_step_ms/?mock_pause/?mock_empty/?mock_ack_timeout,' +
    ' /__test/state introspection)');
});
