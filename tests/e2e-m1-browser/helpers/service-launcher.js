// service-launcher.js — cross-platform isolated launcher for the milestone-1
// browser e2e line (issue #58, e2e-test-plan §7 step 4; replaces the
// PowerShell-only tests/e2e-rt/real/run-real.ps1 approach for this line).
// Isolation follows docs/E2E-ISOLATION-SPEC.md's three primitives, all per
// launch:
//   1. temp state root — one OS-temp dir per launch; every python.*Dir,
//      the shared SQLite DB (Go falls back to python.vocabDBPath when
//      database.path is empty, D-12) and the TTS session dir live inside.
//      Inherited HTA_* env is scrubbed so a stray exported
//      HTA_DATABASE_PATH (or any other override) can't redirect state
//      outside the root. stop() removes ONLY this mkdtemp-created root
//      (path-guarded).
//   2. dual loopback ports — explicit args or auto-allocated free ports;
//      never the dev 8080 pair or another suite's ports.
//   3. no real secrets — MIMO_API_KEY is ALWAYS the local dummy value
//      (overriding any inherited real key: it must authenticate against
//      the strict stub only, and never reach stub logs).
//
// The service is started from a binary built from THIS checkout
// (buildServiceBinary), so a stale bin/service can't answer the tests.
//
// Python interpreter: env override → repo venv candidates → bare
// "python3". Unlike playwright.parent.config.js we do NOT throw when no
// venv exists: NewBridge is a pure constructor (no boot-time spawn), so
// start/stop and non-python endpoints work with a bare interpreter;
// python-backed endpoints (RunDue etc.) fail loudly at use — later specs
// that need them must point HTA_PYTHON_INTERPRETER at a real venv.
//
// Programmatic: buildServiceBinary/launchService (see
// service-launcher.test.js). CLI (Playwright webServer):
//   node service-launcher.js --config tests/e2e-m1-browser/config.m1-e2e.yaml \
//     [--binary path] [--addr 127.0.0.1:18290] [--health-addr 127.0.0.1:18291] \
//     [--voice-base-url http://127.0.0.1:18280/v1]
'use strict';

const fs = require('node:fs');
const http = require('node:http');
const net = require('node:net');
const os = require('node:os');
const path = require('node:path');
const { spawn, spawnSync } = require('node:child_process');

const DUMMY_MIMO_KEY = 'm1-e2e-local-stub-key';

function resolvePython(repoRoot) {
  if (process.env.HTA_PYTHON_INTERPRETER) return process.env.HTA_PYTHON_INTERPRETER;
  const candidates = [path.join(repoRoot, 'venv'), path.join(repoRoot, '.venv'),
    path.join(repoRoot, 'third_party', 'python', '.venv')]
    .flatMap((dir) => [path.join(dir, 'Scripts', 'python.exe'), path.join(dir, 'bin', 'python')]);
  return candidates.find((cand) => fs.existsSync(cand)) || 'python3';
}

// buildServiceBinary compiles the current checkout's cmd/service so tests
// never run a stale binary. Throws with the compiler output on failure.
function buildServiceBinary(repoRoot, outPath) {
  const res = spawnSync('go', ['build', '-o', outPath, './cmd/service'], { cwd: repoRoot, encoding: 'utf8' });
  if (res.status !== 0) {
    throw new Error('go build ./cmd/service failed (' + res.status + '):\n' + (res.stderr || ''));
  }
  return outPath;
}

// freePort grabs one loopback port via a listen(0)/close probe (a small
// TOCTOU window remains by design; pin explicit ports when determinism
// matters).
function freePort() {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.listen(0, '127.0.0.1', () => {
      const { port } = srv.address();
      srv.close(() => resolve(port));
    });
    srv.once('error', reject);
  });
}

function httpGet(url, timeoutMs) {
  return new Promise((resolve) => {
    const req = http.get(url, (res) => {
      res.resume();
      res.on('end', () => resolve(res.statusCode));
    });
    req.on('error', () => resolve(0));
    req.setTimeout(timeoutMs, () => { req.destroy(); resolve(0); });
  });
}

// waitForReady polls the public root until it answers 2xx; bails out
// early with the captured stderr when the child dies first.
async function waitForReady(url, proc, tail, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (proc.exitCode !== null) {
      throw new Error('service exited before ready (code ' + proc.exitCode + '):\n' + tail.join(''));
    }
    const status = await httpGet(url, 2000);
    if (status >= 200 && status < 300) return;
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error('service not ready at ' + url + ' within ' + timeoutMs + 'ms:\n' + tail.join(''));
}

async function launchService(options) {
  const repoRoot = path.resolve(options.repoRoot);
  const template = path.resolve(repoRoot, options.template);
  if (!fs.existsSync(template)) throw new Error('config template not found: ' + template);
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'xiaotian-m1-e2e-'));
  const binary = options.binary || path.join(root, 'service');
  if (!options.binary) buildServiceBinary(repoRoot, binary);

  const addr = options.addr || ('127.0.0.1:' + (await freePort()));
  const healthAddr = options.healthAddr || ('127.0.0.1:' + (await freePort()));
  // No inherited HTA_* rides along (review PR #62 P1): a stray exported
  // HTA_DATABASE_PATH would otherwise make the Go side open that file at
  // boot (database.path beats python.vocabDBPath in cmd/service) while
  // Python keeps the temp one — touching real data and splitting the
  // shared DB. Scrub case-insensitively (Windows env lookup ignores
  // case), then set every launcher-owned value explicitly below.
  const env = {};
  for (const [key, value] of Object.entries(process.env)) {
    if (!key.toUpperCase().startsWith('HTA_')) env[key] = value;
  }
  Object.assign(env, {
    HTA_SERVER_ADDR: addr,
    HTA_SERVER_HEALTHADDR: healthAddr,
    HTA_PYTHON_INTERPRETER: resolvePython(repoRoot),
    // One temp SQLite for BOTH sides (D-03): Go reads database.path
    // (HTA_DATABASE_PATH), Python reads python.vocabDBPath. Both MUST
    // name the same file, so pin them together.
    HTA_DATABASE_PATH: path.join(root, 'vocab.db'),
    HTA_PYTHON_VOCABDBPATH: path.join(root, 'vocab.db'),
    HTA_PYTHON_DATADIR: root,
    HTA_PYTHON_CACHEDIR: path.join(root, 'cache', 'audio'),
    HTA_PYTHON_MANIFESTDIR: path.join(root, 'cache'),
    HTA_PYTHON_ENVFILE: path.join(root, 'dictation.env'),
    HTA_TTS_SESSIONDIR: path.join(root, 'sessions'),
    // Always the local dummy — a real inherited key must never ride along.
    MIMO_API_KEY: DUMMY_MIMO_KEY,
  });
  // voice.baseUrl is pinned by the config template unless explicitly
  // overridden; any inherited HTA_VOICE_BASEURL was scrubbed above.
  if (options.voiceBaseUrl) env.HTA_VOICE_BASEURL = options.voiceBaseUrl;

  const tail = [];
  const proc = spawn(binary, ['-config', template], { cwd: repoRoot, env, stdio: ['ignore', 'pipe', 'pipe'] });
  const forward = (stream, tag) => stream.on('data', (c) => {
    const text = c.toString('utf8');
    tail.push(text);
    if (tail.length > 200) tail.shift();
    process.stderr.write('[' + tag + '] ' + text);
  });
  forward(proc.stderr, 'm1-service');
  forward(proc.stdout, 'm1-service:out');
  proc.on('exit', (code, signal) => {
    if (!stopping) console.error('[m1-launcher] service exited: code=%s signal=%s', code, signal);
  });

  const url = 'http://' + addr + '/';
  let stopping = false;
  await waitForReady(url, proc, tail, options.readyTimeoutMs || 30000);

  // removeRoot deletes ONLY the mkdtemp-created root (path-guarded) and is
  // safe to call repeatedly.
  const removeRoot = () => {
    const guard = path.join(os.tmpdir(), 'xiaotian-m1-e2e-');
    if (path.resolve(root).startsWith(path.resolve(guard))) {
      fs.rmSync(root, { recursive: true, force: true });
    }
  };
  // The exit hook matters when the runner kills the whole tree (Playwright's
  // tree-kill also signals the service child directly): the child can be
  // gone by the time stop() runs, and the root must STILL be removed. Only
  // a SIGKILL of the launcher itself can leave the root behind.
  process.on('exit', () => {
    if (proc.exitCode === null) proc.kill('SIGKILL');
    removeRoot();
  });
  let stopped = false;
  const stop = async () => {
    if (stopped) return;
    stopped = true;
    if (proc.exitCode === null) {
      stopping = true;
      proc.kill('SIGTERM');
      await new Promise((resolve) => {
        const timer = setTimeout(() => { proc.kill('SIGKILL'); resolve(); }, 5000);
        proc.once('exit', () => { clearTimeout(timer); resolve(); });
      });
    }
    removeRoot();
  };
  return { proc, root, addr, healthAddr, url, healthUrl: 'http://' + healthAddr + '/health', stop };
}

module.exports = { buildServiceBinary, launchService, resolvePython, DUMMY_MIMO_KEY };

if (require.main === module) {
  const args = process.argv.slice(2);
  const argValue = (name, fallback) => {
    const i = args.indexOf(name);
    return i >= 0 ? args[i + 1] : fallback;
  };
  const repoRoot = path.resolve(__dirname, '..', '..', '..');
  const options = {
    repoRoot,
    template: argValue('--config', path.join(__dirname, '..', 'config.m1-e2e.yaml')),
    binary: argValue('--binary', ''),
    addr: argValue('--addr', ''),
    healthAddr: argValue('--health-addr', ''),
    voiceBaseUrl: argValue('--voice-base-url', ''),
  };
  launchService(options).then((handle) => {
    console.log('[m1-launcher] ready %s (health %s, root %s)', handle.url, handle.healthUrl, handle.root);
    const shutdown = () => handle.stop().then(() => process.exit(0));
    process.on('SIGINT', shutdown);
    process.on('SIGTERM', shutdown);
  }).catch((e) => {
    console.error('[m1-launcher] launch failed: %s', e.message);
    process.exit(1);
  });
}
