// mimo-stub.js — local OpenAI-compatible stand-in for the EXTERNAL mimo
// vision service, used by the parent photo-import browser e2e (Issue #3).
//
// The Issue #3 test boundary: browser tests may replace ONLY the external
// recognition/generation service with fixed responses; every write and
// read between the browser, the real Go service, the real Python bridge
// and the real SQLite database stays untouched. This stub is that external
// boundary: the service's mimo client is pointed at it via
// voice.baseUrl (tests/e2e/config.parent-e2e.yaml) plus a dummy
// MIMO_API_KEY (tests/playwright.parent.config.js webServer env).
//
// Contract:
//   POST */chat/completions with an image_url data part (vision request):
//     the decoded image bytes are matched against the fixture PNG/JPEGs in
//     --fixtures; a byte-identical match answers with the matching
//     <name>.reply.json's content as a plain chat completion; an image with
//     NO registered reply answers 500 (the "recognition service failed"
//     path the UI must surface and allow retrying).
//     Issue #7 additions:
//       --save-unregistered <dir> — an unmatched image's bytes are written
//         to <dir>/unreg-<seq>.jpg BEFORE the 500, so the browser-rendered
//         PDF page images (whose bytes only the real Chromium canvas can
//         produce) can be captured once and registered as fixtures.
//       <name>.failcount marker — a sibling file whose TEXT is the number
//         of LEADING 500s to serve before the canned reply takes over,
//         exercising the page-level fail → 重试该页 → succeed path. The
//         count must cover the service's own vision retries (mimo client
//         defaults to 4 attempts total, so the marker says 4).
//   POST */chat/completions without an image (LLM/ASR/TTS-shaped calls):
//     Issue #8 text-generation fixtures FIRST: every text-*.reply.json in
//     --fixtures carrying {"match": "<substring>", "content": "<reply>"}
//     is checked against the request's concatenated prompt text; the FIRST
//     fixture whose match substring appears answers with its content
//     (optional sibling text-*.failcount = leading 500s, same semantics
//     as the vision marker — cover the client's 4 total attempts).
//     Unmatched text calls still get the generic completion — good enough
//     for incidental classroom traffic after the assertions the tests
//     care about.
//   GET /healthz: 200 (the Playwright webServer readiness probe).
//
// Run: node tests/e2e/helpers/mimo-stub.js --port 18180 --fixtures tests/e2e/fixtures
'use strict';

const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');

const args = process.argv.slice(2);
function argValue(name, fallback) {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : fallback;
}
const port = Number(argValue('--port', '18180'));
const fixturesDir = path.resolve(
  argValue('--root', process.cwd()), argValue('--fixtures', 'tests/e2e/fixtures'));
const saveUnregistered = argValue('--save-unregistered', '');
if (saveUnregistered) {
  fs.mkdirSync(saveUnregistered, { recursive: true });
}

// One entry per registered fixture: exact image bytes + the canned
// assistant content from the sibling .reply.json (+ the failcount marker).
const registered = [];
for (const name of fs.readdirSync(fixturesDir)) {
  if (!/\.(png|jpe?g)$/i.test(name)) continue;
  const replyFile = path.join(fixturesDir, name.replace(/\.(png|jpe?g)$/i, '.reply.json'));
  if (!fs.existsSync(replyFile)) continue;
  const failcountFile = path.join(
    fixturesDir, name.replace(/\.(png|jpe?g)$/i, '.failcount'));
  let failcount = 0;
  if (fs.existsSync(failcountFile)) {
    failcount = Number(fs.readFileSync(failcountFile, 'utf8').trim()) || 0;
  }
  registered.push({
    name,
    bytes: fs.readFileSync(path.join(fixturesDir, name)),
    content: JSON.parse(fs.readFileSync(replyFile, 'utf8')).content,
    failcount: failcount,
    hits: 0,
  });
}
let unregSeq = 0;

// Text-generation fixtures (Issue #8): text-*.reply.json files carrying
// {match, content} (+ optional failcount sibling). Registered eagerly like
// the image fixtures; the FIRST substring match on the request's prompt
// text wins, so each e2e scenario keys its fixture on a word unique to
// that scenario's supplement request.
const textRegistered = [];
for (const name of fs.readdirSync(fixturesDir)) {
  if (!/^text-.*\.reply\.json$/i.test(name)) continue;
  let def;
  try {
    def = JSON.parse(fs.readFileSync(path.join(fixturesDir, name), 'utf8'));
  } catch (e) {
    console.error('[mimo-stub] skipping unparsable %s: %s', name, e.message);
    continue;
  }
  if (!def || typeof def.match !== 'string' || typeof def.content !== 'string') {
    console.error('[mimo-stub] skipping %s (need string match+content)', name);
    continue;
  }
  const failcountFile = path.join(fixturesDir, name.replace(/\.reply\.json$/i, '.failcount'));
  let failcount = 0;
  if (fs.existsSync(failcountFile)) {
    failcount = Number(fs.readFileSync(failcountFile, 'utf8').trim()) || 0;
  }
  textRegistered.push({ name, match: def.match, content: def.content, failcount, hits: 0 });
}

// promptText flattens a chat-completions body's message contents into one
// comparable string (string contents verbatim; content parts keep their
// .text fields — the generate call's user turn is plain text).
function promptText(body) {
  return (body.messages || [])
    .map((msg) => {
      if (typeof msg.content === 'string') return msg.content;
      if (Array.isArray(msg.content)) {
        return msg.content
          .map((part) => (part && typeof part.text === 'string' ? part.text : ''))
          .join('\n');
      }
      return '';
    })
    .join('\n');
}

// findImagePart walks a chat-completions body for the first image_url data
// part and returns its decoded bytes (null when the request has no image).
function findImagePart(body) {
  for (const msg of body.messages || []) {
    const parts = Array.isArray(msg.content) ? msg.content : [];
    for (const part of parts) {
      const url = part && part.image_url && part.image_url.url;
      if (typeof url === 'string' && url.startsWith('data:')) {
        const b64 = url.slice(url.indexOf(',') + 1);
        return Buffer.from(b64, 'base64');
      }
    }
  }
  return null;
}

function completion(content) {
  return {
    id: 'stub-' + Math.random().toString(36).slice(2),
    object: 'chat.completion',
    choices: [{ index: 0, message: { role: 'assistant', content }, finish_reason: 'stop' }],
  };
}

const server = http.createServer((req, res) => {
  if (req.method === 'GET' && req.url.startsWith('/healthz')) {
    res.writeHead(200, { 'Content-Type': 'text/plain' });
    res.end('ok');
    return;
  }
  if (req.method !== 'POST' || !req.url.endsWith('/chat/completions')) {
    res.writeHead(404, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ error: { message: 'not found' } }));
    return;
  }
  const chunks = [];
  req.on('data', (c) => chunks.push(c));
  req.on('end', () => {
    let body;
    try {
      body = JSON.parse(Buffer.concat(chunks).toString('utf8'));
    } catch (e) {
      res.writeHead(400, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ error: { message: 'bad json' } }));
      return;
    }
    const image = findImagePart(body);
    if (!image) {
      const hit = textRegistered.find((r) => promptText(body).includes(r.match));
      if (hit) {
        hit.hits += 1;
        if (hit.failcount && hit.hits <= hit.failcount) {
          // The Issue #8 generation-retry fixture: lead with 500s covering
          // the client's own retries, then answer — the UI-level 重试 then
          // succeeds.
          console.error('[mimo-stub] text failcount %d/%d: %s', hit.hits, hit.failcount, hit.name);
          res.writeHead(500, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ error: { message: 'stub: text failcount ' + hit.hits } }));
          return;
        }
        console.error('[mimo-stub] text reply: %s', hit.name);
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify(completion(hit.content)));
        return;
      }
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify(completion('好了')));
      return;
    }
    const hit = registered.find((r) => r.bytes.equals(image));
    if (!hit) {
      // An unregistered upload: the external service "failed" — the UI
      // must show the reason and allow retry (Issue #3 acceptance). With
      // --save-unregistered the bytes land on disk first, so browser-
      // rendered page images can be captured and registered (Issue #7).
      if (saveUnregistered) {
        unregSeq += 1;
        const out = path.join(saveUnregistered, 'unreg-' + String(unregSeq).padStart(3, '0') + '.jpg');
        fs.writeFileSync(out, image);
        console.error('[mimo-stub] saved unregistered image: %s', out);
      }
      console.error('[mimo-stub] no registered reply for a %d-byte image', image.length);
      res.writeHead(500, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ error: { message: 'stub: image not recognized' } }));
      return;
    }
    hit.hits += 1;
    if (hit.failcount && hit.hits <= hit.failcount) {
      // The Issue #7 page-retry fixture: lead with the configured number
      // of 500s (covering the service's own vision retries), then answer
      // the canned reply — the UI-level 重试该页 then succeeds.
      console.error('[mimo-stub] failcount %d/%d: %s', hit.hits, hit.failcount, hit.name);
      res.writeHead(500, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ error: { message: 'stub: failcount ' + hit.hits } }));
      return;
    }
    console.error('[mimo-stub] vision reply: %s', hit.name);
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify(completion(hit.content)));
  });
});

server.listen(port, '127.0.0.1', () => {
  console.log('[mimo-stub] listening on 127.0.0.1:%d, %d image + %d text fixture reply(ies) from %s',
    port, registered.length, textRegistered.length, fixturesDir);
});
