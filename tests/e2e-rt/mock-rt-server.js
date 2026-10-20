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
 *   - 13.3-03: READ-ONLY /api/v1 stubs for the parent learning-record e2e
 *     (GET /api/v1/lessons + /api/v1/lessons/{id}, fixed four-lesson
 *     dataset field-for-field with the 13.3-02 wire structs; unknown ids
 *     answer the handlers.go error envelope with 404 not_found).
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
 * 13.1-07 photo-check script (?mock_ocr=, the 13.1-03 wire contract mirror):
 *   ?mock_ocr=off        no OCR reader server-side — photo_chunk frames are
 *                       answered with the coded photo_unavailable error and
 *                       the check stage keeps working manual-only
 *   ?mock_ocr=<specs>    ';' list of per-upload specs ('wrong2,4' = words 2
 *                       and 4 mis-recognized with plausible near-miss
 *                       readings, 'clean'/'empty' = all correct). Upload N's
 *                       completed photo uses spec N (a canceled upload
 *                       consumes no spec — the retake OCRs only the second
 *                       photo). A completed upload prefills verdicts through
 *                       the same upsert (manual=false), echoes the
 *                       authoritative check_verdicts, then sends ocr_result
 *                       — the applyOcrPrefills frame order, field-for-field.
 *   photo_chunk/photo_cancel frames are handled with the assembler's real
 *   discipline (seq 1 resets, contiguous otherwise, cancel clears
 *   unconditionally and answers nothing).
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
 * carry no answer text during dictation ("" at every index), word-bearing
 * announcement subtitles use the masked 「第N个词」 form, the simulated
 * student transcript masks answers to •••; lesson_update changes positions
 * only. ?mode=test mirrors the pre-13.1 full
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

// 13.1-05 (D-11/D-12): the practice pool the 「再练一组」 flow picks from.
// The scripted already-done set is WORDS (the lesson the student just
// finished) — the mock-side exclusion mirrors pickPracticeWords and is
// deterministic: the first two remaining words, never a random flake.
const PRACTICE_POOL = ['apple', 'banana', 'cat', 'dog', 'egg'];
function practicePick(doneWords) {
  return PRACTICE_POOL.filter((w) => !doneWords.includes(w)).slice(0, 2);
}
let practiceCounter = 0;

// ---------------------------------------------------------------------
// 13.3-03 /api/v1 lessons stubs — READ-ONLY, fixed dataset for the
// parent learning-record e2e. Field names mirror the 13.3-02 wire
// structs FIELD-FOR-FIELD (pkg/web/api/handlers.go lessonListItem /
// lessonDetailResponse / receiptJSON, snake_case) — the mock-parity
// discipline: the stub proves frontend rendering, never server
// behavior. The in-progress lesson (①) keeps its word surfaces
// server-side but NEVER serializes them (the leak guard is the
// summary-only projection); those strings are the spec's negative
// canaries.
// ---------------------------------------------------------------------

const LESSON_STUBS = {
  // ① In-progress dictation (stage='', cursor before the last word):
  // summary-only on the list AND as the detail (the whole response).
  'r0-20261009': {
    summary: {
      lesson_id: 'r0-20261009',
      created_at: '2026-10-09 18:30:00',
      word_count: 3,
      stage: '',
      kind: 'lesson',
      completed: false,
      unchecked: false,
      save_status: 'unknown',
      correct: 0,
      wrong: 0,
      unconfirmed: 0
    },
    words: ['zephyr', 'onyx', 'marina']
  },
  // ② Completed check lesson: saved receipt with real word sets.
  'r0-20261008': {
    summary: {
      lesson_id: 'r0-20261008',
      created_at: '2026-10-08 16:00:00',
      word_count: 3,
      stage: 'submitted',
      kind: 'lesson',
      completed: true,
      unchecked: false,
      save_status: 'saved',
      correct: 2,
      wrong: 1,
      unconfirmed: 0
    },
    words: [
      { word: 'spring', meaning: '春天', example: '' },
      { word: 'summer', meaning: '夏天', example: '' },
      { word: 'winter', meaning: '冬天', example: '' }
    ],
    results: [
      { word: 'spring', correct: true, manual: true, hints: 0, outcome: 'confirmed' },
      { word: 'summer', correct: false, manual: true, hints: 1, outcome: 'timeout', ocr_text: 'sumner' },
      { word: 'winter', correct: true, manual: true, hints: 0 }
    ],
    save_receipts: [
      {
        lesson_id: 'r0-20261008',
        request_id: 'check-stub-20261008',
        kind: 'check',
        requested: ['spring', 'summer', 'winter'],
        updated: ['spring', 'summer', 'winter'],
        missing: [],
        status: 'saved',
        created_at: '2026-10-08 16:05:00',
        completed_at: '2026-10-08 16:05:01'
      }
    ]
  },
  // ③ Unchecked-close fallback lesson: derived verdicts (manual=false →
  // 未确认), pending receipt, completed but flagged 未核对.
  'r0-20261007': {
    summary: {
      lesson_id: 'r0-20261007',
      created_at: '2026-10-07 17:20:00',
      word_count: 2,
      stage: 'submitted',
      kind: 'lesson',
      completed: true,
      unchecked: true,
      save_status: 'pending',
      correct: 0,
      wrong: 0,
      unconfirmed: 2
    },
    words: [
      { word: 'river', meaning: '河流', example: '' },
      { word: 'stone', meaning: '石头', example: '' }
    ],
    results: [
      { word: 'river', correct: true, manual: false, hints: 0 },
      { word: 'stone', correct: false, manual: false, hints: 2 }
    ],
    save_receipts: [
      {
        lesson_id: 'r0-20261007',
        request_id: 'r0-20261007-complete-check',
        kind: 'unchecked_fallback',
        requested: ['river', 'stone'],
        updated: [],
        missing: [],
        status: 'pending',
        created_at: '2026-10-07 17:25:00',
        completed_at: ''
      }
    ]
  },
  // ④ Extra-practice lesson (r0x- namespace): results in the practice
  // shape (word/correct/hints/rating — no manual/outcome/ocr_text) and a
  // practice-kind receipt. Practice lessons carry no word cards, so
  // `words` is empty (the view renders meanings only when they exist).
  'r0x-20261009-101500': {
    summary: {
      lesson_id: 'r0x-20261009-101500',
      created_at: '2026-10-09 10:15:00',
      word_count: 2,
      stage: 'submitted',
      kind: 'practice',
      completed: true,
      unchecked: false,
      save_status: 'saved',
      correct: 1,
      wrong: 1,
      unconfirmed: 0
    },
    words: [],
    results: [
      { word: 'guitar', correct: true, hints: 0, rating: 'good' },
      { word: 'piano', correct: false, hints: 1, rating: 'hard' }
    ],
    save_receipts: [
      {
        lesson_id: 'r0x-20261009-101500',
        request_id: 'practice-stub-20261009',
        kind: 'practice',
        requested: ['guitar', 'piano'],
        updated: ['guitar', 'piano'],
        missing: [],
        status: 'saved',
        created_at: '2026-10-09 10:16:00',
        completed_at: '2026-10-09 10:16:01'
      }
    ]
  }
};

// List order = the real store query's created_at DESC.
const LESSON_LIST_ORDER = ['r0-20261009', 'r0x-20261009-101500', 'r0-20261008', 'r0-20261007'];

function lessonsStubList(res) {
  const body = JSON.stringify({
    lessons: LESSON_LIST_ORDER.map((id) => ({ ...LESSON_STUBS[id].summary }))
  });
  res.writeHead(200, { 'Content-Type': 'application/json; charset=utf-8' });
  res.end(body);
}

function lessonsStubDetail(res, id) {
  const stub = LESSON_STUBS[id];
  if (!stub) {
    // Error envelope parity: handlers.go writeError shape.
    res.writeHead(404, { 'Content-Type': 'application/json; charset=utf-8' });
    res.end(JSON.stringify({ error: { code: 'not_found', message: 'lesson not found' } }));
    return;
  }
  let body;
  if (!stub.summary.completed) {
    // In-progress: the summary IS the whole response — the word-bearing
    // keys do not exist in the serialization at all (13.3-02 leak guard).
    body = { ...stub.summary };
  } else {
    body = {
      ...stub.summary,
      words: stub.words.map((w) => ({ ...w })),
      results: stub.results.map((r) => ({ ...r })),
      save_receipts: stub.save_receipts.map((r) => ({ ...r }))
    };
  }
  res.writeHead(200, { 'Content-Type': 'application/json; charset=utf-8' });
  res.end(JSON.stringify(body));
}

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
      lessonId: s.lessonId,
      version: s.version,
      currentIndex: s.currentIndex,
      lessonUpdates: s.lessonUpdates,
      mode: s.mode,
      practiceInit: s.practiceInit,
      practiceSubmits: s.practiceSubmits,
      practiceCompleteSent: s.practiceCompleteSent,
      checkActive: s.checkActive,
      checkVerdicts: s.checkVerdicts,
      checkSubmits: s.checkSubmits,
      voiceLines: s.voiceLines,
      voiceVerdicts: s.voiceVerdicts,
      photoChunks: s.photoChunks,
      photoUploads: s.photoUploads,
      photoCancels: s.photoCancels,
      photoCanceledMid: s.photoCanceledMid,
      photoUnavailable: s.photoUnavailable,
      photoOutOfStage: s.photoOutOfStage,
      ocrResults: s.ocrResults,
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

// 13.1-06 (D-06②) scripted voice lines: the stand-in classifier maps the
// test-only __test_voice lines to the check-stage intents exactly as the
// real server's check-prompt classification would. The third line has NO
// index on purpose — the missing-payload clarification case (T-13.1-13
// mirror: no guess, no write, ask again).
const VOICE_LINES = {
  '第一个词对了': { intent: 'check_right', index: 1 },
  '第一个词写的是 aple': { intent: 'check_spelling', index: 1, text: 'aple' },
  '嗯那个词怎么样': { intent: 'check_right' }
};

// 13.1-07 (FA-PHOTO-E2E landing): plausible mis-recognitions for the
// scripted OCR pass — every misread is edit distance 1 from its word, so
// mockGrade reports the near-miss boolean exactly like the real pipeline's
// GradeWord booleans (correct=false, near_miss=true).
const MOCK_MISREADS = { apple: 'aple', banana: 'bnana', cat: 'cut' };

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

// grading.GradeWord stand-in for the scripted lines (mock vocabulary is
// ASCII): normalized equality is correct; a non-blank wrong answer within
// edit distance 1 on a word of >= 2 characters is a near miss — the same
// boolean outcomes the real voice path consumes.
function mockGrade(expected, heard) {
  const norm = (w) => w.toLowerCase().replace(/[^a-z0-9]/g, '');
  const e = norm(expected);
  const h = norm(heard);
  if (e === h) return { correct: true, nearMiss: false };
  const dp = [];
  for (let i = 0; i <= e.length; i++) dp.push([i].concat(Array(h.length).fill(0)));
  for (let j = 1; j <= h.length; j++) dp[0][j] = j;
  for (let i = 1; i <= e.length; i++) {
    for (let j = 1; j <= h.length; j++) {
      dp[i][j] = Math.min(
        dp[i - 1][j - 1] + (e[i - 1] === h[j - 1] ? 0 : 1),
        dp[i - 1][j] + 1,
        dp[i][j - 1] + 1
      );
    }
  }
  const dist = dp[e.length][h.length];
  return { correct: false, nearMiss: dist <= 1 && e.length >= 2 };
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
  const pathname = req.url ? req.url.split('?')[0] : '/';
  if (pathname === '/__test/state') {
    handleTestState(res);
    return;
  }
  // 13.3-03: read-only /api/v1 lessons stubs (fixed dataset above).
  if (pathname === '/api/v1/lessons') {
    lessonsStubList(res);
    return;
  }
  const lessonDetail = /^\/api\/v1\/lessons\/([^/]+)$/.exec(pathname);
  if (lessonDetail) {
    lessonsStubDetail(res, decodeURIComponent(lessonDetail[1]));
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
    partialSave: query.get('mock_partial_save') === '1',
    voiceVerdict: query.get('mock_voice_verdict') === '1',
    // Issue #55: an optional session_ready.category echo ('english'|
    // 'chinese') for the page's subject-adoption pin — the mock never
    // judges anything, it just mirrors what the spec asks it to claim.
    category: query.get('mock_category') === 'chinese' || query.get('mock_category') === 'english'
      ? query.get('mock_category')
      : '',
    // Issue #55 rework: ?mock_subject_ask=1 makes an auto session_init park
    // on the clarify question (subject_ask, no session_ready yet) and the
    // page's subject_pick answer opens the classroom under the PICKED
    // subject — the button-flow pin for the child-decides flow.
    subjectAsk: query.get('mock_subject_ask') === '1',
    // 13.1-07 scripted OCR pass (?mock_ocr=): 'off' = no OCR reader
    // server-side (photo_unavailable degradation, manual-only check);
    // otherwise a ';' list of PER-UPLOAD specs — upload N's readings use
    // spec N (a canceled upload consumes no spec). Each spec is a ',' list
    // of 1-based wrong positions ('wrong2,4' / 'wrong1') or empty/'clean'
    // for all-correct readings.
    ocr: query.get('mock_ocr') || ''
  };
  const ocrSpecs = mockMode.ocr && mockMode.ocr !== 'off'
    ? mockMode.ocr.split(';').map((spec) => ({
        wrong: new Set(
          spec.replace(/^wrong/i, '')
            .split(',')
            .map((n) => parseInt(n, 10))
            .filter((n) => Number.isFinite(n) && n > 0))
      }))
    : null;
  mockMode.name = mockMode.ocr === 'off' ? 'ocr_off'
    : ocrSpecs ? 'photo_ocr'
      : mockMode.voiceVerdict ? 'voice_verdict'
        : mockMode.partialSave ? 'partial_save'
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
    lessonId: LESSON_ID,
    version: 1,
    currentIndex: 0,
    lessonUpdates: 0,
    mode: null,
    practiceInit: false,
    practiceSubmits: 0,
    practiceCompleteSent: false,
    checkActive: false,
    checkVerdicts: [],
    checkSubmits: [],
    /** 13.1-06 voice-verdict mirror: scripted lines received and verdict
        writes applied through the __test_voice channel. */
    voiceLines: [],
    voiceVerdicts: 0,
    voiceTurns: 0,
    /** 13.1-07 photo mirror: assembler + scripted OCR bookkeeping. */
    photoChunks: 0,
    photoUploads: 0,
    photoUploadOrdinal: 0,
    photoCancels: 0,
    photoCanceledMid: false,
    photoUnavailable: 0,
    photoOutOfStage: 0,
    photoBuf: [],
    ocrResults: [],
    reviewResults: 0,
    /** Issue #55 rework: the clarify state — subject_ask went out, and the
        subject_pick answer that opened the classroom (server parity: a
        stray pick with no clarify pending is a no-op). */
    subjectAskSent: false,
    subjectPicked: '',
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

  /** Student answers stay hidden until the entire dictation is complete. */
  function lessonWordsForMode() {
    if (state.mode === 'test' || state.currentIndex >= state.words.length) return state.words.slice();
    return state.words.map(() => '');
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
  // advances with lesson_update, then the next word is announced — or, at the end of the word
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
      type: 'lesson_update', lesson_id: state.lessonId, version: state.version,
      current_index: state.currentIndex, request_id: 'advance-' + state.currentIndex
    });
    // No per-word reveal: check_started sends all answers together.
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

  // 13.1-06 (D-06②) voice-verdict mirror: one scripted student line → the
  // SAME frames the real server's voice path would produce. Validation
  // first (T-13.1-13 mirror): a missing/out-of-range index or a spelling
  // line without the actual text asks again with NO verdict write; a valid
  // payload upserts the verdict (manual=true — it satisfies the submit gate
  // like any human call), echoes check_verdicts, rides the ocr_result
  // display channel for spelling, and speaks the teacher's ack line.
  function applyVoiceVerdict(v) {
    if (state.ended || !state.checkActive) return;
    const total = state.words.length;
    const idx = (v.index || 0) - 1;
    const spelling = v.intent === 'check_spelling';
    const text = typeof v.text === 'string' ? v.text.trim() : '';
    if (idx < 0 || idx >= total || (spelling && !text)) {
      announce('v' + (++state.voiceTurns), '你说的是第几个词？再说一次好吗？', PLAN_NOTICE, 1500,
        () => { send({ type: 'state', value: 'listening' }); });
      return;
    }
    let correct = v.intent === 'check_right';
    let ocrEntry = null;
    if (spelling) {
      const g = mockGrade(state.words[idx], text);
      correct = g.correct;
      ocrEntry = { word_index: idx, ocr_text: text, correct: g.correct, near_miss: g.nearMiss };
    }
    const verdict = { word_index: idx, correct, manual: true };
    if (spelling) verdict.ocr_text = text;
    const found = state.checkVerdicts.find((x) => x.word_index === idx);
    if (found) Object.assign(found, verdict);
    else state.checkVerdicts.push(verdict);
    state.voiceVerdicts += 1;
    send({ type: 'check_verdicts', verdicts: state.checkVerdicts.map((x) => ({ ...x })) });
    if (ocrEntry) send({ type: 'ocr_result', entries: [ocrEntry] });
    let line;
    if (spelling) {
      if (ocrEntry.correct) line = '对，' + state.words[idx] + ' 写的就是 ' + text + '，这个词写对了，真棒！';
      else if (ocrEntry.near_miss) line = state.words[idx] + ' 你写成了 ' + text + '，差一个字母，很接近了！把它改过来就更棒啦。';
      else line = '我们再看看 ' + state.words[idx] + ' 这个词：你写的是 ' + text + '，把它改过来吧。';
    } else {
      line = correct
        ? '好，第' + (idx + 1) + '个词对了，你真棒！'
        : '好，第' + (idx + 1) + '个词错了，我们在纸上把它改一改吧。';
    }
    announce('v' + (++state.voiceTurns), line, PLAN_NOTICE, 1600 + idx * 10,
      () => { send({ type: 'state', value: 'listening' }); });
  }

  // 13.1-07 (FA-PHOTO-E2E landing): the scripted OCR pass for one COMPLETED
  // upload. The readings come from THIS upload attempt's spec (the ordinal
  // counts seq-1 starts, so an abandoned upload consumes its spec and the
  // retake OCRs only the second photo). The frame order mirrors
  // applyOcrPrefills field-for-field: prefill verdicts (manual=false — never
  // satisfies the submit gate, D-05) through the SAME upsert the button taps
  // use, the authoritative check_verdicts echo, THEN the ocr_result display
  // entries.
  function runMockOcr() {
    if (state.ended || !state.checkActive) return;
    const spec = ocrSpecs
      ? ocrSpecs[Math.min(Math.max(state.photoUploadOrdinal, 1) - 1, ocrSpecs.length - 1)]
      : { wrong: new Set() };
    state.photoUploads += 1;
    const entries = state.words.map((w, i) => {
      const wrong = spec.wrong.has(i + 1);
      const text = wrong ? (MOCK_MISREADS[w] || w + 'x') : w;
      const g = mockGrade(w, text);
      return { word_index: i, ocr_text: text, correct: g.correct, near_miss: g.nearMiss };
    });
    state.ocrResults.push(entries.map((e) => ({ ...e })));
    for (const e of entries) {
      const prefill = { word_index: e.word_index, correct: e.correct, manual: false };
      const found = state.checkVerdicts.find((v) => v.word_index === e.word_index);
      if (found) Object.assign(found, prefill);
      else state.checkVerdicts.push(prefill);
    }
    send({ type: 'check_verdicts', verdicts: state.checkVerdicts.map((v) => ({ ...v })) });
    send({ type: 'ocr_result', entries });
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
    if (reason === 'practice_complete') state.practiceCompleteSent = true;
    if (pair) pair.phase = 'done'; // any later connection short-circuits
    send({ type: 'session_ended', reason });
    closeSoon();
  }

  function startSessionFlow() {
    if (state.started || state.ended) return;
    state.started = true;
    send({ type: 'state', value: 'listening' });
    if (mockMode.empty) {
      // Zero due words: the D-13 days copy (the real server computes X from
      // the vocabulary's fsrs due data — the mock pins 3 for determinism),
      // then reason=no_due_words strictly after that sentence's matching ack.
      announce('nodue', '今天没有要到期的单词，下次复习大约在 3 天后，好好休息，我们 3 天后再练吧。', PLAN_NOTICE, 500, (epoch, sid) => {
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
        if (msg.practice === true) {
          // 13.1-05 practice branch (D-11/D-12, handlePracticeInit parity):
          // deterministic random pick over the pool EXCLUDING the scripted
          // already-done words, a fresh r0x- lesson id (Pitfall 10 — the
          // daily r0- namespace is never touched), then the SAME three-stage
          // flow — only the submit/ending differ (practice_submitted +
          // practice_complete).
          state.practiceInit = true;
          state.words = practicePick(WORDS);
          state.currentIndex = 0;
          state.version = 1;
          state.lessonId = 'r0x-mock-' + (++practiceCounter);
          send({
            type: 'session_ready',
            session_id: state.id,
            audio: { sampleRate: 16000, format: 's16le', channels: 1 },
            tts: { sampleRate: TTS_RATE },
            lesson: { id: state.lessonId, version: state.version, current_index: 0, words: lessonWordsForMode() },
            ...(mockMode.category ? { category: mockMode.category } : {})
          });
          break;
        }
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
            lesson: { id: LESSON_ID, version: state.version, current_index: state.currentIndex, words: lessonWordsForMode() },
            ...(mockMode.category ? { category: mockMode.category } : {})
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
            lesson: { id: LESSON_ID, version: state.version, current_index: state.currentIndex, words: lessonWordsForMode() },
            ...(mockMode.category ? { category: mockMode.category } : {})
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
        // Issue #55 rework: an auto entry under ?mock_subject_ask=1 parks on
        // the clarify question — subject_ask out, NO session_ready until the
        // page's subject_pick names the subject (startSubjectAsk parity).
        if (mockMode.subjectAsk && msg.category === 'auto' && !state.subjectPicked) {
          state.subjectAskSent = true;
          send({
            type: 'subject_ask',
            question: '语文和英语今天都有要默写的词语。这次想先默写哪一科？'
          });
          // The teacher SPEAKS the question too (announce parity): the pick
          // buttons are live WHILE the audio is still out, so the spec taps
          // mid-announcement — the wire shape the server-side epoch-gated
          // termination has to survive.
          announce('ask', '语文和英语今天都有要默写的词语。这次想先默写哪一科？', PLAN_NOTICE, 320,
            () => { send({ type: 'state', value: 'listening' }); });
          break;
        }
        // LessonView field-for-field (id / version / current_index / words).
        send({
          type: 'session_ready',
          session_id: state.id,
          audio: { sampleRate: 16000, format: 's16le', channels: 1 },
          tts: { sampleRate: TTS_RATE },
          lesson: { id: LESSON_ID, version: state.version, current_index: state.currentIndex, words: lessonWordsForMode() },
          ...((state.subjectPicked || mockMode.category) ? { category: state.subjectPicked || mockMode.category } : {})
        });
        break;
      }

      case 'subject_pick': {
        // Issue #55: the clarify's button answer. ParseControl parity —
        // english/chinese only; and only a pending clarify consumes it
        // (a stray pick with no clarify parked is a no-op server-side).
        if (msg.category !== 'english' && msg.category !== 'chinese') {
          send({ type: 'error', code: 'invalid_message', message: 'mock: subject_pick requires english or chinese', recoverable: true });
          break;
        }
        if (!state.subjectAskSent || state.subjectPicked) break;
        state.subjectPicked = msg.category;
        // stopAskPlayback parity: the pick terminates the ask mid-play by
        // bumping the epoch — the ask's already-scheduled chunks ride the
        // OLD epoch and the client's epoch gate drops them, while the
        // classroom that answers here speaks under the new one.
        state.epoch += 1;
        send({
          type: 'session_ready',
          session_id: state.id,
          audio: { sampleRate: 16000, format: 's16le', channels: 1 },
          tts: { sampleRate: TTS_RATE },
          lesson: { id: LESSON_ID, version: state.version, current_index: state.currentIndex, words: lessonWordsForMode() },
          category: state.subjectPicked
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

      case 'read_word': {
        if (!state.ready || state.ended || msg.current_index !== state.currentIndex ||
            !Number.isInteger(msg.word_index) || msg.word_index < 0 || msg.word_index >= state.words.length ||
            (!state.checkActive && msg.word_index > state.currentIndex)) {
          send({ type: 'error', code: 'invalid_message', message: '现在不能重读这个词', recoverable: true });
          break;
        }
        cancelPending();
        const sid = 'read-' + (state.turn + 1);
        state.pendingReading = { epoch: state.epoch, sentence_id: sid };
        const text = state.checkActive
          ? '好，我再读一遍，' + state.words[msg.word_index]
          : wordAnnounceText('好，我再读一遍，', msg.word_index);
        announce(sid, text, PLAN_WORD, 100, () => {
          send({ type: 'state', value: state.pauseActive ? 'paused' : 'listening' });
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
        const reading = state.pendingReading;
        if (reading && reading.epoch === msg.epoch && reading.sentence_id === msg.sentence_id) {
          state.pendingReading = null;
          if (!state.checkActive && !state.pauseActive) armConfirmTimer();
        }
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
        if (state.practiceInit) {
          // 13.1-05 practice mirror (T-13.1-11): the practice submit writes
          // the Go-owned practice records INSTEAD of the FSRS review — the
          // result reports the RECORD counts and the session records NO
          // review (state.reviewResults stays 0: the wire-side proof the
          // spec asserts). Ending: ack-gated session_ended(practice_complete).
          state.practiceSubmits += 1;
          state.reviewResults += 0; // deliberate: never an FSRS write here
          send({ type: 'review_result', requested: n, updated: n, missing: 0, unchecked: false });
          state.summaries += 1;
          announce('psum', '这组练习' + n + '个词对了' + correct + '个，很棒！', PLAN_SUMMARY, 900, (epoch, sid) => {
            state.pendingTerminal = { reason: 'practice_complete', epoch, sentence_id: sid };
          });
          break;
        }
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

      case '__test_voice': {
        // 13.1-06 test-only channel (?mock_voice_verdict=1): a scripted
        // student line rides in as a control frame (the __test_resume
        // precedent for test-only channels); the stand-in classifier maps
        // it to the check-stage intent and runs the same verdict/announce
        // frames the real server's voice path would. Unknown lines are
        // recorded and ignored — the stand-in has no opinion on them.
        if (!mockMode.voiceVerdict) break;
        state.voiceLines.push(String(msg.line || ''));
        const said = VOICE_LINES[String(msg.line || '')];
        if (said) applyVoiceVerdict(said);
        break;
      }

      case 'photo_chunk': {
        // 13.1-03/13.1-07 mirror: the assembler discipline (seq 1 RESETS the
        // buffer — the retake contract; any other seq must be exactly
        // contiguous) + the scripted OCR pass on final. Outside a live check
        // stage every chunk is refused (photoStageClosedNotice parity), and
        // with the OCR reader disabled a coded honest degradation error
        // answers instead (photoUnavailableNotice parity — the check keeps
        // working manual-only).
        if (!state.checkActive || state.ended) {
          state.photoOutOfStage += 1;
          send({ type: 'error', code: 'invalid_message', message: '现在不是拍照检查的时间哦', recoverable: true });
          break;
        }
        if (mockMode.ocr === 'off') {
          state.photoUnavailable += 1;
          send({ type: 'error', code: 'photo_unavailable', message: '拍照检查现在用不了，我们看着屏幕逐个确认吧', recoverable: true });
          break;
        }
        const seq = msg.seq;
        if (typeof seq !== 'number' || seq < 1) {
          send({ type: 'error', code: 'invalid_message', message: '这张照片没有传完整，再拍一次吧', recoverable: true });
          break;
        }
        if (seq === 1) {
          state.photoBuf = [];
          state.photoUploadOrdinal += 1; // a new upload attempt — consumes the next spec
        }
        if (seq !== state.photoBuf.length + 1) {
          // Out-of-order/duplicate frame: rejected, buffer untouched (the
          // real add() refuses without absorbing; the 1-based mirror keeps
          // the same observable).
          send({ type: 'error', code: 'invalid_message', message: '这张照片没有传完整，再拍一次吧', recoverable: true });
          break;
        }
        state.photoBuf.push(seq);
        state.photoChunks += 1;
        if (msg.final) {
          state.photoBuf = []; // consumed: one upload never bleeds into the next
          runMockOcr();
        }
        break;
      }

      case 'photo_cancel': {
        // 13.1-03 retake discipline: discard the in-flight buffer
        // unconditionally and answer NOTHING (the real handler only records
        // the event — without the clear, the next upload's restart-at-1
        // sequences would absorb stale bytes).
        const hadBuf = state.photoBuf.length > 0;
        state.photoBuf = [];
        state.photoCancels += 1;
        if (hadBuf) state.photoCanceledMid = true;
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
    ' practice flow 13.1-05 keyed on session_init.practice,' +
    ' /api/v1 lessons stubs 13.3-03,' +
    ' /__test/state introspection)');
});
