// strict-mimo-stub.test.js — step-2 verification for the strict vendor
// stub (issue #58, e2e-test-plan §7 step 2): unknown requests, script
// exhaustion and wrong model types must FAIL; ASR input must be a valid
// WAV; scripted asr/llm/tts/image replies reuse the mimo-stub.js wire
// shapes. Run: node --test tests/e2e-m1-browser/helpers/strict-mimo-stub.test.js
'use strict';

const assert = require('node:assert/strict');
const http = require('node:http');
const { test } = require('node:test');
const { createStrictMimoStub } = require('./strict-mimo-stub.js');

// makeWav builds a minimal RIFF/WAVE container around pcm (mirrors the
// 16kHz s16le mono the /rt ASR adapter wraps via voice.WrapWAV).
function makeWav(pcm, sampleRateHz = 16000, channels = 1) {
  const header = Buffer.alloc(44);
  header.write('RIFF', 0, 'ascii');
  header.writeUInt32LE(36 + pcm.length, 4);
  header.write('WAVE', 8, 'ascii');
  header.write('fmt ', 12, 'ascii');
  header.writeUInt32LE(16, 16);
  header.writeUInt16LE(1, 20); // PCM
  header.writeUInt16LE(channels, 22);
  header.writeUInt32LE(sampleRateHz, 24);
  header.writeUInt32LE(sampleRateHz * 2 * channels, 28);
  header.writeUInt16LE(16, 32);
  header.writeUInt16LE(channels, 34);
  header.write('data', 36, 'ascii');
  header.writeUInt32LE(pcm.length, 40);
  return Buffer.concat([header, pcm]);
}

function request(port, method, path, body, headers = {}) {
  return new Promise((resolve, reject) => {
    const req = http.request({ host: '127.0.0.1', port, method, path, headers }, (res) => {
      const chunks = [];
      res.on('data', (c) => chunks.push(c));
      res.on('end', () => resolve({ status: res.statusCode, text: Buffer.concat(chunks).toString('utf8') }));
    });
    req.on('error', reject);
    if (body !== undefined) req.end(body);
    else req.end();
  });
}

const json = (port, method, path, obj, auth = 'Bearer dummy') =>
  request(port, method, path, JSON.stringify(obj), {
    'Content-Type': 'application/json',
    ...(auth ? { Authorization: auth } : {}),
  });

const asrBody = (wav) => ({
  model: 'mimo-v2.5-asr',
  asr_options: { language: 'zh' },
  messages: [{
    role: 'user',
    content: [{ type: 'input_audio', input_audio: { data: 'data:audio/wav;base64,' + wav.toString('base64') } }],
  }],
});

const llmBody = (text) => ({
  model: 'mimo-v2.6-flash',
  messages: [{ role: 'user', content: text }],
});

const ttsBody = (text) => ({
  model: 'mimo-v2.5-tts',
  messages: [{ role: 'assistant', content: text }],
  audio: { format: 'pcm16', voice: '冰糖', sample_rate: 16000 },
  stream: true,
});

async function withStub(t, scripts) {
  const stub = createStrictMimoStub();
  const { port } = await stub.listen(0);
  for (const [key, entries] of Object.entries(scripts || {})) stub.state.scripts[key].push(...entries);
  t.after(() => stub.close());
  return { stub, port };
}

test('scripted ASR reply requires a valid WAV and records its facts', async (t) => {
  const { stub, port } = await withStub(t, { asr: [{ text: '好了' }] });
  const res = await json(port, 'POST', '/v1/chat/completions', asrBody(makeWav(Buffer.alloc(3200))));
  assert.equal(res.status, 200);
  const parsed = JSON.parse(res.text);
  assert.equal(parsed.choices[0].message.role, 'assistant');
  assert.equal(parsed.choices[0].message.content, '好了');
  assert.equal(parsed.choices[0].finish_reason, 'stop');
  assert.deepEqual(stub.state.violations, []);
  assert.equal(stub.state.requests[0].category, 'asr');
  assert.equal(stub.state.requests[0].sampleRateHz, 16000);
  assert.equal(stub.state.requests[0].durationMs, 100);
});

test('ASR input that is not a WAV fails and is recorded as a violation', async (t) => {
  const { stub, port } = await withStub(t, { asr: [{ text: '好了' }] });
  const res = await json(port, 'POST', '/v1/chat/completions', asrBody(Buffer.from('not a wav at all')));
  assert.equal(res.status, 500);
  assert.match(res.text, /not a valid WAV/);
  assert.equal(stub.state.violations.length, 1);
  assert.match(stub.state.violations[0].reason, /valid WAV/);
  assert.equal(stub.state.requests.length, 0); // failed input is never answered
});

test('scripted llm and image replies, and the match guard catches wrong order', async (t) => {
  const { stub, port } = await withStub(t, {
    llm: [{ content: '{"intent":"confirm"}', match: '分类' }],
    image: [{ content: 'helpful\nfriendly' }],
  });
  const llm = await json(port, 'POST', '/v1/chat/completions', llmBody('请分类这句话'));
  assert.equal(llm.status, 200);
  assert.equal(JSON.parse(llm.text).choices[0].message.content, '{"intent":"confirm"}');
  // Same category, next entry has a match guard the prompt will miss.
  stub.state.scripts.llm.push({ content: 'x', match: '不存在的关键词' });
  const bad = await json(port, 'POST', '/v1/chat/completions', llmBody('另一句'));
  assert.equal(bad.status, 500);
  assert.match(stub.state.violations.at(-1).reason, /order mismatch/);

  const image = await json(port, 'POST', '/v1/chat/completions', {
    model: 'mimo-v2.6-flash',
    messages: [{
      role: 'user',
      content: [
        { type: 'text', text: '识别词表' },
        { type: 'image_url', image_url: { url: 'data:image/jpeg;base64,' + Buffer.from('fakejpg').toString('base64') } },
      ],
    }],
  });
  assert.equal(image.status, 200);
  assert.equal(JSON.parse(image.text).choices[0].message.content, 'helpful\nfriendly');
  assert.equal(stub.state.requests.filter((r) => r.category === 'image').length, 1);
});

test('wrong model type, unknown path and missing auth are violations', async (t) => {
  const { stub, port } = await withStub(t, { llm: [{ content: 'x' }], asr: [{ text: 'y' }] });
  const wrongModel = await json(port, 'POST', '/v1/chat/completions', { ...llmBody('hi'), model: 'mimo-v2.5-tts' });
  assert.equal(wrongModel.status, 500);
  assert.match(stub.state.violations.at(-1).reason, /unknown chat model/);
  const unknown = await request(port, 'POST', '/v1/embeddings', '{}', { 'Content-Type': 'application/json' });
  assert.equal(unknown.status, 404);
  assert.match(stub.state.violations.at(-1).reason, /unknown request/);
  const noAuth = await json(port, 'POST', '/v1/chat/completions', llmBody('hi'), null);
  assert.equal(noAuth.status, 401);
  assert.match(stub.state.violations.at(-1).reason, /bearer auth/);
  assert.equal(stub.state.requests.length, 0);
});

test('script exhaustion fails instead of defaulting to success', async (t) => {
  const { stub, port } = await withStub(t, {});
  const res = await json(port, 'POST', '/v1/chat/completions', llmBody('好了'));
  assert.equal(res.status, 500);
  assert.match(res.text, /script exhausted for llm/);
  assert.equal(stub.state.violations.length, 1);
});

test('TTS streams the SSE shape with a stop terminator and [DONE]', async (t) => {
  const { stub, port } = await withStub(t, { tts: [{}] });
  const res = await json(port, 'POST', '/v1/chat/completions', ttsBody('请写：苹果'));
  assert.equal(res.status, 200);
  const events = res.text.split('\n\n').filter(Boolean).map((l) => l.replace(/^data: /, ''));
  const fin = JSON.parse(events[events.length - 2]);
  assert.equal(fin.choices[0].finish_reason, 'stop');
  assert.equal(fin.choices[0].delta.audio.data, '');
  assert.equal(events[events.length - 1], '[DONE]');
  assert.equal(stub.state.requests[0].category, 'tts');
  assert.equal(stub.state.requests[0].text, '请写：苹果');
});

test('control plane installs scripts, reports state and resets', async (t) => {
  const { stub, port } = await withStub(t);
  const install = await json(port, 'POST', '/__strict/scripts', { llm: [{ content: 'ok' }] });
  assert.equal(install.status, 200);
  assert.equal(JSON.parse(install.text).remaining.llm, 1);
  const ok = await json(port, 'POST', '/v1/chat/completions', llmBody('问'));
  assert.equal(ok.status, 200);
  const state = await request(port, 'GET', '/__strict/state');
  assert.equal(JSON.parse(state.text).remaining.llm, 0);
  assert.equal(JSON.parse(state.text).requests.length, 1);
  const reset = await json(port, 'POST', '/__strict/reset', {});
  assert.equal(reset.status, 200);
  assert.deepEqual(stub.state.requests, []);
  assert.deepEqual(stub.state.violations, []);
  const health = await request(port, 'GET', '/healthz');
  assert.equal(health.status, 200);
});
