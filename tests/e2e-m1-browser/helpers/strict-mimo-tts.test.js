// strict-mimo-tts.test.js — step-3 verification for the strict stub's
// chunked TTS output (issue #58, e2e-test-plan §7 step 3): the SSE reply
// must carry MULTIPLE non-zero 24kHz pcm16 chunks, a stop terminator and
// [DONE]; scripted failure, mid-stream cut and client-abort shapes are
// exercised too. Run: node --test tests/e2e-m1-browser/helpers/strict-mimo-tts.test.js
'use strict';

const assert = require('node:assert/strict');
const http = require('node:http');
const { test } = require('node:test');
const { createStrictMimoStub } = require('./strict-mimo-stub.js');

function request(port, method, path, body, headers = {}) {
  return new Promise((resolve, reject) => {
    const req = http.request({ host: '127.0.0.1', port, method, path, headers }, (res) => {
      const chunks = [];
      res.on('data', (c) => chunks.push(c));
      // A cut stream ends without a proper FIN: settle on close/error with
      // whatever bytes arrived so the test can assert the truncation.
      res.on('aborted', () => resolve({ status: res.statusCode, text: Buffer.concat(chunks).toString('utf8') }));
      res.on('end', () => resolve({ status: res.statusCode, text: Buffer.concat(chunks).toString('utf8') }));
    });
    req.on('error', (e) => e.code === 'ECONNRESET'
      ? resolve({ status: 0, text: '', reset: true })
      : reject(e));
    if (body !== undefined) req.end(body);
    else req.end();
  });
}

const ttsBody = (text) => JSON.stringify({
  model: 'mimo-v2.5-tts',
  messages: [{ role: 'assistant', content: text }],
  audio: { format: 'pcm16', voice: '冰糖', sample_rate: 16000 },
  stream: true,
});

const post = (port, body) => request(port, 'POST', '/v1/chat/completions', body, {
  'Content-Type': 'application/json',
  Authorization: 'Bearer dummy',
});

// parseSSE splits the raw reply into decoded events; audioChunks keeps the
// base64-decoded PCM of every delta event that carries data.
function parseSSE(text) {
  const events = text.split('\n\n').filter(Boolean).map((l) => l.replace(/^data: /, ''));
  const audioChunks = [];
  let fin = null;
  let done = false;
  for (const ev of events) {
    if (ev === '[DONE]') { done = true; continue; }
    const parsed = JSON.parse(ev);
    const delta = parsed.choices && parsed.choices[0] && parsed.choices[0].delta;
    if (delta && delta.audio && delta.audio.data) audioChunks.push(Buffer.from(delta.audio.data, 'base64'));
    if (parsed.choices && parsed.choices[0].finish_reason === 'stop') fin = parsed;
  }
  return { events, audioChunks, fin, done };
}

async function withStub(t, ttsScripts) {
  const stub = createStrictMimoStub();
  const { port } = await stub.listen(0);
  stub.state.scripts.tts.push(...ttsScripts);
  t.after(() => stub.close());
  return { stub, port };
}

test('default entry streams multiple non-zero 24kHz PCM chunks then stops', async (t) => {
  const { stub, port } = await withStub(t, [{}]); // defaults: 600ms, 150ms chunks
  const res = await post(port, ttsBody('请写：苹果'));
  assert.equal(res.status, 200);
  const sse = parseSSE(res.text);
  assert.equal(sse.audioChunks.length, 4); // 600ms / 150ms
  const pcm = Buffer.concat(sse.audioChunks);
  assert.equal(pcm.length, 600 * 48); // 24kHz · s16le · mono → ms·48 bytes
  // Non-zero requirement: every chunk carries actual samples, and the
  // stream is not silence (plan §2 rule 5).
  for (const [i, chunk] of sse.audioChunks.entries()) {
    assert.ok(chunk.some((b) => b !== 0), 'chunk ' + i + ' is all-zero');
  }
  assert.ok(pcm.some((b) => b !== 0));
  assert.ok(sse.fin, 'finish_reason=stop terminator present');
  assert.equal(sse.fin.choices[0].delta.audio.data, '');
  assert.ok(sse.done, '[DONE] sentinel present');
  assert.deepEqual(stub.state.violations, []);
});

test('chunkMs and ms drive the chunk count and total length', async (t) => {
  const { stub, port } = await withStub(t, [{ ms: 400, chunkMs: 100 }, { ms: 400, chunkMs: 100 }]);
  const res = await post(port, ttsBody('下一个'));
  const sse = parseSSE(res.text);
  assert.equal(sse.audioChunks.length, 4);
  assert.equal(Buffer.concat(sse.audioChunks).length, 400 * 48);
  // Deterministic generator: same duration → identical bytes (future
  // corpus cache keys rely on it).
  const again = await post(port, ttsBody('再读一遍'));
  assert.deepEqual(Buffer.concat(parseSSE(again.text).audioChunks), Buffer.concat(sse.audioChunks));
});

test('scripted failure answers HTTP 500 and records the request', async (t) => {
  const { stub, port } = await withStub(t, [{ fail: true }]);
  const res = await post(port, ttsBody('请写：好了'));
  assert.equal(res.status, 500);
  assert.match(res.text, /scripted failure/);
  assert.equal(stub.state.requests[0].category, 'tts');
  assert.deepEqual(stub.state.violations, []);
});

test('cut entry destroys the stream after the first chunk without [DONE]', async (t) => {
  const { stub, port } = await withStub(t, [{ cut: true }]);
  const res = await post(port, ttsBody('请写：语文'));
  const sse = parseSSE(res.text);
  assert.equal(sse.audioChunks.length, 1); // one chunk, then the socket died
  assert.ok(!sse.fin, 'no stop terminator on a cut stream');
  assert.ok(!sse.done, 'no [DONE] on a cut stream');
  assert.equal(stub.state.requests[0].cut, true);
  assert.deepEqual(stub.state.violations, []);
});

test('client abort mid-stream is recorded and the stub keeps serving', async (t) => {
  const { stub, port } = await withStub(t, [{ ms: 3000, chunkMs: 100 }, {}]);
  await new Promise((resolve, reject) => {
    const req = http.request({
      host: '127.0.0.1', port, method: 'POST', path: '/v1/chat/completions',
      headers: { 'Content-Type': 'application/json', Authorization: 'Bearer dummy' },
    }, (res) => {
      res.once('data', () => req.destroy()); // abort as soon as chunk 1 lands
      res.on('error', () => resolve());
      res.on('close', () => resolve());
    });
    req.on('error', () => resolve());
    req.end(ttsBody('长句测试'));
  });
  await new Promise((r) => setTimeout(r, 50)); // let the stub notice the close
  assert.equal(stub.state.requests[0].interrupted, true);
  assert.equal(stub.state.requests[0].category, 'tts');
  // The stub survived: the next scripted request still answers normally.
  const next = await post(port, ttsBody('后续请求'));
  assert.equal(next.status, 200);
  assert.ok(parseSSE(next.text).done);
  assert.deepEqual(stub.state.violations, []);
});
