// 13-12 Task 1 smoke: real WSS session_init → session_ready (RunDue words) → end_session.
// Probe discipline: LAN pre-acceptance channel, self-signed mkcert → rejectUnauthorized:false.
// Scrath-only tool; NEVER committed (T-13-30: raw evidence stays local).
import WebSocket from 'ws';

const URL = 'wss://127.0.0.1:8443/rt';
const OVERALL_MS = 45000;
const timer = setTimeout(() => { console.error('SMOKE TIMEOUT overall watchdog'); process.exit(1); }, OVERALL_MS);

const ws = new WebSocket(URL, { rejectUnauthorized: false, headers: { Origin: 'https://127.0.0.1:8443' } });
let ready = null;
let ended = false;
let sawListening = false;
let audioEnds = 0;
let acksSent = 0;
let binaryFrames = 0;
const t0 = Date.now();

function send(obj) { ws.send(JSON.stringify(obj)); }

ws.on('error', (e) => { console.error('WS ERROR:', e.message); clearTimeout(timer); process.exit(1); });

ws.on('open', () => {
  console.log('WSS handshake OK in', Date.now() - t0, 'ms');
  send({ type: 'session_init', audio: { sampleRate: 16000, format: 's16le', channels: 1 } });
  console.log('session_init sent');
});

ws.on('message', (data, isBinary) => {
  if (isBinary) { binaryFrames++; return; }
  let m;
  try { m = JSON.parse(data.toString()); } catch { console.error('non-JSON text frame'); return; }
  switch (m.type) {
    case 'session_ready': {
      ready = m;
      const words = m.lesson?.words ?? [];
      console.log('session_ready: session_id=%s lesson_id=%s version=%s words=%d tts=%dHz audio=%d/%s/%d',
        m.session_id, m.lesson?.id, m.lesson?.version, words.length, m.tts?.sampleRate,
        m.audio?.sampleRate, m.audio?.format, m.audio?.channels);
      if (words.length === 0) console.log('WORDS_EMPTY — no due words (user_setup precondition missing)');
      break;
    }
    case 'audio_end': {
      audioEnds++;
      send({ type: 'sentence_ack', sentence_id: m.sentence_id, epoch: m.epoch });
      acksSent++;
      break;
    }
    case 'state':
      if (m.value === 'listening') sawListening = true;
      break;
    case 'error':
      console.log('server error msg: code=%s message=%s', m.code, m.message);
      if (m.code === 'lesson_unavailable' || m.code === 'voice_unavailable') {
        console.error('SMOKE FAIL degraded code=%s', m.code);
        clearTimeout(timer); ws.close(); process.exit(2);
      }
      break;
    case 'session_ended':
      ended = true;
      console.log('session_ended: reason=%s', m.reason);
      // Server keeps the ws open (idle-timeout semantics) — the smoke's
      // assertions are complete here; close client-side and report.
      const wcEnd = ready?.lesson?.words?.length ?? 0;
      console.log('SMOKE RESULT: words=%d audio_ends=%d acks=%d pcm_frames=%d total_ms=%d',
        wcEnd, audioEnds, acksSent, binaryFrames, Date.now() - t0);
      if (wcEnd > 0) { console.log('SMOKE PASS'); clearTimeout(timer); ws.close(); process.exit(0); }
      console.error('SMOKE FAIL: words was empty'); clearTimeout(timer); ws.close(); process.exit(1);
      break;
    default:
      break;
  }
});

// After session_ready lands, wait for the opening greeting to settle
// (listening state or 12s bound), then end the session explicitly.
const settle = setInterval(() => {
  if (!ready) return;
  if (sawListening || Date.now() - t0 > 12000) {
    clearInterval(settle);
    console.log('greeting settled (listening=%s) — sending end_session', sawListening);
    send({ type: 'end_session' });
  }
}, 250);

ws.on('close', () => {
  clearTimeout(timer);
  const dt = Date.now() - t0;
  if (!ready) { console.error('SMOKE FAIL: closed before session_ready'); process.exit(1); }
  const wc = ready.lesson?.words?.length ?? 0;
  console.log('SMOKE RESULT: words=%d ended=%s listening=%s audio_ends=%d acks=%d pcm_frames=%d total_ms=%d',
    wc, ended, sawListening, audioEnds, acksSent, binaryFrames, dt);
  if (wc > 0 && ended) { console.log('SMOKE PASS'); process.exit(0); }
  console.error('SMOKE FAIL: words>0 and clean session_ended required');
  process.exit(1);
});
