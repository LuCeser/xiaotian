// 13-12 local E2E: generate student-voice command fixtures via Volcengine TTS
// (same API contract as third_party/python/pregenerate_dictation.py _tts_volcengine).
// Scratch-only; NEVER committed (T-13-30). Output: .scratch/fixtures-1312/*.wav (16k s16le mono).
import { readFileSync, writeFileSync, mkdirSync, existsSync, unlinkSync } from 'node:fs';
import { randomUUID } from 'node:crypto';
import { execFileSync } from 'node:child_process';

const FFMPEG = 'D:\\tools\\ffmpeg-6.1-full\\bin\\ffmpeg.exe';
const TTS_URL = 'https://openspeech.bytedance.com/api/v3/tts/unidirectional';
// CHINESE_CONFIG "fragments" mode pairing (proven in production): seed-tts-1.0
// resource+model with the beijingxiaoye voice. seed-tts-2.0 resource rejects
// this voice ("resource ID is mismatched with speaker related resource").
const SPEAKER = 'zh_male_beijingxiaoye_emo_v2_mars_bigtts';
const RESOURCE_ID = 'seed-tts-1.0';
const MODEL = 'seed-tts-1.0';
const OUT_DIR = new URL('./fixtures-1312/', import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1');

const COMMANDS = [
  { slug: 'hao_le', text: '好了。' },
  { slug: 'keyi_le', text: '可以了。' },
  { slug: 'xiayige', text: '下一个。' },
  { slug: 'zaidu', text: '再读一遍。' },
  { slug: 'man', text: '读慢一点。' },
  { slug: 'buhui', text: '不会写。' },
  { slug: 'xiuxi', text: '休息一下。' },
  { slug: 'jixu', text: '继续。' },
  { slug: 'ciyi', text: '这个词是什么意思？' },
  { slug: 'filler', text: '今天天气真好。' },
  { slug: 'deng', text: '等一下，我还在写。' },
];

function loadEnv() {
  const env = {};
  const raw = readFileSync(new URL('../.env', import.meta.url), 'utf8');
  for (const line of raw.split(/\r?\n/)) {
    const m = line.match(/^\s*([A-Z0-9_]+)\s*=\s*(.*)\s*$/);
    if (m) env[m[1]] = m[2].replace(/^["']|["']$/g, '');
  }
  return env;
}

const env = loadEnv();
if (!env.VOLCENGINE_TTS_API_KEY) { console.error('no VOLCENGINE_TTS_API_KEY in .env'); process.exit(1); }

async function ttsOne(text) {
  const headers = {
    'Content-Type': 'application/json',
    'X-Api-Resource-Id': RESOURCE_ID,
    'X-Api-Request-Id': randomUUID(),
    'X-Api-Key': env.VOLCENGINE_TTS_API_KEY,
    'X-Api-App-Key': 'aGjiRDfUWi',
  };
  const payload = {
    user: { uid: 'e2e-fixture-1312' },
    req_params: {
      text,
      speaker: SPEAKER,
      model: MODEL,
      audio_params: { format: 'mp3', sample_rate: 24000 },
    },
  };
  const resp = await fetch(TTS_URL, {
    method: 'POST',
    headers,
    body: JSON.stringify(payload),
    signal: AbortSignal.timeout(30000),
  });
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
  const body = await resp.text();
  const chunks = [];
  for (const line of body.split(/\r?\n/)) {
    if (!line.trim()) continue;
    let chunk;
    try { chunk = JSON.parse(line); } catch { continue; }
    const code = chunk.code;
    if (code != null && code !== 0 && code !== 20000000) {
      throw new Error(`API code=${code} msg=${chunk.message ?? ''}`);
    }
    if (chunk.data) chunks.push(Buffer.from(chunk.data, 'base64'));
  }
  const audio = Buffer.concat(chunks);
  if (!audio.length) throw new Error('no audio data');
  return audio;
}

mkdirSync(OUT_DIR, { recursive: true });
let fail = 0;
for (const { slug, text } of COMMANDS) {
  const mp3 = OUT_DIR + slug + '.mp3';
  const wav = OUT_DIR + slug + '.wav';
  try {
    const audio = await ttsOne(text);
    writeFileSync(mp3, audio);
    execFileSync(FFMPEG, ['-y', '-loglevel', 'error', '-i', mp3, '-ar', '16000', '-ac', '1', '-sample_fmt', 's16', wav], { timeout: 30000 });
    const sz = readFileSync(wav).length;
    console.log(`OK ${slug} mp3=${audio.length}B wav=${sz}B`);
    unlinkSync(mp3);
  } catch (e) {
    fail++;
    console.error(`FAIL ${slug}: ${e.message}`);
  }
}

// Local (no API) VAD-probe assets: 3s pure silence + 2s pink noise, 16k s16le mono.
try {
  execFileSync(FFMPEG, ['-y', '-loglevel', 'error', '-f', 'lavfi', '-i', 'anullsrc=r=16000:cl=mono', '-t', '3', '-sample_fmt', 's16', OUT_DIR + 'silence-3s.wav'], { timeout: 30000 });
  execFileSync(FFMPEG, ['-y', '-loglevel', 'error', '-f', 'lavfi', '-i', 'anoisesrc=r=16000:color=pink:amplitude=0.3', '-t', '2', '-sample_fmt', 's16', OUT_DIR + 'noise-2s.wav'], { timeout: 30000 });
  console.log('OK silence-3s.wav noise-2s.wav');
} catch (e) {
  fail++;
  console.error(`FAIL lavfi assets: ${e.message}`);
}

console.log(fail === 0 ? 'GEN PASS' : `GEN FAIL (${fail})`);
process.exit(fail === 0 ? 0 : 1);
