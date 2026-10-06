/**
 * .scratch/pw-real-1312.config.js — Playwright config for the REAL-channel
 * /tutor/ UI verification (13-12 Task 2 Track 2). Scratch-only; the repo's
 * playwright.rt.config.js (mock, tests/e2e-rt) stays untouched.
 *
 * Target: the already-running production service https://127.0.0.1:8443
 * (pid 50472) — NO webServer here; the suite must never start or stop it.
 *
 * Env minefields honored:
 *  - Privoxy hijacks loopback for proxied clients → NO_PROXY gets 127.0.0.1
 *    at config load, before anything probes (same defense as the mock rt
 *    config; Chromium itself bypasses localhost implicitly).
 *  - Self-signed cert → ignoreHTTPSErrors.
 *  - getUserMedia headless → fake UI (auto-grant) + fake device. NO
 *    --use-file-for-fake-audio-capture: today's day-keyed lesson is COMPLETE
 *    (startCompletedSession short-circuit), the classroom never opens an
 *    uplink turn, so a looping fixture mic would only feed silence-turns
 *    into a session that closes instantly.
 */
// @ts-check
const { defineConfig } = require('@playwright/test');
const path = require('node:path');

process.env.NO_PROXY = process.env.NO_PROXY
  ? process.env.NO_PROXY + ',127.0.0.1,localhost'
  : '127.0.0.1,localhost';
process.env.no_proxy = process.env.NO_PROXY;

module.exports = defineConfig({
  testDir: path.join(__dirname, 'e2e-1312', 'track2'),
  testMatch: '**/*.spec.js',
  timeout: 60_000,
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [['list']],
  outputDir: path.join(__dirname, 'e2e-1312', 'track2', 'test-results'),
  use: {
    baseURL: 'https://127.0.0.1:8443',
    ignoreHTTPSErrors: true,
    screenshot: 'on',
    trace: 'retain-on-failure',
    launchOptions: {
      args: [
        '--use-fake-ui-for-media-stream',
        '--use-fake-device-for-media-stream',
      ],
    },
  },
});
