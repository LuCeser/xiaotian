// 13-12 Task 2 Track 1 — full E2E dictation-lesson driver against the REAL
// running 8443 service (WSS /rt). Scratch-only tool; NEVER committed
// (T-13-30: raw evidence stays local, transcripts never leave this machine).
//
// Basis: .scratch/smoke-rt-8443-1312.mjs (handshake baseline), extended into
// a full classroom flow + assertion matrix + evidence output.
//
// Faithful-browser behaviors modeled (rt-client.js / rt-capture.js /
// rt-playback.js):
//   - MIC IS ALWAYS OPEN: a 20 ms pump sends 320-sample (640-byte s16le)
//     blocks CONTINUOUSLY — fixture PCM while an utterance is queued, pure
//     silence otherwise. The browser's mic never mutes between phrases; the
//     server VAD hangover can only close a segment when real quiet frames
//     flow (first run's lesson: muted-between-phrases latches segments).
//   - downlink playback: binary chunks scheduled as they arrive; a sentence's
//     sentence_ack is sent only after audio_end AND the simulated onended of
//     the sentence duration (samples/24 ms at 24 kHz) — actual-playback
//     completion, never a start signal.
//   - interrupt: browser detection is RMS-side; the WIRE path is what the
//     server sees — mic frames keep flowing (server half-duplex gate drops
//     them) while an explicit interrupt control message
//     {epoch, last_sentence_id, played_until_ms} is sent mid-playback.
//   - ping every 10 s → clock_offset {offset_ms, rtt_ms} events.
//   - fixture WAVs are RIFF-parsed to the true data chunk offset (78 bytes
//     on the Volcengine-generated set — NOT the canonical 44).
//
// Evidence (all under .scratch/e2e-1312/, never committed):
//   rt-timing-e2e-1312.jsonl  — TimingLog-schema client events
//   messages-timeline.jsonl   — full raw control-message timeline (local only)
//   server-rt-events.jsonl    — this session's msg="rt_event" lines (tail read)
//   e2e-1312-results.json     — machine-readable assertion matrix
//
// Exit code: 0 = all assertions pass; 2 = at least one fail (results still
// complete). Findings are EVIDENCE ONLY — no fix attempts here.
import WebSocket from 'ws';
import fs from 'node:fs';
import path from 'node:path';
import { execFileSync } from 'node:child_process';

const ROOT = path.resolve(import.meta.dirname, '..');
const OUT = path.join(ROOT, '.scratch', 'e2e-1312');
fs.mkdirSync(OUT, { recursive: true });
const FIX = path.join(ROOT, '.scratch', 'fixtures-1312');
const SERVER_LOG = path.join(ROOT, 'logs', 'server-8443.err.log');
const DB = path.join(ROOT, '.hta-data', 'vocab.db');
const URL = 'wss://127.0.0.1:8443/rt';

// Internal watchdog: bounded well under the 8-minute discipline.
const TOTAL_MS = 6 * 60 * 1000;

const t0 = Date.now();
const nowMs = () => Date.now() - t0;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ---------------------------------------------------------------------------
// Evidence buffers
// ---------------------------------------------------------------------------
const timing = [];
const tlog = (event, extra = {}) =>
  timing.push({ event, t_ms: nowMs(), ...extra });
const timeline = [];
const rec = (dir, msg) => timeline.push({ t_ms: nowMs(), dir, msg });

const results = [];
function assert(name, pass, detail) {
  results.push({ assertion: name, pass: !!pass, detail });
  console.log(`  [${pass ? 'PASS' : 'FAIL'}] ${name} — ${detail}`);
}

// ---------------------------------------------------------------------------
// Connection / session state
// ---------------------------------------------------------------------------
let ws = null;
let finished = false;
let evidenceWritten = false;
const S = {
  ready: null,
  words: [],
  N: 0,
  state: '',
  lastStateChangeTs: 0,
  lastTurnStartTs: 0,
  epoch: 0,
  lastTurnId: 0,
  curSent: null, // {epoch, sentenceId, firstChunkTs, samples}
  pendingAcks: new Map(), // 'epoch:sentenceId' -> timer
  sentAcks: 0,
  binaryFrames: 0,
  zeroBinFrames: 0,
  audioStarts: 0,
  audioEnds: 0,
  turnStarts: 0,
  transcripts: [], // {turn_id, t_ms}
  lessonUpdates: [], // {version, current_index, t_ms}
  interruptAcks: [],
  errors: [],
  sessionEnded: null,
  firstAudioPlayed: false,
  teacherTexts: new Map(), // sentenceId -> text (memory only; never leaves)
  sentenceLog: [], // {id, epoch, t_ms} — every teacher_sentence seen
  nonJson: [], // captured unparseable server text frames {t_ms, hex, utf8}
  greeting: null, // {sentences, audioEnds, done, doneTs}
};

const OPEN = WebSocket.OPEN;

function send(obj) {
  if (!ws || ws.readyState !== OPEN) return;
  rec('out', obj);
  ws.send(JSON.stringify(obj));
}

const mark = () => ({
  ts: nowMs(),
  turns: S.turnStarts,
  lu: S.lessonUpdates.length,
  bin: S.binaryFrames,
  ae: S.audioEnds,
});

function waitFor(pred, timeoutMs, everyMs = 50) {
  const dl = Date.now() + timeoutMs;
  return new Promise((resolve) => {
    const iv = setInterval(() => {
      let v = false;
      try { v = pred(); } catch { v = false; }
      if (v) { clearInterval(iv); resolve(true); }
      else if (Date.now() > dl) { clearInterval(iv); resolve(false); }
    }, everyMs);
  });
}

// Ready-to-speak: the classroom accepts uplink in listening, and while the
// pause bit holds (resume must be recognizable — uplink is NOT gated then).
async function waitReady(gateMs = 650, timeoutMs = 25000) {
  const ok = await waitFor(() => S.state === 'listening' || S.state === 'paused', timeoutMs);
  if (!ok) return false;
  // Echo tail (VADEchoTailMs, default 500ms) gates uplink after the ack —
  // mimic the browser's natural pause before the child starts talking.
  await sleep(gateMs);
  return S.state === 'listening' || S.state === 'paused';
}

// ---------------------------------------------------------------------------
// Wire handlers
// ---------------------------------------------------------------------------
function handleText(m) {
  rec('in', m);
  switch (m.type) {
    case 'session_ready':
      S.ready = m;
      S.words = (m.lesson && m.lesson.words) || [];
      S.N = S.words.length;
      break;
    case 'turn_start':
      S.turnStarts++;
      S.lastTurnStartTs = Date.now();
      S.lastTurnId = m.turn_id;
      S.epoch = m.epoch;
      S.turnFirstAudioLogged = false; // browser: one first_audio_played per turn
      break;
    case 'transcript':
      // Text stays in messages-timeline only (local); count + id here.
      S.transcripts.push({ turn_id: m.turn_id, t_ms: nowMs() });
      tlog('transcript_received', { turn_id: m.turn_id });
      break;
    case 'teacher_sentence':
      S.teacherTexts.set(m.sentence_id, m.text || '');
      S.sentenceLog.push({ id: m.sentence_id, epoch: m.epoch, t_ms: Date.now() });
      // Announcement turn id derives from the sentence id (t<turn>-s<idx>);
      // the greeting announcement has no preceding turn_start.
      S.annTurnId = parseInt(m.sentence_id.split('-')[0].slice(1), 10) || S.lastTurnId;
      break;
    case 'audio_start':
      S.audioStarts++;
      S.curSent = { epoch: m.epoch, sentenceId: m.sentence_id, firstChunkTs: null, samples: 0 };
      S.epoch = m.epoch;
      break;
    case 'audio_end':
      S.audioEnds++;
      scheduleAck();
      break;
    case 'lesson_update':
      S.lessonUpdates.push({ version: m.version, current_index: m.current_index, t_ms: nowMs() });
      break;
    case 'interrupt_ack':
      S.interruptAcks.push(m);
      break;
    case 'state':
      S.state = m.value;
      S.lastStateChangeTs = Date.now();
      // First listening broadcast closes the greeting observation window.
      if (S.greeting && !S.greeting.done && m.value === 'listening') {
        S.greeting.done = true;
        S.greeting.doneTs = nowMs();
      }
      break;
    case 'error':
      S.errors.push({ code: m.code, message: m.message, recoverable: m.recoverable });
      break;
    case 'session_ended':
      S.sessionEnded = m;
      break;
    case 'pong': {
      const sentAt = S._pingSentAt || Date.now();
      const rtt = Date.now() - sentAt;
      const offsetMs = m.t_server_mono_ns / 1e6 - (sentAt + rtt / 2);
      S.pingStats = S.pingStats || [];
      S.pingStats.push({ rtt_ms: rtt });
      tlog('clock_offset', { offset_ms: Math.round(offsetMs), rtt_ms: rtt });
      break;
    }
    default:
      break;
  }
}

function scheduleAck() {
  const s = S.curSent;
  S.curSent = null;
  if (!s) return;
  if (S.greeting && !S.greeting.done) S.greeting.audioEnds++;
  const durMs = s.samples / 24; // s16le mono @ 24 kHz → ms
  const firstTs = s.firstChunkTs || Date.now();
  const delay = Math.max(0, durMs - (Date.now() - firstTs));
  const key = `${s.epoch}:${s.sentenceId}`;
  const timer = setTimeout(() => {
    S.pendingAcks.delete(key);
    if (!ws || ws.readyState !== OPEN) return;
    send({ type: 'sentence_ack', sentence_id: s.sentenceId, epoch: s.epoch });
    S.sentAcks++;
    tlog('sentence_ack', { epoch: s.epoch, sentence_id: s.sentenceId });
  }, delay);
  S.pendingAcks.set(key, timer);
}

function handleBinary(data) {
  S.binaryFrames++;
  if (data.length === 0) { S.zeroBinFrames++; return; }
  const s = S.curSent;
  if (!s) return;
  const now = Date.now();
  if (s.firstChunkTs === null) {
    s.firstChunkTs = now;
    // Browser-faithful: one first_audio_played per turn (rt-playback's
    // _turnFirstAudioDone), turn id = the announcement's own turn.
    if (!S.turnFirstAudioLogged) {
      S.turnFirstAudioLogged = true;
      tlog('first_audio_played', { turn_id: S.annTurnId || S.lastTurnId, epoch: s.epoch, sentence_id: s.sentenceId });
    }
    if (!S.firstAudioPlayed) S.firstAudioPlayed = true;
    if (S.greeting && !S.greeting.done) S.greeting.sentences++;
  }
  s.samples += data.length / 2;
}

// ---------------------------------------------------------------------------
// Mic pump: ALWAYS-OPEN microphone (browser-faithful)
// ---------------------------------------------------------------------------
const micQueue = []; // Buffers of PCM to speak, drained 640B per 20 ms tick
let micPumpIv = null;
const SILENCE = Buffer.alloc(640, 0);

function startMic() {
  if (micPumpIv) return;
  micPumpIv = setInterval(() => {
    if (!ws || ws.readyState !== OPEN) return;
    let frame = Buffer.alloc(0);
    while (micQueue.length > 0 && frame.length < 640) {
      const head = micQueue[0];
      const take = Math.min(640 - frame.length, head.length);
      frame = Buffer.concat([frame, head.subarray(0, take)]);
      if (take === head.length) micQueue.shift();
      else micQueue[0] = head.subarray(take);
    }
    if (frame.length === 0) frame = SILENCE;
    else if (frame.length < 640) {
      // partial: pad with silence to keep the 20 ms cadence exact
      frame = Buffer.concat([frame, Buffer.alloc(640 - frame.length, 0)]);
    }
    ws.send(frame);
  }, 20);
}

function stopMic() {
  if (micPumpIv) { clearInterval(micPumpIv); micPumpIv = null; }
}

/**
 * Queue one fixture utterance and resolve when it has fully drained through
 * the 20 ms pump (real-time cadence). PCM is taken from the RIFF data chunk.
 */
async function speak(file) {
  const pcm = readWavPcm(path.join(FIX, file));
  micQueue.push(pcm);
  await waitFor(() => micQueue.length === 0, 30000, 10);
  await sleep(150); // let the final frame land
  return Math.round(pcm.length / 32); // ms of speech (640 B per 20 ms)
}

// RIFF-parse to the true data chunk offset (Volcengine fixtures: 78 B header).
function readWavPcm(file) {
  const b = fs.readFileSync(file);
  let off = 12;
  while (off + 8 <= b.length) {
    const id = b.toString('ascii', off, off + 4);
    const sz = b.readUInt32LE(off + 4);
    if (id === 'data') return b.subarray(off + 8, Math.min(off + 8 + sz, b.length));
    off += 8 + sz + (sz % 2);
  }
  return b.subarray(44); // fallback: canonical header
}

// ---------------------------------------------------------------------------
// Utterance turns
// ---------------------------------------------------------------------------
/**
 * One student utterance: wait for ready-to-speak, speak via the open mic,
 * wait for the turn to open and the teacher's response to settle.
 * endState: 'listening' | 'paused'.
 */
async function speakTurn(file, endState = 'listening', timeoutMs = 40000) {
  await waitReady(650);
  const m = mark();
  await speak(file);
  // Turn must open (VAD hangover ≈700 ms after speech ends)…
  let ok = await waitFor(() => S.turnStarts > m.turns, timeoutMs);
  if (!ok) return { settled: false, m, turnsDelta: 0, luDelta: 0, noTurn: true };
  // …and the response must settle back to the expected effective state.
  ok = await waitFor(() =>
    S.turnStarts === m.turns + 1 &&
    S.state === endState &&
    S.lastStateChangeTs > S.lastTurnStartTs, timeoutMs);
  await sleep(300);
  return {
    settled: ok,
    m,
    luDelta: S.lessonUpdates.length - m.lu,
    turnsDelta: S.turnStarts - m.turns,
  };
}

// ---------------------------------------------------------------------------
// Scenario acts
// ---------------------------------------------------------------------------

// VAD false-trigger probes: pure silence then low noise, real-time paced.
// Split on purpose: silence is the echo/0-second regression check; noise
// (sustained energy) is reported separately as observed behavior.
async function vadProbeAct() {
  await waitReady(900);
  const m = mark();
  await speak('silence-3s.wav');
  await sleep(1300); // > hangover(700ms) in case of any onset
  const afterSilence = S.turnStarts - m.turns;
  await speak('noise-2s.wav');
  await sleep(6000); // observed segment-close + ASR-empty settle lag ≈ 4-5 s
  const afterNoise = S.turnStarts - m.turns;
  return {
    silenceTurnStarts: afterSilence,
    noiseTurnStarts: afterNoise,
    windowMs: nowMs() - m.ts,
  };
}

// Pause → filler (must not advance) → resume.
async function pauseResumeAct() {
  const pause = await speakTurn('xiuxi.wav', 'paused');
  const stateAfterPause = S.state;
  const m = mark();
  const filler = await speakTurn('filler.wav', 'paused');
  const fillerAdvanced = S.lessonUpdates.length - m.lu;
  const fillerTurns = S.turnStarts - m.turns;
  const stateAfterFiller = S.state;
  const resume = await speakTurn('jixu.wav', 'listening');
  const stateAfterResume = S.state;
  return {
    pause, filler, resume,
    stateAfterPause, stateAfterFiller, stateAfterResume,
    fillerAdvanced, fillerTurns,
  };
}

// Interrupt: the confirm advances to word k; its announcement begins; we
// barge in mid-playback exactly like the browser's RMS detector (wire path).
async function confirmWithInterrupt(file) {
  await waitReady(650);
  const m = mark();
  await speak(file);
  // Turn opens, then the advance commits…
  let ok = await waitFor(() => S.turnStarts > m.turns, 20000);
  if (!ok) return { interrupted: false, reason: 'no_turn' };
  ok = await waitFor(() => S.lessonUpdates.length > m.lu, 40000);
  if (!ok) return { interrupted: false, reason: 'no_advance' };
  const targetIndex = S.lessonUpdates[S.lessonUpdates.length - 1].current_index;
  // …and the announcement's first downlink chunk arrives.
  ok = await waitFor(() => S.binaryFrames > m.bin && S.curSent !== null, 12000);
  if (!ok) return { interrupted: false, reason: 'no_announcement_audio', targetIndex };
  const sent = S.curSent;
  await sleep(400); // land mid-playback (well inside the sentence duration)
  const oldEpoch = sent.epoch;
  const lastSid = sent.sentenceId;
  const playedUntilMs = Math.max(1, Date.now() - (sent.firstChunkTs || Date.now()));
  tlog('interrupt_start', { epoch: oldEpoch, sentence_id: lastSid, source: 'manual' });
  const interruptSentAt = Date.now();
  // Open mic during teacher speech: the pump keeps flowing (half-duplex gate
  // drops frames server-side) — same as the browser. Speak deng concurrently.
  const dengP = speak('deng.wav');
  send({ type: 'interrupt', epoch: oldEpoch, last_sentence_id: lastSid, played_until_ms: playedUntilMs });
  // Local playback stops now: drop every pending ack of the old epoch.
  for (const [k, t] of [...S.pendingAcks]) {
    if (k.startsWith(oldEpoch + ':')) { clearTimeout(t); S.pendingAcks.delete(k); }
  }
  tlog('interrupt_stop', { epoch: oldEpoch });
  const acked = await waitFor(() => S.interruptAcks.some((a) => a.old_epoch === oldEpoch), 6000);
  const binAtAck = S.binaryFrames;
  await dengP; // rest of the utterance hits the open mic (a real turn)
  // Old-epoch audio must never resume: no new binary frames, and no late
  // teacher_sentence registered under the old epoch.
  await sleep(1500);
  const revivedFrames = S.binaryFrames - binAtAck;
  const lateOldSentences = S.sentenceLog.filter(
    (x) => x.epoch === oldEpoch && x.t_ms > interruptSentAt,
  ).length;
  // The deng tail may have opened its own turn; let it settle. NOTE: the
  // tail ("等一下，我还在写") legitimately classifies as intent=pause, which
  // re-pauses the classroom — recover with a real resume command so the
  // lesson can continue.
  await waitFor(
    () => S.state === 'listening' || S.state === 'paused',
    35000,
  );
  await sleep(400);
  let resumedAfterDeng = false;
  if (S.state === 'paused') {
    const r = await speakTurn('jixu.wav', 'listening');
    resumedAfterDeng = r.settled;
  }
  return {
    interrupted: acked,
    targetIndex,
    oldEpoch,
    revivedFrames,
    lateOldSentences,
    resumedAfterDeng,
    interruptAck: S.interruptAcks.find((a) => a.old_epoch === oldEpoch) || null,
  };
}

// Timeout: stay completely silent (mic keeps sending silence); the armed
// per-word timer must advance.
async function timeoutAct(boundMs = 80000) {
  await waitReady(500);
  const m = mark();
  const armedTs = nowMs();
  const advanced = await waitFor(() => S.lessonUpdates.length > m.lu, boundMs);
  const waitMs = nowMs() - armedTs;
  const turnsDuringWait = S.turnStarts - m.turns;
  const newIndex = advanced ? S.lessonUpdates[S.lessonUpdates.length - 1].current_index : -1;
  // Let the follow-up announcement settle.
  await waitFor(() => S.state === 'listening' && S.lastStateChangeTs > S.lastTurnStartTs, 30000);
  await sleep(400);
  return { advanced, waitMs, turnsDuringWait, newIndex };
}

// ---------------------------------------------------------------------------
// Server-side evidence (post-session)
// ---------------------------------------------------------------------------
function extractServerEvents(sessionId) {
  let size = 0;
  try { size = fs.statSync(SERVER_LOG).size; } catch { return []; }
  const start = Math.max(0, size - 8 * 1024 * 1024); // tail ≤ 8 MB
  const len = size - start;
  const buf = Buffer.alloc(len);
  const fd = fs.openSync(SERVER_LOG, 'r');
  try {
    fs.readSync(fd, buf, 0, len, start);
  } finally {
    fs.closeSync(fd);
  }
  const events = [];
  for (const line of buf.toString('utf8').split('\n')) {
    if (!line.includes('rt_event')) continue;
    try {
      const j = JSON.parse(line);
      if (j && j.msg === 'rt_event' && j.session_id === sessionId) events.push(j);
    } catch { /* torn line at tail boundary */ }
  }
  return events;
}

function pyOut(args, cwd = ROOT) {
  try {
    return execFileSync('py', args, { encoding: 'utf8', cwd, timeout: 30000 });
  } catch (e) {
    return 'PY_ERR ' + (e.message || String(e));
  }
}

function lessonsRowSnapshot() {
  const out = pyOut([path.join(ROOT, '.scratch', 'check-lesson-row-1312.py'), DB]);
  const row = { ok: !out.startsWith('PY_ERR') };
  const m = out.match(/lesson: (r0-\d+) \| version: (\d+) \| current_index: (\d+) \| words_count: (\d+)/);
  if (m) {
    row.lesson_id = m[1];
    row.version = parseInt(m[2], 10);
    row.current_index = parseInt(m[3], 10);
    row.words_count = parseInt(m[4], 10);
  }
  return row;
}

function reviewLogCount() {
  const out = pyOut(['-c', `import sqlite3;print(sqlite3.connect(r'${DB}').execute('select count(*) from review_logs').fetchone()[0])`]);
  const n = parseInt(out.trim(), 10);
  return Number.isFinite(n) ? n : -1;
}

// ---------------------------------------------------------------------------
// Finish / evidence write (idempotent)
// ---------------------------------------------------------------------------
function writeEvidence(sessionId, serverEvents, exitContext) {
  if (evidenceWritten) return;
  evidenceWritten = true;
  const jl = (arr) => arr.map((x) => JSON.stringify(x)).join('\n') + '\n';
  try {
    fs.writeFileSync(path.join(OUT, 'rt-timing-e2e-1312.jsonl'),
      jl(timing.map((t) => ({ session_id: sessionId, ...t }))));
    fs.writeFileSync(path.join(OUT, 'messages-timeline.jsonl'),
      jl(timeline.map((t) => ({ session_id: sessionId, ...t }))));
    fs.writeFileSync(path.join(OUT, 'server-rt-events.jsonl'), jl(serverEvents));
    fs.writeFileSync(path.join(OUT, 'e2e-1312-results.json'), JSON.stringify({
      generated_at: new Date().toISOString(),
      session_id: sessionId,
      lesson_id: S.ready ? S.ready.lesson.id : null,
      words_count: S.N,
      duration_ms: nowMs(),
      exit_context: exitContext || 'normal',
      observations: {
        zero_length_server_binary_frames: S.zeroBinFrames,
        non_json_server_text_frames: S.nonJson,
        client_turns: S.turnStarts,
        sentence_acks_sent: S.sentAcks,
        server_audio_ends: S.audioEnds,
      },
      defect_candidates: results.filter((r) => !r.pass),
      results,
    }, null, 2));
    console.log('evidence written to', OUT);
  } catch (e) {
    console.error('evidence write failed:', e.message);
  }
}

let exitCode = null;
function finish(code, exitContext = 'normal') {
  if (exitCode !== null) return;
  exitCode = code;
  stopMic();
  const sessionId = S.ready ? S.ready.session_id : null;
  writeEvidence(sessionId, sessionId ? extractServerEvents(sessionId) : [], exitContext);
  setTimeout(() => process.exit(code), 200);
}

const watchdog = setTimeout(() => {
  console.error('E2E WATCHDOG fired — dumping partial evidence');
  finish(2, 'watchdog_timeout');
}, TOTAL_MS);
process.on('unhandledRejection', (e) => console.error('unhandledRejection:', e && e.message));
process.on('uncaughtException', (e) => {
  console.error('uncaughtException:', e && e.message);
  finish(2, 'uncaught_exception');
});

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------
async function main() {
  tlog('page_ready');
  const lessonsBefore = lessonsRowSnapshot();
  const reviewBefore = reviewLogCount();
  console.log('baseline lessons row:', JSON.stringify({
    lesson_id: lessonsBefore.lesson_id, version: lessonsBefore.version,
    current_index: lessonsBefore.current_index, words_count: lessonsBefore.words_count,
  }), '| review_logs before =', reviewBefore);

  ws = new WebSocket(URL, {
    rejectUnauthorized: false,
    headers: { Origin: 'https://127.0.0.1:8443' },
  });

  await new Promise((resolve, reject) => {
    const to = setTimeout(() => reject(new Error('ws open timeout')), 15000);
    ws.once('open', () => { clearTimeout(to); resolve(); });
    ws.once('error', (e) => { clearTimeout(to); reject(e); });
  });
  tlog('connected');
  console.log('WSS handshake OK at', nowMs(), 'ms');

  startMic();

  const pingIv = setInterval(() => {
    if (ws && ws.readyState === OPEN) {
      S._pingSentAt = Date.now();
      send({ type: 'ping', t_client_ms: Date.now() % 100000000 });
    }
  }, 10000);

  ws.on('message', (data, isBinary) => {
    if (isBinary) { handleBinary(data); return; }
    try { handleText(JSON.parse(data.toString())); } catch {
      const entry = {
        t_ms: nowMs(),
        hex: data.toString('hex').slice(0, 64),
        utf8: JSON.stringify(data.toString('utf8').slice(0, 64)),
        len: data.length,
      };
      S.nonJson.push(entry);
      rec('in', { type: 'non-json', ...entry });
    }
  });
  ws.on('error', (e) => console.error('WS ERROR:', e.message));
  ws.on('close', () => clearInterval(pingIv));

  // --- 1. session_init → session_ready -----------------------------------
  // Node equivalent of the browser's page-auth step (the report pipeline's
  // greeting anchor); provenance noted in the extra field.
  tlog('auth_complete', { note: 'node-driver-session_init-consent' });
  send({ type: 'session_init', audio: { sampleRate: 16000, format: 's16le', channels: 1 } });
  const readyOk = await waitFor(() => S.ready !== null, 30000);
  if (!readyOk || S.N === 0) {
    assert('session_ready_words', false,
      `ready=${!!S.ready} words=${S.N} (empty words = no due words, an env precondition — rerun after adding due words, never fake a pass)`);
    return finish(2, 'no_session_ready');
  }
  assert('session_ready_words', true,
    `session_ready received; words=${S.N} lesson_version=${S.ready.lesson.version} current_index=${S.ready.lesson.current_index}`);
  // Resume semantics: the FSM continues at the stored cursor — only words
  // [current_index, N) are actionable this session (M-space below).
  const startIdx = S.ready.lesson.current_index || 0;
  const M = S.N - startIdx;
  S.greeting = { sentences: 0, audioEnds: 0, done: false, doneTs: 0 };

  // --- 2. first greeting --------------------------------------------------
  // The opening announcement streams immediately after session_ready; it
  // settles when the final sentence's ack lands and listening is broadcast.
  const greetOk = await waitFor(() => S.greeting.done, 45000);
  assert('a_first_greeting',
    greetOk && S.greeting.sentences > 0 && S.greeting.audioEnds > 0 && S.firstAudioPlayed,
    `greeting sentences-with-chunks=${S.greeting.sentences} audio_end=${S.greeting.audioEnds} first_audio_played=${S.firstAudioPlayed} settled_listening@${S.greeting.doneTs}ms`);

  // --- scenario plan (adaptive on M = actionable words) --------------------
  // features: probes, commands(4), pauseResume, interrupt(announced target),
  // timeout(word); last confirm completes the lesson. Indices are M-space
  // (0 = the resumed cursor word).
  const plan = [];
  const addConfirms = (from, to) => {
    for (let i = from; i <= to; i++) plan.push({ act: 'confirm', word: i });
  };
  if (M >= 5) {
    plan.push({ act: 'probes' }, { act: 'confirm', word: 0 });
    plan.push({ act: 'commands', word: 1 }, { act: 'confirm', word: 1 });
    plan.push({ act: 'pauseResume', word: 2 }, { act: 'confirm', word: 2, interrupt: true });
    plan.push({ act: 'confirm', word: 3 });
    plan.push({ act: 'timeout', word: 4 });
    addConfirms(5, M - 1);
  } else if (M === 4) {
    plan.push({ act: 'probes' }, { act: 'confirm', word: 0 });
    plan.push({ act: 'commands', word: 1 }, { act: 'pauseResume', word: 1 },
      { act: 'confirm', word: 1, interrupt: true });
    plan.push({ act: 'timeout', word: 2 });
    plan.push({ act: 'confirm', word: 3 });
  } else if (M === 3) {
    plan.push({ act: 'probes' }, { act: 'confirm', word: 0, interrupt: true });
    // word 1: interrupted announcement (no ack arm) → commands re-arm the
    // timer → pause/resume → silent timeout drives the advance.
    plan.push({ act: 'commands', word: 1 }, { act: 'pauseResume', word: 1 },
      { act: 'timeout', word: 1 });
    plan.push({ act: 'confirm', word: 2 }); // last word → completion
  } else if (M === 2) {
    plan.push({ act: 'probes' }, { act: 'confirm', word: 0, interrupt: true });
    plan.push({ act: 'commands', word: 1 }, { act: 'pauseResume', word: 1 });
    plan.push({ act: 'confirm', word: 1 });
    // timeout skipped — no spare armed word; recorded as a coverage note
  } else {
    plan.push({ act: 'probes' }, { act: 'commands', word: 0 },
      { act: 'pauseResume', word: 0 }, { act: 'confirm', word: 0 });
    // interrupt + timeout skipped (single-word lesson cannot host them)
  }
  if (M < 5) {
    results.push({ assertion: 'coverage_note', pass: true,
      detail: `actionable words=${M} < 5: full five-feature spread needs ≥5 due words — this run still covers ${M >= 3 ? 'interrupt+timeout' : M === 2 ? 'interrupt' : 'neither'}; rerun on a fuller due batch if needed` });
  }

  const outcomes = { probes: null, commands: null, pauseResume: null, interrupt: null, timeout: null };
  const confirmFiles = ['hao_le.wav', 'keyi_le.wav', 'xiayige.wav'];
  let confirmIdx = 0;
  let interruptedAnnouncedWord = -1;

  for (const step of plan) {
    if (exitCode !== null) return;
    console.log(`act ${step.act}${step.word !== undefined ? ' @word' + step.word : ''} t=${nowMs()}ms state=${S.state}`);
    try {
      if (step.act === 'probes') {
        outcomes.probes = await vadProbeAct();
      } else if (step.act === 'commands') {
        outcomes.commands = {
          repeat: await speakTurn('zaidu.wav', 'listening'),
          slow: await speakTurn('man.wav', 'listening'),
          hint: await speakTurn('buhui.wav', 'listening'),
          meaning: await speakTurn('ciyi.wav', 'listening'),
        };
      } else if (step.act === 'pauseResume') {
        outcomes.pauseResume = await pauseResumeAct();
      } else if (step.act === 'timeout') {
        outcomes.timeout = await timeoutAct();
        if (!outcomes.timeout.advanced) {
          console.log('  timeout act did not advance — continuing (bound reached)');
        }
      } else if (step.act === 'confirm') {
        const file = confirmFiles[confirmIdx++ % confirmFiles.length];
        const isLast = step.word === M - 1;
        if (step.interrupt) {
          const r = await confirmWithInterrupt(file);
          outcomes.interrupt = r;
          if (r && r.targetIndex >= 0) interruptedAnnouncedWord = r.targetIndex;
          if (!r || !r.interrupted) {
            console.log(`  interrupt act incomplete (${r ? r.reason || 'no ack' : 'null'}) — continuing`);
            if (S.state === 'paused') await speakTurn('jixu.wav', 'listening');
            else await waitReady(650, 30000);
          }
        } else {
          await speakTurn(file, 'listening');
        }
        if (isLast) {
          // Completion: summary announcement → lesson_complete → session_ended.
          const ended = await waitFor(() => S.sessionEnded !== null, 60000);
          if (!ended) console.log('  session_ended not observed within 60s of last confirm');
        }
      }
    } catch (e) {
      console.error(`act ${step.act} threw:`, e && e.message);
      results.push({ assertion: `act_${step.act}_error`, pass: false, detail: String(e && e.message) });
    }
  }

  // Post-lesson settle (in case the last act wasn't a confirming confirm).
  if (!S.sessionEnded) {
    await waitFor(() => S.sessionEnded !== null, 30000);
  }
  assert('k_session_ended_reason',
    !!S.sessionEnded && S.sessionEnded.reason === 'lesson_complete',
    `session_ended=${S.sessionEnded ? S.sessionEnded.reason : 'none'}`);

  // --- close --------------------------------------------------------------
  // Natural ending: the server already released the session (internalClose).
  // Per server semantics we do NOT send end_session after lesson_complete —
  // the socket is closed client-side (smoke baseline behavior).
  await sleep(500);
  try { ws.close(); } catch { /* fine */ }
  clearInterval(pingIv);

  // --- server-side evidence + final assertions ----------------------------
  const sessionId = S.ready ? S.ready.session_id : null;
  const serverEvents = sessionId ? extractServerEvents(sessionId) : [];
  const evCount = (name) => serverEvents.filter((e) => e.event === name).length;
  const intents = serverEvents.filter((e) => e.event === 'intent_classified').map((e) => e.intent);
  const review = serverEvents.find((e) => e.event === 'review_written') || null;
  const errEvents = serverEvents.filter((e) => e.event === 'error');
  const speechStarted = evCount('speech_started');
  const wordAnnounced = evCount('word_announced');
  const wordAdvanced = evCount('word_advanced');
  const lessonComplete = evCount('lesson_complete');
  const timeoutFired = evCount('timeout_fired');
  const providerErrEvents = errEvents.filter((e) =>
    e.code === 'provider_failed' || e.attempts !== undefined).length;

  const lessonsAfter = lessonsRowSnapshot();
  const reviewAfter = reviewLogCount();

  // b. VAD probes — split: silence = the 0-second/echo regression check;
  // noise = sustained-energy behavior, reported as observed.
  const p = outcomes.probes;
  assert('b1_vad_silence_zero_false_trigger',
    !!p && p.silenceTurnStarts === 0 && speechStarted === S.turnStarts,
    `silence probe turn_start=${p ? p.silenceTurnStarts : 'n/a'}; client turns=${S.turnStarts} vs server speech_started=${speechStarted} (window ${p ? p.windowMs : '?'}ms)`);
  const noiseTriggers = !!p && p.noiseTurnStarts > 0;
  assert('b2_vad_noise_no_turn',
    !!p && p.noiseTurnStarts === 0,
    `noise probe turn_start=${p ? p.noiseTurnStarts : 'n/a'}${noiseTriggers ? ' — noise fixture (sustained RMS≈0.058) opened a speech segment; StartRMS=0.02 design-consistent but per assertion matrix this is a false-trigger candidate (ASR found nothing, no user-visible effect)' : ''}`);

  // c. invalid API response
  const clientFatal = S.errors.filter((e) =>
    e.code === 'provider_failed' || e.code === 'invalid_message').length;
  assert('c_no_invalid_api_response',
    clientFatal === 0 && providerErrEvents === 0 && evCount('playback_ack_timeout') === 0,
    `client provider/invalid errors=${clientFatal}; server error-events(provider_failed/invalid)=${providerErrEvents}; playback_ack_timeout=${evCount('playback_ack_timeout')}; total server error events=${errEvents.length}`);

  // d. event chain (M-space: this session advanced M words)
  const expectedAnnounced = M - (interruptedAnnouncedWord >= 0 ? 1 : 0);
  assert('d_event_chain_counts',
    wordAnnounced === expectedAnnounced && wordAdvanced === M && lessonComplete === 1 &&
    !!review && S.lessonUpdates.length === M,
    `word_announced=${wordAnnounced} (expect ${expectedAnnounced}${interruptedAnnouncedWord >= 0 ? `, announcement of absolute word ${interruptedAnnouncedWord} was interrupted` : ''}) word_advanced=${wordAdvanced}/${M} lesson_complete=${lessonComplete} review_written=${review ? 1 : 0} client lesson_update=${S.lessonUpdates.length}/${M}`);

  // e. four commands (server intent labels + client no-advance)
  const cmd = outcomes.commands || {};
  const hadIntent = (label) => intents.includes(label);
  const noAdv = (r) => !!r && r.settled === true && r.luDelta === 0;
  assert('e1_repeat_reread', noAdv(cmd.repeat) && hadIntent('repeat'),
    `repeat settled=${noAdv(cmd.repeat)} intent_seen=${hadIntent('repeat')}`);
  assert('e2_slow_reread', noAdv(cmd.slow) && hadIntent('slow'),
    `slow settled=${noAdv(cmd.slow)} intent_seen=${hadIntent('slow')}`);
  assert('e3_hint_no_advance', noAdv(cmd.hint) && hadIntent('hint'),
    `hint settled=${noAdv(cmd.hint)} intent_seen=${hadIntent('hint')} (hint must not advance)`);
  assert('e4_meaning_no_advance', noAdv(cmd.meaning) && hadIntent('meaning'),
    `meaning settled=${noAdv(cmd.meaning)} intent_seen=${hadIntent('meaning')} (meaning must not advance)`);

  // f. pause / filler / resume
  const pr = outcomes.pauseResume;
  assert('f_pause_filler_resume',
    !!pr && pr.pause.settled && pr.stateAfterPause === 'paused' &&
    pr.filler.settled && pr.fillerAdvanced === 0 && pr.stateAfterFiller === 'paused' &&
    pr.resume.settled && pr.stateAfterResume === 'listening' &&
    hadIntent('pause') && hadIntent('resume'),
    `paused@pause=${pr ? pr.stateAfterPause : 'n/a'} filler: turns=${pr ? pr.fillerTurns : '?'} advance=${pr ? pr.fillerAdvanced : '?'} pausedAfter=${pr ? pr.stateAfterFiller : 'n/a'} resumedTo=${pr ? pr.stateAfterResume : 'n/a'} intents pause/resume=${hadIntent('pause')}/${hadIntent('resume')}`);

  // g. interrupt
  const ir = outcomes.interrupt;
  assert('g_interrupt_stops_playback',
    !!ir && ir.interrupted === true && ir.revivedFrames === 0 && ir.lateOldSentences === 0,
    `interrupt_ack=${ir ? !!ir.interruptAck : false} old_epoch_audio_frames_after=${ir ? ir.revivedFrames : 'n/a'} late_old_epoch_sentences=${ir ? ir.lateOldSentences : 'n/a'} (deng tail then settles as its own turn)`);

  // h. timeout
  const to = outcomes.timeout;
  assert('h_timeout_auto_advance',
    !!to && to.advanced && to.turnsDuringWait === 0 && timeoutFired === 1,
    `advanced=${to ? to.advanced : false} silent_wait_ms=${to ? to.waitMs : 'n/a'} turns_during_wait=${to ? to.turnsDuringWait : '?'} server timeout_fired=${timeoutFired}`);

  // i. vocab write
  const requested = review ? review.requested : -1;
  const updated = review ? review.updated : -1;
  const missing = review ? review.missing : -1;
  const rowOk = !!lessonsAfter.lesson_id &&
    lessonsAfter.current_index === S.N &&
    lessonsAfter.version === (lessonsBefore.version || 0) + M;
  const reviewDeltaOk = reviewAfter >= 0 && reviewBefore >= 0 &&
    (reviewAfter - reviewBefore) === updated;
  assert('i_vocab_db_written',
    rowOk && !!review && requested === M && updated >= 0 && updated + missing === requested && reviewDeltaOk,
    `row ${lessonsAfter.lesson_id || '?'}: index ${lessonsAfter.current_index}/${S.N} version ${lessonsAfter.version} (expect ${S.N}/${(lessonsBefore.version || 0) + M}); review requested=${requested}/${M} updated=${updated} missing=${missing}; review_logs delta=${reviewAfter - reviewBefore} (vs updated=${updated})`);

  // extras
  assert('x_transcript_turns_consistent', S.turnStarts === speechStarted,
    `client turn_start=${S.turnStarts} server speech_started=${speechStarted}`);
  assert('x_ack_stream', S.sentAcks >= S.audioEnds - 2,
    `sentence_acks sent=${S.sentAcks} audio_end=${S.audioEnds} (interrupted epoch intentionally left unacked)`);

  writeEvidence(sessionId, serverEvents, 'complete');
  console.log(`\nE2E done: session=${sessionId} duration=${nowMs()}ms pass=${results.filter((r) => r.pass).length}/${results.length}`);
  finish(results.every((r) => r.pass) ? 0 : 2, 'complete');
}

main().catch((e) => {
  console.error('main error:', e && e.message);
  finish(2, 'main_error');
});
