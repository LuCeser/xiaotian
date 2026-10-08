// Canary for the always-open mic pump: stream noise-2s through the pump; if
// the pump works, the server VAD opens+closes a segment → turn_start arrives
// (chat intent, no advance possible). end_session before the 60s word timer.
import WebSocket from 'ws';
import fs from 'node:fs';
import path from 'node:path';

const ROOT = path.resolve(import.meta.dirname, '..');
const FIX = path.join(ROOT, '.scratch', 'fixtures-1312');
const URL = 'wss://127.0.0.1:8443/rt';
const t0 = Date.now();
const now = () => Date.now() - t0;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const ws = new WebSocket(URL, { rejectUnauthorized: false, headers: { Origin: 'https://127.0.0.1:8443' } });
const send = (o) => { console.log(now(), 'out', o.type); ws.send(JSON.stringify(o)); };
let turn = 0, transcript = 0;

// --- the pump under test ---
const micQueue = [];
const SILENCE = Buffer.alloc(640, 0);
const pump = setInterval(() => {
  if (ws.readyState !== WebSocket.OPEN) return;
  let frame = Buffer.alloc(0);
  while (micQueue.length > 0 && frame.length < 640) {
    const head = micQueue[0];
    const take = Math.min(640 - frame.length, head.length);
    frame = Buffer.concat([frame, head.subarray(0, take)]);
    if (take === head.length) micQueue.shift(); else micQueue[0] = head.subarray(take);
  }
  if (frame.length === 0) frame = SILENCE;
  else if (frame.length < 640) frame = Buffer.concat([frame, Buffer.alloc(640 - frame.length, 0)]);
  ws.send(frame);
}, 20);

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

ws.on('open', async () => {
  console.log(now(), 'open');
  send({ type: 'session_init', audio: { sampleRate: 16000, format: 's16le', channels: 1 } });
  await sleep(9000); // greeting plays
  console.log(now(), 'speaking noise-2s via pump…');
  micQueue.push(readWavPcm(path.join(FIX, 'noise-2s.wav')));
  await sleep(5000);
  console.log(now(), `RESULT: turns=${turn} transcripts=${transcript}`);
  send({ type: 'end_session' });
  await sleep(800);
  clearInterval(pump);
  process.exit(turn > 0 ? 0 : 3);
});

ws.on('message', (data, isBinary) => {
  if (isBinary) return;
  try {
    const m = JSON.parse(data.toString());
    if (m.type === 'turn_start') { turn++; console.log(now(), 'TURN_START', m.turn_id); }
    if (m.type === 'transcript') { transcript++; console.log(now(), 'transcript received'); }
    if (m.type === 'state') console.log(now(), 'state', m.value);
    if (m.type === 'session_ended') console.log(now(), 'ended', m.reason);
    if (m.type === 'error') console.log(now(), 'error', m.code);
    if (m.type === 'audio_end') send({ type: 'sentence_ack', sentence_id: m.sentence_id, epoch: m.epoch });
  } catch (e) {
    console.log(now(), 'NON-JSON text frame:', data.length, 'B', data.toString('hex').slice(0, 48), JSON.stringify(data.toString('utf8').slice(0, 40)));
  }
});
ws.on('error', (e) => { console.error(now(), 'ws error', e.message); process.exit(1); });
setTimeout(() => { console.error(now(), 'probe watchdog'); process.exit(2); }, 40000);
