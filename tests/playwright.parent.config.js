/**
 * Playwright config for the parent-management browser suite against a
 * HERMETIC isolated instance (Issue #2's browser link: browser → real
 * service → real Python → real SQLite).
 *
 * DELIBERATELY SEPARATE from playwright.config.js (same rationale as
 * playwright.rt.config.js, review H-1): the legacy config boots the dev
 * service via run.ps1 with .env loaded, because the student dictation
 * smoke hard-depends on TTS pregeneration succeeding. The parent REST
 * surface needs none of that — real Python + real SQLite, no voice keys,
 * no .env — so this config boots its own throwaway instance:
 *
 *   - ports 127.0.0.1:18080/18081 (no contact with run.ps1's 8080/8081),
 *   - per-run OS-temp DB/data/cache (deleted on exit — every run a fresh
 *     database, nothing ever touches ./.hta-data),
 *   - HTA_PYTHON_INTERPRETER resolved from the repo venvs (same candidate
 *     order as tests/e2e-rt/real/run-real.ps1; override via env).
 *
 * Local run:  npm run e2e:parent
 *             (or: npx playwright test -c tests/playwright.parent.config.js)
 * Requires:   npx playwright install chromium (one-time, shared with e2e)
 */
// @ts-check
const { defineConfig } = require('@playwright/test');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const repoRoot = path.resolve(__dirname, '..');

// Loopback traffic must never ride the system proxy (same guard as
// playwright.rt.config.js — see its comment for the Privoxy failure mode).
process.env.NO_PROXY = process.env.NO_PROXY
  ? process.env.NO_PROXY + ',127.0.0.1,localhost'
  : '127.0.0.1,localhost';
process.env.no_proxy = process.env.NO_PROXY;

// Resolve the venv interpreter BEFORE anything probes the server: the
// service aborts at boot without one, and a bare `python` on PATH may be
// the WindowsApps stub. Candidate order mirrors run-real.ps1.
function resolvePython() {
  if (process.env.HTA_PYTHON_INTERPRETER) {
    return process.env.HTA_PYTHON_INTERPRETER;
  }
  const candidates = [
    path.join(repoRoot, 'venv'),
    path.join(repoRoot, '.venv'),
    path.join(repoRoot, 'third_party', 'python', '.venv'),
  ].flatMap((dir) => [
    path.join(dir, 'Scripts', 'python.exe'), // Windows venv layout
    path.join(dir, 'bin', 'python'),         // POSIX venv layout
  ]);
  const found = candidates.find((cand) => fs.existsSync(cand));
  if (!found) {
    throw new Error(
      'No venv python found (searched venv/.venv/third_party venvs under the repo root). ' +
      'Create one (uv venv --python 3.12 + install -r third_party/python/requirements.txt) ' +
      'or set HTA_PYTHON_INTERPRETER.');
  }
  return found;
}

// Per-run isolated state root. Guard against Playwright re-evaluating this
// config in a worker (a second mkdtemp would desync PARENT_E2E_DB from the
// webServer's vocab path and break family-journey SQLite seeding).
function ensureIsolatedRoot() {
  if (process.env.PARENT_E2E_ROOT && fs.existsSync(process.env.PARENT_E2E_ROOT)) {
    return process.env.PARENT_E2E_ROOT;
  }
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'xiaotian-parent-e2e-'));
  fs.mkdirSync(path.join(root, 'cache', 'audio'), { recursive: true });
  process.env.PARENT_E2E_ROOT = root;
  process.env.PARENT_E2E_DB = path.join(root, 'vocab.db');
  process.on('exit', () => fs.rmSync(root, { recursive: true, force: true }));
  return root;
}
const isolatedRoot = ensureIsolatedRoot();

module.exports = defineConfig({
  testDir: './e2e',
  // Only the parent suites ride this config; the student specs stay on the
  // legacy config (they need the .env-loaded dev service).
  testMatch: /(?:parent-(management|photo-chinese|photo-import|poem-import|poem-recitation|pdf-import|photo-reimport|photo-textbook|textbook-scope|meaning-supplement|uat-gaps)|family-journey)\.spec\.js|pdf-capture\.spec\.js/,
  timeout: 120_000,
  retries: 0,
  // One isolated instance, one vocabulary DB: serialize like the legacy
  // suite (two parents mutating the same unit would interleave asserts).
  workers: 1,
  maxFailures: process.env.CI ? undefined : 1,
  reporter: [['list']],
  use: {
    baseURL: 'http://127.0.0.1:18080',
  },
  webServer: [
    {
      // The EXTERNAL vision boundary for the Issue #3 photo suite: a local
      // OpenAI-compatible stub the service's mimo client is pointed at
      // (voice.baseUrl + the dummy MIMO_API_KEY below). Everything behind
      // the service — Python bridge, SQLite, the static pages — stays real.
      // HTA_CAPTURE_PDF_DIR (Issue #7 regen tool) additionally saves every
      // UNREGISTERED image to that dir before failing it — the pdf-capture
      // spec turns those saved bytes into new page-image fixtures.
      cwd: repoRoot,
      command: 'node tests/e2e/helpers/mimo-stub.js --port 18180 --fixtures tests/e2e/fixtures' +
        (process.env.HTA_CAPTURE_PDF_DIR
          ? ' --save-unregistered ' + process.env.HTA_CAPTURE_PDF_DIR
          : ''),
      url: 'http://127.0.0.1:18180/healthz',
      reuseExistingServer: false,
      timeout: 20_000,
    },
    {
      // Real service, real Python, real SQLite — only the machine-specific
      // bits (interpreter, DB/data/cache dirs) ride env overrides into the
      // per-run temp root. Everything else comes from the committed template.
      cwd: repoRoot,
      command: 'go run ./cmd/service -config tests/e2e/config.parent-e2e.yaml',
      url: 'http://127.0.0.1:18080/',
      reuseExistingServer: false, // never adopt a stray 18080 listener
      env: {
        ...process.env,
        // Pin ports from the committed template — never inherit a stray
        // HTA_SERVER_* override from the launching shell.
        HTA_SERVER_ADDR: '127.0.0.1:18080',
        HTA_SERVER_HEALTHADDR: '127.0.0.1:18081',
        HTA_PYTHON_INTERPRETER: resolvePython(),
        HTA_PYTHON_VOCABDBPATH: path.join(isolatedRoot, 'vocab.db'),
        HTA_PYTHON_DATADIR: isolatedRoot,
        HTA_PYTHON_CACHEDIR: path.join(isolatedRoot, 'cache', 'audio'),
        HTA_PYTHON_MANIFESTDIR: path.join(isolatedRoot, 'cache'),
        HTA_PYTHON_ENVFILE: path.join(isolatedRoot, 'dictation.env'),
        HTA_TTS_SESSIONDIR: path.join(isolatedRoot, 'sessions'),
        // Issue #3: dummy key turns the shared mimo client ON against the
        // local stub (config.parent-e2e.yaml voice.baseUrl). Never a real
        // secret — it authenticates to 127.0.0.1:18180 only.
        MIMO_API_KEY: 'parent-e2e-local-stub-key',
      },
      // First `go run` compiles the whole module; 3 minutes covers a cold cache.
      timeout: 180_000,
    },
  ],
});
