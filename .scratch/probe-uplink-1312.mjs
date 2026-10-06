// Diagnostic probe: does the server's VAD react to OUR binary uplink?
// Safe-by-design: end_session before the 60s word timer can fire; no
// advance possible. Also captures raw bytes of any unparseable text frame.
import WebSocket from 'ws';
import fs from 'node:fs';
import path from 'node:path';

const URL = 'wss://127.0.0.1:8443/rt';
const FIX = path.join(ROOT(), '.scratch', 'fixtures-1312');
function ROOT() { return path.resolve(import.meta.dirname, '..'); }

const t0 = Date.now();
const now = () => Date.now() - t0;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const ws = new WebSocket(URL, { rejectUnauthorized: false, headers: { Origin: 'https://127.0.0.1:8443' } });
const send = (o) => { console.log(now(), 'out', JSON.stringify(o).slice(0, 80)); ws.send(JSON.stringify(o)); };
let sawTurn = 0, sawTranscript = 0, states = [];

ws.on('open', async () => {
  console.log(now(), 'open');
  send({ type: 'session_init', audio: { sampleRate: 16000, format: 's16le', channels: 1 } });
  // greeting ~5-8s
  await sleep(9000);
  console.log(now(), '=== streaming noise-2s (subarray views, 640B/20ms) ===');
  await stream('noise-2s.wav', 640, false);
  await sleep(2500);
  console.log(now(), `after variant A: turns=${sawTurn} transcripts=${sawTranscript} states=${states.join(',')}`);
  console.log(now(), '=== streaming noise-2s again (Buffer.copy, 960B/30ms) ===');
  await stream('noise-2s.wav', 960, true);
  await sleep(2500);
  console.log(now(), `after variant B: turns=${sawTurn} transcripts=${sawTranscript} states=${states.join(',')}`);
  console.log(now(), '=== streaming hao_le.wav (subarray, 640B/20ms) ===');
  await stream('hao_le.wav', 640, false);
  await sleep(3000);
  console.log(now(), `after hao_le: turns=${sawTurn} transcripts=${sawTranscript} states=${states.join(',')}`);
  send({ type: 'end_session' });
  await sleep(1000);
  process.exit(0);
});

async function stream(file, frame, copy) {
  let pcm = fs.readFileSync(path.join(FIX, file)).subarray(78); // TRUE data offset
  if (copy) pcm = Buffer.from(pcm);
  const start = Date.now();
  let i = 0;
  for (let off = 0; off < pcm.length; off += frame, i++) {
    if (ws.readyState !== WebSocket.OPEN) break;
    ws.send(pcm.subarray(off, Math.min(off + frame, pcm.length)));
    const due = start + (i + 1) * (copy ? 30 : 20);
    const w = due - Date.now();
    if (w > 0) await sleep(w);
  }
  console.log(now(), `streamed ${file} frames=${i} bytes=${pcm.length} copy=${copy}`);
}

ws.on('message', (data, isBinary) => {
  if (isBinary) { console.log(now(), 'in <binary', data.length, 'B>'); return; }
  try {
    const m = JSON.parse(data.toString());
    if (m.type === 'turn_start') sawTurn++;
    if (m.type === 'transcript') sawTranscript++;
    if (m.type === 'state') states.push(m.value + '@' + now());
    if (m.type === 'session_ended') console.log(now(), 'ended:', m.reason);
    if (m.type === 'audio_end') send({ type: 'sentence_ack', sentence_id: m.sentence_id, epoch: m.epoch });
  } catch {
    console.log(now(), 'in <NON-JSON', data.length, 'B>', data.toString('hex').slice(0, 64), JSON.stringify(data.toString('utf8').slice(0, 48)));
  }
});
ws.on('error', (e) => { console.error(now(), 'ws error', e.message); process.exit(1); });
setTimeout(() => { console.error(now(), 'probe watchdog'); process.exit(2); }, 45000);
