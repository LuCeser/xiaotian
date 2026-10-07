// 13-12 Task 2 Track 2 — SHORT WSS session against the REAL 8443 service,
// purpose: paired-timing sample with the CORRECTED per-turn
// first_audio_played driver behavior (Track 1's rt-7 JSONL predates the fix
// → pairing was 0). Scratch-only tool; never committed; transcripts stay
// local (messages timeline only).
//
// Faithful-browser machinery copied from .scratch/e2e-rt-1312.mjs (always-open
// 20 ms mic pump, sentence_ack gated on real playback duration, per-turn
// first_audio_played keyed by the announcement turn from teacher_sentence
// ids, 10 s pings → clock_offset, RIFF 78-byte data offset).
//
// Scenario (≤3 min, watchdog 150 s):
//   session_init → session_ready →
//     IF session_ended arrives with no actionable word (the day-keyed lesson
//        r0-20261006 is COMPLETE: current_index 10/10 → pkg/rt/turn.go
//        startCompletedSession sends ready + ended(lesson_complete) and
//        closes): record the completed-resume path HONESTLY — no greeting,
//        no audio, no user turns are possible today.
//     ELSE (defensive active-lesson branch): greeting settle → one hao_le
//        confirm (real advance) → settle → end_session → session_ended.
//
// Evidence → .scratch/e2e-1312/track2/:
//   rt-timing-track2.jsonl        TimingLog-schema client events
//   messages-timeline-track2.jsonl full control timeline (local only)
//   server-rt-events-track2.jsonl  this session's msg="rt_event" log lines
//   short-session-results.json    machine-readable outcome
import WebSocket from 'ws';
import fs from 'node:fs';
import path from 'node:path';

const ROOT = path.resolve(import.meta.dirname, '..');
const OUT = path.join(ROOT, '.scratch', 'e2e-1312', 'track2');
fs.mkdirSync(OUT, { recursive: true });
const FIX = path.join(ROOT, '.scratch', 'fixtures-1312');
const SERVER_LOG = path.join(ROOT, 'logs', 'server-8443.err.log');
const URL = 'wss://127.0.0.1:8443/rt';

const TOTAL_MS = 150 * 1000; // watchdog — well under the 3-minute discipline

const t0 = Date.now();
const nowMs = () => Date.now() - t0;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const timing = [];
const tlog = (event, extra = {}) => timing.push({ event, t_ms: nowMs(), ...extra });
const timeline = [];
const rec = (dir, msg) => timeline.push({ t_ms: nowMs(), dir, msg });

const results = [];
function assert(name, pass, detail) {
  results.push({ assertion: name, pass: !!pass, detail });
  console.log(`  [${pass ? 'PASS' : 'FAIL'}] ${name} — ${detail}`);
}

let ws = null;
let finished = false;
const OPEN = WebSocket.OPEN;

const S = {
  ready: null,
  state: '',
  lastStateChangeTs: 0,
  lastTurnStartTs: 0,
  epoch: 0,
  lastTurnId: 0,
  annTurnId: null,
  turnFirstAudioLogged: false,
  curSent: null,
  pendingAcks: new Map(),
  sentAcks: 0,
  binaryFrames: 0,
  audioStarts: 0,
  audioEnds: 0,
  turnStarts: 0,
  lessonUpdates: [],
  sessionEnded: null,
  firstAudioPlayed: false,
  errors: [],
  greeting: null, // {sentences, audioEnds, done}
};

function send(obj) {
  if (!ws || ws.readyState !== OPEN) return;
  rec('out', obj);
  ws.send(JSON.stringify(obj));
}

const mark = () => ({ turns: S.turnStarts, lu: S.lessonUpdates.length, ae: S.audioEnds });

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

async function waitReady(gateMs = 650, timeoutMs = 25000) {
  const ok = await waitFor(() => S.state === 'listening' || S.state === 'paused', timeoutMs);
  if (!ok) return false;
  await sleep(gateMs);
  return S.state === 'listening' || S.state === 'paused';
}

function handleText(m) {
  rec('in', m);
  switch (m.type) {
    case 'session_ready':
      S.ready = m;
      break;
    case 'turn_start':
      S.turnStarts++;
      S.lastTurnStartTs = Date.now();
      S.lastTurnId = m.turn_id;
      S.epoch = m.epoch;
      S.turnFirstAudioLogged = false;
      break;
    case 'teacher_sentence':
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
    case 'state':
      S.state = m.value;
      S.lastStateChangeTs = Date.now();
      if (S.greeting && !S.greeting.done && m.value === 'listening') {
        S.greeting.done = true;
      }
      break;
    case 'error':
      S.errors.push({ code: m.code, message: m.message });
      break;
    case 'session_ended':
      S.sessionEnded = m;
      break;
    case 'pong': {
      const sentAt = S._pingSentAt || Date.now();
      const rtt = Date.now() - sentAt;
      const offsetMs = m.t_server_mono_ns / 1e6 - (sentAt + rtt / 2);
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
  const durMs = s.samples / 24; // s16le mono @ 24 kHz
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
  if (data.length === 0) return;
  const s = S.curSent;
  if (!s) return;
  const now = Date.now();
  if (s.firstChunkTs === null) {
    s.firstChunkTs = now;
    // CORRECTED per-turn first_audio_played (turn = the announcement's own
    // turn, derived from the teacher_sentence id) — the Track 1 fix.
    if (!S.turnFirstAudioLogged) {
      S.turnFirstAudioLogged = true;
      tlog('first_audio_played', { turn_id: S.annTurnId ?? S.lastTurnId, epoch: s.epoch, sentence_id: s.sentenceId });
    }
    S.firstAudioPlayed = true;
    if (S.greeting && !S.greeting.done) S.greeting.sentences++;
  }
  s.samples += data.length / 2;
}

// --- always-open mic pump (browser-faithful) -------------------------------
const micQueue = [];
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
    else if (frame.length < 640) frame = Buffer.concat([frame, Buffer.alloc(640 - frame.length, 0)]);
    ws.send(frame);
  }, 20);
}

function stopMic() { if (micPumpIv) { clearInterval(micPumpIv); micPumpIv = null; } }

function readWavPcm(file) {
  const b = fs.readFileSync(file);
  let off = 12;
  while (off + 8 <= b.length) {
    const id = b.toString('ascii', off, off + 4);
    const sz = b.readUInt32LE(off + 4);
    if (id === 'data') return b.subarray(off + 8, Math.min(off + 8 + sz, b.length));
    off += 8 + sz + (sz % 2);
  }
  return b.subarray(44);
}

async function speak(file) {
  const pcm = readWavPcm(path.join(FIX, file));
  micQueue.push(pcm);
  await waitFor(() => micQueue.length === 0, 30000, 10);
  await sleep(150);
  return Math.round(pcm.length / 32);
}

async function speakTurn(file, endState = 'listening', timeoutMs = 40000) {
  await waitReady(650);
  const m = mark();
  await speak(file);
  let ok = await waitFor(() => S.turnStarts > m.turns, timeoutMs);
  if (!ok) return { settled: false, turnsDelta: 0, luDelta: 0 };
  ok = await waitFor(() =>
    S.turnStarts === m.turns + 1 &&
    S.state === endState &&
    S.lastStateChangeTs > S.lastTurnStartTs, timeoutMs);
  await sleep(300);
  return { settled: ok, turnsDelta: S.turnStarts - m.turns, luDelta: S.lessonUpdates.length - m.lu };
}

function extractServerEvents(sessionId) {
  let size = 0;
  try { size = fs.statSync(SERVER_LOG).size; } catch { return []; }
  const start = Math.max(0, size - 8 * 1024 * 1024);
  const buf = Buffer.alloc(size - start);
  const fd = fs.openSync(SERVER_LOG, 'r');
  try { fs.readSync(fd, buf, 0, buf.length, start); } finally { fs.closeSync(fd); }
  const events = [];
  for (const line of buf.toString('utf8').split('\n')) {
    if (!line.includes('rt_event')) continue;
    try {
      const j = JSON.parse(line);
      if (j && j.msg === 'rt_event' && j.session_id === sessionId) events.push(j);
    } catch { /* torn tail line */ }
  }
  return events;
}

// The service's log writer may lag the wire by a beat (observed: the rt-8
// connected line materialized after the driver's immediate tail read), so
// poll the tail briefly instead of reading once.
async function extractServerEventsPolled(sessionId, timeoutMs = 10000) {
  const dl = Date.now() + timeoutMs;
  let evs = [];
  do {
    evs = extractServerEvents(sessionId);
    if (evs.length > 0) return evs;
    await sleep(500);
  } while (Date.now() < dl);
  return evs;
}

const jl = (arr) => arr.map((x) => JSON.stringify(x)).join('\n') + '\n';

function writeEvidence(sessionId, serverEvents, exitContext) {
  if (finished) return;
  finished = true;
  try {
    fs.writeFileSync(path.join(OUT, 'rt-timing-track2.jsonl'),
      jl(timing.map((t) => ({ session_id: sessionId, ...t }))));
    fs.writeFileSync(path.join(OUT, 'messages-timeline-track2.jsonl'),
      jl(timeline.map((t) => ({ session_id: sessionId, ...t }))));
    fs.writeFileSync(path.join(OUT, 'server-rt-events-track2.jsonl'), jl(serverEvents));
    fs.writeFileSync(path.join(OUT, 'short-session-results.json'), JSON.stringify({
      generated_at: new Date().toISOString(),
      session_id: sessionId,
      lesson_id: S.ready && S.ready.lesson ? S.ready.lesson.id : null,
      words_count: S.ready && S.ready.lesson ? (S.ready.lesson.words || []).length : 0,
      current_index: S.ready && S.ready.lesson ? S.ready.lesson.current_index : null,
      duration_ms: nowMs(),
      exit_context: exitContext,
      path_taken: exitContext === 'completed_resume' ? 'completed_resume' : 'active_lesson_or_other',
      observations: {
        client_turns: S.turnStarts,
        first_audio_played_events: timing.filter((t) => t.event === 'first_audio_played').length,
        clock_offset_events: timing.filter((t) => t.event === 'clock_offset').length,
        sentence_acks_sent: S.sentAcks,
        audio_starts: S.audioStarts,
        audio_ends: S.audioEnds,
        lesson_updates: S.lessonUpdates.length,
        errors: S.errors,
      },
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
  for (const [, t] of S.pendingAcks) clearTimeout(t);
  const sessionId = S.ready ? S.ready.session_id : null;
  writeEvidence(sessionId, sessionId ? extractServerEvents(sessionId) : [], exitContext);
  setTimeout(() => process.exit(code), 200);
}

const watchdog = setTimeout(() => {
  console.error('SHORT-SESSION WATCHDOG fired — dumping partial evidence');
  finish(2, 'watchdog_timeout');
}, TOTAL_MS);
process.on('unhandledRejection', (e) => console.error('unhandledRejection:', e && e.message));
process.on('uncaughtException', (e) => {
  console.error('uncaughtException:', e && e.message);
  finish(2, 'uncaught_exception');
});

async function main() {
  tlog('page_ready');
  tlog('auth_complete', { note: 'node-driver-session_init-consent' });

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
    try { handleText(JSON.parse(data.toString())); } catch { /* ignore */ }
  });
  ws.on('error', (e) => console.error('WS ERROR:', e.message));
  ws.on('close', () => clearInterval(pingIv));

  send({ type: 'session_init', audio: { sampleRate: 16000, format: 's16le', channels: 1 } });
  const readyOk = await waitFor(() => S.ready !== null, 30000);
  if (!readyOk) {
    assert('a_session_ready', false, 'no session_ready within 30s');
    return finish(2, 'no_session_ready');
  }
  const L = S.ready.lesson || {};
  const N = (L.words || []).length;
  const idx = L.current_index || 0;
  assert('a_session_ready', true,
    `session_ready session=${S.ready.session_id} lesson=${L.id} words=${N} current_index=${idx} version=${L.version}`);

  // Completed-resume detection: the day-keyed lesson is fully done
  // (current_index >= words) → pkg/rt turn.go startCompletedSession answers
  // ready + ended(lesson_complete) and closes, BEFORE any greeting audio.
  const endedEarly = await waitFor(() => S.sessionEnded !== null, 8000);
  if (endedEarly && N > 0 && idx >= N) {
    assert('b_path_taken', true,
      `completed-resume path: session_ended(${S.sessionEnded.reason}) arrived with no actionable word (index ${idx}/${N}) — pkg/rt/turn.go startCompletedSession; greeting/confirm impossible today, recorded honestly`);
    assert('c_no_audio_expected', S.audioStarts === 0 && !S.firstAudioPlayed,
      `audio_starts=${S.audioStarts} first_audio_played=${S.firstAudioPlayed} (completed resume plays nothing)`);
    assert('d_no_turns_expected', S.turnStarts === 0,
      `client turns=${S.turnStarts} (uplink turns impossible before close)`);
    await sleep(500);
    try { ws.close(); } catch { /* fine */ }
    clearInterval(pingIv);
    const sid = S.ready ? S.ready.session_id : null;
    const evs = sid ? await extractServerEventsPolled(sid) : [];
    const evCount = (n) => evs.filter((e) => e.event === n).length;
    // Event contract (pkg/rt/turn.go): startCompletedSession emits ONLY
    // `connected` — its internalClose() (L508) marks the session closed, so
    // the transport's Close() (L772-781) idempotently skips the session_end
    // emission. session_end=0 here is the DESIGNED behavior, asserted as such.
    assert('e_server_events_completed_resume',
      evCount('connected') >= 1 && evCount('session_end') === 0,
      `server rt_events: connected=${evCount('connected')} session_end=${evCount('session_end')} (session_end=0 is by design — internalClose pre-marks closed; session=${sid})`);
    finish(0, 'completed_resume');
    clearTimeout(watchdog);
    return;
  }

  // Defensive ACTIVE-lesson branch (only reachable on a fresh day-keyed
  // lesson): greeting → one hao_le confirm → end_session.
  console.log('active lesson detected — greeting + 1 confirm + end_session');
  S.greeting = { sentences: 0, audioEnds: 0, done: false };
  const greetOk = await waitFor(() => S.greeting.done, 45000);
  assert('b_path_taken', true, 'active-lesson path (fresh day-keyed lesson)');
  assert('c_greeting_audible',
    greetOk && S.greeting.sentences > 0 && S.greeting.audioEnds > 0 && S.firstAudioPlayed,
    `greeting sentences=${S.greeting.sentences} audio_ends=${S.greeting.audioEnds} first_audio_played=${S.firstAudioPlayed}`);

  const m = mark();
  const luBefore = S.lessonUpdates.length;
  await speakTurn('hao_le.wav', 'listening');
  const advanced = S.lessonUpdates.length > luBefore;
  assert('d_confirm_advanced', advanced,
    `hao_le confirm: turns=${S.turnStarts - m.turns} lesson_updates=${S.lessonUpdates.length - luBefore}`);
  await waitFor(() => S.state === 'listening' && S.lastStateChangeTs > S.lastTurnStartTs, 20000);

  send({ type: 'end_session' });
  const ended = await waitFor(() => S.sessionEnded !== null || ws.readyState !== OPEN, 20000);
  assert('e_end_session_honored', ended,
    `end_session → session_ended=${S.sessionEnded ? S.sessionEnded.reason : 'close/none'}`);

  clearInterval(pingIv);
  await sleep(300);
  try { ws.close(); } catch { /* fine */ }
  const sid = S.ready ? S.ready.session_id : null;
  const evs = sid ? await extractServerEventsPolled(sid) : [];
  const speechEnded = evs.filter((e) => e.event === 'speech_ended');
  const fap = timing.filter((t) => t.event === 'first_audio_played');
  const overlap = fap.filter((t) => speechEnded.some((e) => e.turn_id === t.turn_id)).length;
  assert('f_pairing_overlap', overlap > 0,
    `client first_audio_played turns=[${fap.map((t) => t.turn_id).join(',')}] server speech_ended turns=[${speechEnded.map((e) => e.turn_id).join(',')}] overlap=${overlap}`);
  finish(0, 'complete');
  clearTimeout(watchdog);
}

main().catch((e) => {
  console.error('main error:', e && e.message);
  finish(2, 'main_error');
});
