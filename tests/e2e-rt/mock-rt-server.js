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
 *   ?mock_partial_save=1 13.1-01: review_result carries updated=N-1,
 *                       missing=1 so the page's honest-copy branch renders
 *                       「保存了 N-1 / N」 instead of full success
 *
 * 13.1-04 reconnect pairs (?mock_unchecked=1 / ?mock_resume=1, both with
 * ?mock_pair=<id> — the persistent per-pair lesson state lives in THIS
 * process so two page loads reconnect into the SAME lesson):
 *   ?mock_unchecked=1    session 1 dictates normally, the check stage opens
 *                       (check_started reason=dictation_done), and the socket
 *                       is HARD-CLOSED before any verdict — the stand-in for
 *                       the real close-path D-03 fallback (which would
 *                       persist downgraded ratings + the unchecked marker).
 *                       Session 2 (same pair id) reopens the lesson as the
 *                       CORRECTION panel: check_started(reason=correction)
 *                       with the derived fallback verdicts as pending
 *                       (manual=false) rows; check_submit runs the diff —
 *                       only words whose human verdict OVERRIDES the
 *                       derivation ride the second batch (Pitfall 7), and
 *                       review_result carries requested=updated=changed with
 *                       unchecked=false. A THIRD connection after the natural
 *                       end short-circuits (completed lesson, no panel).
 *   ?mock_resume=1       session 1 reaches the check stage and TWO verdicts
 *                       are already applied when the socket dies; session 2
 *                       receives check_started(reason=resume) plus the STORED
 *                       verdicts echo and finishes the check with the normal
 *                       unified submit (full ratings).
 *
 * 13.1-01 three-stage flow (mirrors pkg/rt/protocol.go + pkg/rt/check.go
 * field-for-field): after the last word's confirmation the mock OPENS THE
 * CHECK STAGE instead of summarizing — check_started(full words, reason
 * dictation_done) → bridge teacher_sentence → waits for client
 * check_verdict frames (echoing the authoritative check_verdicts list after
 * each) → on check_submit (gated: every word needs a manual verdict;
 * incomplete → recoverable invalid_message, NO stage transition) →
 * review_result(requested=N, updated=N±, missing) → battle summary
 * teacher_sentence → session_ended(lesson_complete) gated on the summary's
 * MATCHING sentence_ack (13-11 gating). A second check_submit is a REPLAY:
 * recorded, answered with nothing — no second review_result, no second
 * summary (Pitfall 6 / T-13.1-01 mirror).
 *
 * 13.1-02 mode contract (mirrors the Go send-side masking, D-08/D-09/D-10):
 * the DEFAULT (no mode param) is STUDENT mode — session_ready.lesson.words
 * carry text only for already-revealed indexes ("" elsewhere), word-bearing
 * announcement subtitles use the masked 「第N个词」 form, the simulated
 * student transcript masks the target word to •••, and a word_revealed
 * frame rides every lesson_update. ?mode=test mirrors the pre-13.1 full
 * reveal. check_started stays the wholesale unmask point in BOTH modes.
 * Every outbound control frame is recorded in the session's `frames` log
 * (the wire-side no-leak assertion source for rt-loop.spec.js).
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
      mode: s.mode,
      checkActive: s.checkActive,
      checkVerdicts: s.checkVerdicts,
      checkSubmits: s.checkSubmits,
      reviewResults: s.reviewResults,
      checkStartedReason: s.checkStartedReason,
      correctionDiff: s.correctionDiff,
      closedMidCheck: s.closedMidCheck,
      frames: s.frames,
      summaries: s.summaries,
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
// 13.1-04 reconnect pairs: persistent per-pair lesson state (keyed by the
// ?mock_pair= value; each spec test uses its own pair id, and the single
// Playwright worker keeps the map deterministic). phase:
//   dictation → checking (socket dies mid-check) → panel (session 2 reopens)
//   → done (any later connection short-circuits as a completed lesson).
// ---------------------------------------------------------------------

/** @type {Map<string, Object>} pair id → shared lesson state */
const lessonPairs = new Map();

function pairState(pairId) {
  let p = lessonPairs.get(pairId);
  if (!p) {
    p = {
      phase: 'dictation',
      words: WORDS.slice(),
      currentIndex: 0,
      version: 1,
      lessonUpdates: 0,
      mode: 'student',
      /** Stored verdicts (the resume case's echo source). */
      checkVerdicts: [],
      /** Derived fallback verdicts (the correction case's display hints):
          apple/banana confirmed clean → derived correct; cat carried a hint →
          the D-03 fallback rated it again → derived wrong. manual=false: a
          display hint, never a confirmation. */
      derived: WORDS.map((_, i) => ({ word_index: i, correct: i < 2, manual: false })),
      correctionDiff: null,
      closedMidCheck: false
    };
    lessonPairs.set(pairId, p);
  }
  return p;
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
    ackTimeout: query.get('mock_ack_timeout') === '1',
    partialSave: query.get('mock_partial_save') === '1'
  };
  mockMode.name = mockMode.partialSave ? 'partial_save'
    : mockMode.ackTimeout ? 'ack_timeout'
      : mockMode.empty ? 'empty'
        : mockMode.pause ? 'pause' : 'default';

  // 13.1-04 reconnect pair binding (persistent lesson state across page
  // loads; the pair id comes from the spec test).
  const pairId = query.get('mock_pair') || '';
  const wantUnchecked = query.get('mock_unchecked') === '1';
  const wantResume = query.get('mock_resume') === '1';
  const pair = (wantUnchecked || wantResume) && pairId ? pairState(pairId) : null;
  if (pair) mockMode.name = wantResume ? 'resume_pair' : 'unchecked_pair';

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
    mode: null,
    checkActive: false,
    checkVerdicts: [],
    checkSubmits: [],
    reviewResults: 0,
    checkStartedReason: null,
    correctionDiff: null,
    closedMidCheck: false,
    /** 13.1-02: every outbound control frame, as its JSON string (the
        wire-side no-leak assertion source). */
    frames: [],
    summaries: 0,
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
    state.frames.push(JSON.stringify(msg));
    if (ws.readyState !== ws.OPEN) return;
    try { ws.send(JSON.stringify(msg)); } catch (sendErr) { /* closing */ }
  }

  // 13.1-02 mode contract helpers (the server is the masking authority; the
  // page renders only what it was sent).

  /** LessonView words by UI mode: student carries text ONLY for already-
      revealed indexes (D-09); test keeps the full reveal. */
  function lessonWordsForMode() {
    if (state.mode === 'test') return state.words.slice();
    return state.words.map((w, i) => (i < state.currentIndex ? w : ''));
  }

  /** Word-bearing announcement text by UI mode (D-10): student subtitles use
      the masked 「第N个词」 form; test names the word. */
  function wordAnnounceText(prefix, idx) {
    const n = idx + 1;
    if (state.mode === 'test') return prefix + '第' + n + '个单词：' + state.words[idx];
    return prefix + '第' + n + '个词';
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

  // One simulated student confirmation: the student "says" the word (the
  // transcript frame — masked to ••• in student mode, D-10), the cursor
  // advances with lesson_update (+ the student-mode word_revealed reveal,
  // 13.1-02), then the next word is announced — or, at the end of the word
  // list, THE CHECK STAGE OPENS (13.1-01: no immediate summary any more).
  function confirmAndAdvance() {
    if (state.ended || state.pauseActive) return;
    const saidWord = state.words[state.currentIndex];
    const said = state.mode === 'test' ? saidWord + ' 写好了' : '••• 写好了';
    send({ type: 'transcript', turn_id: state.turn + 1, text: said });
    state.currentIndex += 1;
    state.version += 1;
    state.lessonUpdates += 1;
    if (pair) {
      // 13.1-04 pair mirror: the shared lesson state keeps the cursor the
      // reconnect resumes from.
      pair.currentIndex = state.currentIndex;
      pair.version = state.version;
      pair.lessonUpdates = state.lessonUpdates;
    }
    send({
      type: 'lesson_update', lesson_id: LESSON_ID, version: state.version,
      current_index: state.currentIndex, request_id: 'advance-' + state.currentIndex
    });
    if (state.mode !== 'test') {
      // 13.1-02 (D-09): the reveal rides the advance — the chip flips to
      // 已默=显词＋绿勾 (the mock tick IS the first commit).
      send({ type: 'word_revealed', word_index: state.currentIndex - 1, word: saidWord });
    }
    send({ type: 'state', value: 'listening' });
    if (state.currentIndex >= state.words.length) {
      startCheckStage();
    } else {
      announce('w' + (state.currentIndex + 1),
        wordAnnounceText('', state.currentIndex),
        PLAN_WORD, (state.currentIndex + 1) * 100,
        () => { send({ type: 'state', value: 'listening' }); armConfirmTimer(); });
    }
  }

  // 13.1-01: the check stage — check_started (full plaintext list, the
  // unmask point) + a bridge teacher sentence, then wait for client
  // check_verdict frames. 13.1-04 pair scripts: the socket DIES mid-check
  // (the real close-path fallback's stand-in) after optionally applying two
  // verdicts (the resume case).
  function startCheckStage() {
    if (state.checkActive || state.ended) return;
    state.checkActive = true;
    send({ type: 'check_started', words: state.words.slice(), reason: 'dictation_done' });
    announce('chk', '全都写完啦！我们一起检查：屏幕上有' + state.words.length +
      '个词，想一想你写的是对还是错，点「对」或「错」告诉我。', PLAN_NOTICE, 800,
      () => { send({ type: 'state', value: 'listening' }); });
    if (pair) {
      pair.phase = 'checking';
      if (wantResume) {
        // Two verdicts already applied before the disconnect (the student
        // tapped twice): apple correct, banana wrong — the stored state the
        // reconnect must restore verbatim.
        pair.checkVerdicts = [
          { word_index: 0, correct: true, manual: true },
          { word_index: 1, correct: false, manual: true }
        ];
        send({ type: 'check_verdicts', verdicts: pair.checkVerdicts.map((v) => ({ ...v })) });
      }
      later(500, () => {
        pair.phase = 'panel';
        pair.closedMidCheck = true;
        state.closedMidCheck = true; // session-side mirror for introspection
        cancelPending();
        try { ws.terminate(); } catch (termErr) { /* gone */ }
      });
    }
  }

  // The unified-submit battle summary (D-04 mirror): battle counts plus the
  // honest save note when the save was partial.
  function announceSummary(correct, updated, requested) {
    state.summaries += 1;
    const plan = mockMode.ackTimeout ? PLAN_SUMMARY_TIMEOUT : PLAN_SUMMARY;
    let text = '今天' + requested + '个词对了' + correct + '个，你真棒！' +
      '今天的默写课堂结束啦，我们下次再见。';
    if (updated < requested) {
      text = '今天' + requested + '个词对了' + correct + '个。本次保存' + updated +
        '/' + requested + '个。今天的默写课堂结束啦，我们下次再见。';
    }
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
    if (pair) pair.phase = 'done'; // any later connection short-circuits
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
    // template wording — the mock mirrors the shape, not the model). Student
    // mode masks the word to its position (13.1-02, D-10).
    announce('w1', wordAnnounceText('我们开始默写课堂，', 0), PLAN_WORD, 100, () => {
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
        // 13.1 D-08: mirror the page's UI mode (absent → student).
        state.mode = msg.mode === 'test' ? 'test' : 'student';
        if (pair) pair.mode = state.mode;
        if (pair && pair.phase === 'done') {
          // 13.1-04 regression mirror: a COMPLETED lesson reconnects and
          // short-circuits — session_ready + session_ended(lesson_complete),
          // no narration, no check panel (startCompletedSession parity).
          state.currentIndex = pair.currentIndex;
          state.version = pair.version;
          state.lessonUpdates = pair.lessonUpdates;
          state.started = true; // binary uplink must not start the classroom
          send({
            type: 'session_ready',
            session_id: state.id,
            audio: { sampleRate: 16000, format: 's16le', channels: 1 },
            tts: { sampleRate: TTS_RATE },
            lesson: { id: LESSON_ID, version: state.version, current_index: state.currentIndex, words: lessonWordsForMode() }
          });
          state.ended = true;
          state.sessionEndedSent = true;
          state.lessonCompleteSent = true;
          state.terminalOrder.push('session_ended');
          send({ type: 'session_ended', reason: 'lesson_complete' });
          closeSoon();
          break;
        }
        if (pair && pair.phase === 'panel') {
          // 13.1-04 reconnect: the pair reopens the check panel from the
          // SHARED lesson state — resume echoes the STORED verdicts,
          // correction shows the derived fallback hints as pending rows.
          state.currentIndex = pair.currentIndex;
          state.version = pair.version;
          state.lessonUpdates = pair.lessonUpdates;
          state.started = true; // binary uplink must not start the classroom
          state.checkActive = true;
          state.checkStartedReason = wantResume ? 'resume' : 'correction';
          const initial = (wantResume ? pair.checkVerdicts : pair.derived).map((v) => ({ ...v }));
          state.checkVerdicts = initial.map((v) => ({ ...v }));
          send({
            type: 'session_ready',
            session_id: state.id,
            audio: { sampleRate: 16000, format: 's16le', channels: 1 },
            tts: { sampleRate: TTS_RATE },
            lesson: { id: LESSON_ID, version: state.version, current_index: state.currentIndex, words: lessonWordsForMode() }
          });
          send({ type: 'check_started', words: state.words.slice(), reason: state.checkStartedReason });
          send({ type: 'check_verdicts', verdicts: initial });
          const bridge = wantResume
            ? '欢迎回来！我们继续检查刚才那' + state.words.length + '个词，接着点「对」或「错」告诉我。'
            : '欢迎回来！上次没来得及检查，我们现在补上：屏幕上有' + state.words.length +
              '个词，老师先猜了一个，你来点「对」或「错」告诉老师真正的答案吧。';
          announce('chk', bridge, PLAN_NOTICE, 1200,
            () => { send({ type: 'state', value: 'listening' }); });
          break;
        }
        // LessonView field-for-field (id / version / current_index / words).
        send({
          type: 'session_ready',
          session_id: state.id,
          audio: { sampleRate: 16000, format: 's16le', channels: 1 },
          tts: { sampleRate: TTS_RATE },
          lesson: { id: LESSON_ID, version: state.version, current_index: state.currentIndex, words: lessonWordsForMode() }
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
            announce('w1', wordAnnounceText('我们继续，', 0), PLAN_WORD, 100, () => {
              send({ type: 'state', value: 'listening' });
              armConfirmTimer();
            });
          } else if (state.currentIndex >= state.words.length) {
            startCheckStage();
          } else {
            announce('w' + (state.currentIndex + 1),
              wordAnnounceText('我们继续，', state.currentIndex),
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
          state.terminalOrder.push('terminal_ack');
          fireNaturalEnd(pending.reason);
        }
        break;
      }

      case 'check_verdict': {
        // 13.1-01 mirror: one screen verdict — validate, upsert by
        // word_index, echo the FULL authoritative list (RT-07).
        if (!state.checkActive) {
          send({ type: 'error', code: 'invalid_message', message: 'mock: no check stage', recoverable: true });
          break;
        }
        const vi = msg.word_index;
        if (!msg.request_id || typeof vi !== 'number' || vi < 0 || vi >= state.words.length) {
          send({ type: 'error', code: 'invalid_message', message: 'mock: bad check_verdict', recoverable: true });
          break;
        }
        const verdict = { word_index: vi, correct: !!msg.correct, manual: true };
        const found = state.checkVerdicts.find((v) => v.word_index === vi);
        if (found) Object.assign(found, verdict);
        else state.checkVerdicts.push(verdict);
        send({ type: 'check_verdicts', verdicts: state.checkVerdicts.map((v) => ({ ...v })) });
        break;
      }

      case 'check_submit': {
        // 13.1-01 mirror: the unified-submit gate — every word needs a
        // manual verdict (13.1-04: derived pending hints carry manual=false
        // and do NOT satisfy this gate, D-05); the transition latches after
        // ONE review_result.
        if (!state.checkActive) {
          send({ type: 'error', code: 'invalid_message', message: 'mock: no check stage', recoverable: true });
          break;
        }
        state.checkSubmits.push(msg.request_id || '');
        const manualCount = state.checkVerdicts.filter((v) => v.manual).length;
        if (manualCount < state.words.length) {
          send({ type: 'error', code: 'invalid_message', message: '还有词没有检查哦', recoverable: true });
          break;
        }
        if (state.reviewResults > 0) {
          // Replay (network retry or second tap): recorded, answered with
          // NOTHING — no second review_result, no second summary (Pitfall 6).
          break;
        }
        const n = state.words.length;
        const correct = state.checkVerdicts.filter((v) => v.correct).length;
        if (mockMode.name === 'unchecked_pair') {
          // 13.1-04 correction mirror: diff the CONFIRMED verdicts against
          // the derived fallback hints — ONLY overridden words ride the
          // second batch (Pitfall 7), and the result carries the
          // unchecked=false semantics of a checked submission.
          let changed = 0;
          for (let i = 0; i < n; i++) {
            const v = state.checkVerdicts.find((x) => x.word_index === i);
            const d = pair.derived.find((x) => x.word_index === i);
            if (v && d && v.correct !== d.correct) changed += 1;
          }
          state.correctionDiff = changed;
          state.reviewResults += 1;
          send({ type: 'review_result', requested: changed, updated: changed, missing: 0, unchecked: false });
          announceSummary(correct, changed, changed);
          break;
        }
        const updated = mockMode.partialSave ? n - 1 : n;
        const missing = n - updated;
        state.reviewResults += 1;
        send({ type: 'review_result', requested: n, updated, missing, unchecked: false });
        announceSummary(correct, updated, n);
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
