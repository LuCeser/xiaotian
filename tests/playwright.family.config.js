/**
 * Playwright config for family force-mode + residual browser gates that
 * need HTA_RT_FORCESTUDENTMODE / a dedicated isolated instance.
 *
 * Ports 18280/18281 (+ mimo stub 18380) — no contact with parent e2e 18080,
 * L3 real 8790, mock 8099, or production.
 *
 * Local run: npm run e2e:family
 */
// @ts-check
const { defineConfig } = require('@playwright/test');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const repoRoot = path.resolve(__dirname, '..');

process.env.NO_PROXY = process.env.NO_PROXY
  ? process.env.NO_PROXY + ',127.0.0.1,localhost'
  : '127.0.0.1,localhost';
process.env.no_proxy = process.env.NO_PROXY;

function resolvePython() {
  if (process.env.HTA_PYTHON_INTERPRETER) {
    return process.env.HTA_PYTHON_INTERPRETER;
  }
  const candidates = [
    path.join(repoRoot, 'venv'),
    path.join(repoRoot, '.venv'),
    path.join(repoRoot, 'third_party', 'python', '.venv'),
  ].flatMap((dir) => [
    path.join(dir, 'Scripts', 'python.exe'),
    path.join(dir, 'bin', 'python'),
  ]);
  const found = candidates.find((cand) => fs.existsSync(cand));
  if (!found) {
    throw new Error(
      'No venv python found for e2e:family. Create third_party/python/.venv or set HTA_PYTHON_INTERPRETER.',
    );
  }
  return found;
}

const isolatedRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'xiaotian-family-e2e-'));
fs.mkdirSync(path.join(isolatedRoot, 'cache', 'audio'), { recursive: true });
process.on('exit', () => fs.rmSync(isolatedRoot, { recursive: true, force: true }));

module.exports = defineConfig({
  testDir: './e2e',
  testMatch: /family-force-mode\.spec\.js/,
  timeout: 90_000,
  retries: 0,
  workers: 1,
  maxFailures: process.env.CI ? undefined : 1,
  reporter: [['list']],
  use: {
    baseURL: 'http://127.0.0.1:18280',
    launchOptions: {
      args: [
        '--use-fake-ui-for-media-stream',
        '--use-fake-device-for-media-stream',
      ],
    },
  },
  webServer: [
    {
      cwd: repoRoot,
      command: 'node tests/e2e/helpers/mimo-stub.js --port 18380 --fixtures tests/e2e/fixtures',
      url: 'http://127.0.0.1:18380/healthz',
      reuseExistingServer: false,
      timeout: 20_000,
    },
    {
      cwd: repoRoot,
      command: 'go run ./cmd/service -config tests/e2e/config.family-e2e.yaml',
      url: 'http://127.0.0.1:18280/',
      reuseExistingServer: false,
      env: {
        ...process.env,
        HTA_PYTHON_INTERPRETER: resolvePython(),
        HTA_PYTHON_VOCABDBPATH: path.join(isolatedRoot, 'vocab.db'),
        HTA_PYTHON_DATADIR: isolatedRoot,
        HTA_PYTHON_CACHEDIR: path.join(isolatedRoot, 'cache', 'audio'),
        HTA_PYTHON_MANIFESTDIR: path.join(isolatedRoot, 'cache'),
        HTA_PYTHON_ENVFILE: path.join(isolatedRoot, 'dictation.env'),
        HTA_TTS_SESSIONDIR: path.join(isolatedRoot, 'sessions'),
        HTA_RT_FORCESTUDENTMODE: 'true',
        MIMO_API_KEY: 'family-e2e-local-stub-key',
      },
      timeout: 180_000,
    },
  ],
});
