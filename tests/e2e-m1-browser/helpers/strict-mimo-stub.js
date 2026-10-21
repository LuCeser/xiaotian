// strict-mimo-stub.js — STRICT external-MIMO stand-in for the milestone-1
// browser e2e line (issue #58, e2e-test-plan §2 rule 4). Unlike the loose
// tests/e2e/helpers/mimo-stub.js (whose unmatched calls default to success),
// this stub FAILS every unplanned request so a green run means the real
// service issued exactly the vendor traffic the test scripted:
//
//   - Every POST */chat/completions is classified as exactly one of
//     asr / tts / llm / image by wire shape, and required fields are
//     validated (model type per category, auth header, JSON body).
//   - Each category consumes a per-category FIFO script; script
//     exhaustion, a wrong model type, an unknown path, a bad WAV on ASR
//     input, or an entry whose optional `match` substring does not appear
//     in the prompt text is a violation: HTTP 5xx/4xx to the caller AND a
//     recorded violation the spec asserts against (no silent defaults).
//   - Response wire shapes are reused verbatim from mimo-stub.js.
//
// Control plane (test-only, never called by the service): GET
// /__strict/state, POST /__strict/scripts {asr:[],llm:[],tts:[],image:[]},
// POST /__strict/reset. GET /healthz serves the Playwright readiness probe.
//
// Programmatic: createStrictMimoStub() (see strict-mimo-stub.test.js).
// CLI: node strict-mimo-stub.js --port 18280
'use strict';

const http = require('node:http');

const ASR_MODEL = 'mimo-v2.5-asr';
const TTS_MODEL = 'mimo-v2.5-tts';
// Chat-family model ids (classification LLM, vision): mimo-v2.x-flash/-pro.
const CHAT_MODEL_RE = /^mimo-v2\.\d+(-\d+)?-(flash|pro)$/;

// walkParts returns the first content part carrying key, plus the count of
// parts carrying ANY of the looked-for keys (to reject mixed payloads).
function walkParts(body, keys) {
  let found = null;
  let signals = 0;
  for (const msg of body.messages || []) {
    for (const part of Array.isArray(msg.content) ? msg.content : []) {
      for (const key of keys) {
        if (part && part[key]) {
          signals += 1;
          if (!found) found = part;
        }
      }
    }
  }
  return { found, signals };
}

function promptText(body) {
  return (body.messages || [])
    .map((m) => (typeof m.content === 'string' ? m.content
      : (Array.isArray(m.content) ? m.content.map((p) => (p && p.text) || '').join('\n') : '')))
    .join('\n');
}

// parseWavMeta is a STRICTER sibling of pkg/mimo parseWAV (which only
// needs lenient rate/duration hints for playback metadata): the stub must
// prove the ASR input really is the container the production path wraps
// via pkg/voice.WrapWAV — plain PCM encoding, 16-bit samples, and a fmt
// header whose blockAlign/byteRate agree with channels × rate. A lying
// header (encoding 99, 1-bit samples, misaligned blockAlign…) must fail
// here so the spec sees a violation instead of a false green (review PR
// #62 P2).
function parseWavMeta(b) {
  if (!Buffer.isBuffer(b) || b.length < 12) return null;
  if (b.slice(0, 4).toString('ascii') !== 'RIFF' || b.slice(8, 12).toString('ascii') !== 'WAVE') return null;
  let fmt = null;
  let dataBytes = 0;
  let pos = 12;
  while (pos + 8 <= b.length) {
    const id = b.slice(pos, pos + 4).toString('ascii');
    const size = b.readUInt32LE(pos + 4);
    const body = pos + 8;
    if (body + size > b.length) break; // truncated/corrupt chunk
    if (id === 'fmt ' && size >= 16 && !fmt) {
      fmt = {
        audioFormat: b.readUInt16LE(body),
        channels: b.readUInt16LE(body + 2),
        sampleRateHz: b.readUInt32LE(body + 4),
        byteRate: b.readUInt32LE(body + 8),
        blockAlign: b.readUInt16LE(body + 12),
        bitsPerSample: b.readUInt16LE(body + 14),
      };
    } else if (id === 'data') {
      dataBytes = size;
    }
    pos = body + size + (size & 1); // chunks are word-aligned
  }
  if (!fmt || !fmt.sampleRateHz || !fmt.channels || !dataBytes) return null;
  if (fmt.audioFormat !== 1) return null; // plain PCM, as WrapWAV writes
  if (fmt.bitsPerSample !== 16) return null; // s16le capture path
  if (fmt.blockAlign !== (fmt.channels * fmt.bitsPerSample) / 8) return null;
  if (fmt.byteRate !== fmt.sampleRateHz * fmt.blockAlign) return null;
  if (dataBytes % fmt.blockAlign !== 0) return null; // whole frames only
  return {
    sampleRateHz: fmt.sampleRateHz,
    channels: fmt.channels,
    bitsPerSample: fmt.bitsPerSample,
    blockAlign: fmt.blockAlign,
    byteRate: fmt.byteRate,
    dataBytes,
    durationMs: Math.round((dataBytes * 1000) / fmt.byteRate),
  };
}

function createStrictMimoStub(options = {}) {
  const host = options.host || '127.0.0.1';
  const state = {
    violations: [], // {seq, at, reason, detail} — auth values never recorded
    requests: [], // {seq, at, category, model, ...category facts}
    scripts: { asr: [], llm: [], tts: [], image: [] },
  };
  let seq = 0;

  function violate(reason, detail, res, status) {
    seq += 1;
    state.violations.push({ seq, at: new Date().toISOString(), reason, detail });
    console.error('[strict-mimo-stub] violation #%d: %s %j', seq, reason, detail);
    if (res && !res.headersSent) {
      res.writeHead(status || 500, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ error: { message: 'strict-stub: ' + reason } }));
    }
  }

  function decodeDataUrl(url) {
    if (typeof url !== 'string' || !/^data:[^,]+;base64,/.test(url)) return null;
    return Buffer.from(url.slice(url.indexOf(',') + 1), 'base64');
  }

  // classify validates one chat-completions body and returns
  // {category, facts} or {violation, detail}.
  function classify(body) {
    const audioPart = walkParts(body, ['input_audio']).found;
    const imagePart = walkParts(body, ['image_url']).found;
    const mixed = (audioPart ? 1 : 0) + (imagePart ? 1 : 0) + (body.stream ? 1 : 0);
    if (mixed > 1) return { violation: 'ambiguous request shape' };
    if (body.model === ASR_MODEL || audioPart) {
      if (body.model !== ASR_MODEL) return { violation: 'wrong model for asr shape', detail: { model: body.model } };
      if (!audioPart) return { violation: 'asr model without input_audio part' };
      const wav = decodeDataUrl(audioPart.input_audio && audioPart.input_audio.data);
      const meta = wav && parseWavMeta(wav);
      if (!meta) return { violation: 'asr input is not a valid WAV', detail: { bytes: wav ? wav.length : 0 } };
      return { category: 'asr', facts: { wav: meta } };
    }
    if (body.stream === true) {
      if (body.model !== TTS_MODEL) return { violation: 'wrong model for tts shape', detail: { model: body.model } };
      const text = body.messages && typeof body.messages[0].content === 'string' ? body.messages[0].content : '';
      if (!text) return { violation: 'tts request without sentence text' };
      if (!body.audio || body.audio.format !== 'pcm16') return { violation: 'tts request must ask pcm16' };
      return { category: 'tts', facts: { text } };
    }
    if (imagePart) {
      if (!CHAT_MODEL_RE.test(body.model || '')) {
        return { violation: 'wrong model for image shape', detail: { model: body.model } };
      }
      const img = decodeDataUrl(imagePart.image_url && imagePart.image_url.url);
      if (!img || !img.length) return { violation: 'image part without decodable bytes' };
      return { category: 'image', facts: { imageBytes: img.length } };
    }
    if (!CHAT_MODEL_RE.test(body.model || '')) {
      return { violation: 'unknown chat model', detail: { model: body.model } };
    }
    if (!promptText(body).trim()) return { violation: 'llm request without prompt text' };
    return { category: 'llm', facts: {} };
  }

  function completion(content) {
    return {
      id: 'strict-stub-' + Math.random().toString(36).slice(2),
      object: 'chat.completion',
      choices: [{ index: 0, message: { role: 'assistant', content }, finish_reason: 'stop' }],
    };
  }

  // Step-3 TTS output: chunked NON-ZERO 24kHz s16le PCM (plan §2 rule 5 —
  // pure silence or one tiny chunk cannot carry playback acceptance).
  // synthTonePCM is deterministic (same ms → same bytes) so later corpus
  // steps can key caches on it.
  function synthTonePCM(ms, sampleRateHz = 24000, freq = 440) {
    const n = Math.round((ms * sampleRateHz) / 1000);
    const buf = Buffer.alloc(n * 2);
    for (let i = 0; i < n; i++) {
      buf.writeInt16LE(Math.round(Math.sin((2 * Math.PI * freq * i) / sampleRateHz) * 12000), i * 2);
    }
    return buf;
  }

  // streamTTS writes the SSE reply one chunk at a time so a disconnecting
  // client can really interrupt mid-stream (record.interrupted) and the
  // {cut} entry can destroy the socket after the first chunk — the
  // provider-interruption shape the client must surface as an error.
  function streamTTS(res, record, entry) {
    res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache' });
    let finished = false;
    res.on('close', () => {
      if (!finished) record.interrupted = true;
    });
    const pcm = synthTonePCM(Math.max(1, entry.ms || 600));
    const bytesPerChunk = Math.max(20, entry.chunkMs || 150) * 48; // 24kHz·s16·mono
    let offset = 0;
    let chunkIndex = 0;
    const writeNext = () => {
      if (res.destroyed) return;
      const end = Math.min(offset + bytesPerChunk, pcm.length);
      const data = JSON.stringify({
        choices: [{ index: 0, delta: { audio: { data: pcm.subarray(offset, end).toString('base64') } } }],
      });
      const frame = 'data: ' + data + '\n\n';
      offset = end;
      chunkIndex += 1;
      if (entry.cut) {
        record.cut = true;
        // Destroy only after the chunk has flushed, so the client really
        // receives chunk 1 and then the truncation.
        res.write(frame, () => res.destroy());
        return;
      }
      res.write(frame);
      if (offset < pcm.length) {
        setTimeout(writeNext, 5);
        return;
      }
      finished = true;
      const fin = JSON.stringify({
        choices: [{ index: 0, delta: { audio: { data: '' } }, finish_reason: 'stop' }],
      });
      res.write('data: ' + fin + '\n\ndata: [DONE]\n\n');
      res.end();
    };
    setImmediate(writeNext);
  }

  function handle(req, res) {
    if (req.method === 'GET' && req.url.startsWith('/healthz')) {
      res.writeHead(200, { 'Content-Type': 'text/plain' });
      res.end('ok');
      return;
    }
    if (req.url.startsWith('/__strict/')) {
      if (req.method === 'GET' && req.url === '/__strict/state') {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({
          violations: state.violations,
          requests: state.requests,
          remaining: {
            asr: state.scripts.asr.length,
            llm: state.scripts.llm.length,
            tts: state.scripts.tts.length,
            image: state.scripts.image.length,
          },
        }));
        return;
      }
      if (req.method === 'POST' && req.url === '/__strict/scripts') {
        readBody(req, (buf) => {
          try {
            const def = JSON.parse(buf.toString('utf8'));
            for (const key of ['asr', 'llm', 'tts', 'image']) {
              if (Array.isArray(def[key])) state.scripts[key] = def[key].slice();
            }
            res.writeHead(200, { 'Content-Type': 'application/json' });
            res.end(JSON.stringify({ ok: true, remaining: { asr: state.scripts.asr.length, llm: state.scripts.llm.length, tts: state.scripts.tts.length, image: state.scripts.image.length } }));
          } catch (e) {
            res.writeHead(400, { 'Content-Type': 'application/json' });
            res.end(JSON.stringify({ error: { message: 'bad scripts json' } }));
          }
        });
        return;
      }
      if (req.method === 'POST' && req.url === '/__strict/reset') {
        state.violations.length = 0;
        state.requests.length = 0;
        for (const key of ['asr', 'llm', 'tts', 'image']) state.scripts[key].length = 0;
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: true }));
        return;
      }
      res.writeHead(404, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ error: { message: 'unknown control path' } }));
      return;
    }
    if (req.method !== 'POST' || !req.url.endsWith('/chat/completions')) {
      violate('unknown request', { method: req.method, url: req.url }, res, 404);
      return;
    }
    readBody(req, (buf) => {
      let body;
      try {
        body = JSON.parse(buf.toString('utf8'));
      } catch (e) {
        violate('unparsable json body', { bytes: buf.length }, res, 400);
        return;
      }
      if (!/^Bearer /.test(req.headers.authorization || '')) {
        violate('missing bearer auth', {}, res, 401);
        return;
      }
      const verdict = classify(body);
      if (verdict.violation) {
        violate(verdict.violation, verdict.detail || { model: body.model }, res);
        return;
      }
      const entry = state.scripts[verdict.category].shift();
      if (!entry) {
        violate('script exhausted for ' + verdict.category, { model: body.model }, res);
        return;
      }
      if (entry.match && typeof entry.match === 'string') {
        const hay = verdict.category === 'tts' ? verdict.facts.text : promptText(body);
        if (!hay.includes(entry.match)) {
          violate('script order mismatch for ' + verdict.category, { match: entry.match }, res);
          return;
        }
      }
      seq += 1;
      const record = { seq, at: new Date().toISOString(), category: verdict.category, model: body.model };
      if (verdict.category === 'asr') Object.assign(record, verdict.facts.wav);
      if (verdict.category === 'tts') record.text = verdict.facts.text;
      if (verdict.category === 'image') record.imageBytes = verdict.facts.imageBytes;
      state.requests.push(record);
      console.error('[strict-mimo-stub] %s #%d ok', verdict.category, record.seq);

      if (entry.fail) {
        res.writeHead(entry.fail.status || 500, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ error: { message: 'strict-stub: scripted failure' } }));
        return;
      }
      if (verdict.category === 'tts') {
        streamTTS(res, record, entry);
        return;
      }
      if (verdict.category === 'asr' && entry.empty) {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ choices: [] }));
        return;
      }
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify(completion(entry.content || entry.text || '')));
    });
  }

  function readBody(req, cb) {
    const chunks = [];
    req.on('data', (c) => chunks.push(c));
    req.on('end', () => cb(Buffer.concat(chunks)));
  }

  const server = http.createServer(handle);
  return {
    server,
    state,
    listen(port = 0) {
      return new Promise((resolve, reject) => {
        server.once('error', reject);
        server.listen(port, host, () => resolve({ port: server.address().port }));
      });
    },
    close() {
      return new Promise((resolve) => server.close(resolve));
    },
  };
}

module.exports = { createStrictMimoStub };

if (require.main === module) {
  const args = process.argv.slice(2);
  const i = args.indexOf('--port');
  const stub = createStrictMimoStub();
  stub.listen(i >= 0 ? Number(args[i + 1]) : 18280).then(({ port }) => {
    console.log('[strict-mimo-stub] listening on 127.0.0.1:%d (strict: install scripts via POST /__strict/scripts)', port);
  }).catch((e) => {
    console.error('[strict-mimo-stub] listen failed: %s', e.message);
    process.exit(1);
  });
}
