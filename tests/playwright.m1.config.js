/**
 * Playwright config for the milestone-1 REAL-server browser line (issue #58
 * A-line, e2e-test-plan §7 step 5; B-layer: browser + real Go service +
 * real Python bridge + real SQLite — ONLY the external MIMO HTTP responses
 * and, in later steps, the browser microphone source are replaced).
 *
 * Deliberately separate from every other config (same rationale as
 * playwright.parent.config.js, review H-1): own ports, own temp state via
 * the cross-platform launcher, own template. It never adopts an existing
 * listener and never reuses the dev 8080 service.
 *
 *   - strict mimo stub   127.0.0.1:18285 (scripted; unplanned traffic fails)
 *   - service            127.0.0.1:18290 / health 18291, started by
 *     service-launcher.js from a binary BUILT FROM THIS CHECKOUT into a
 *     per-run temp root (go build inside the launcher; 180s covers a cold
 *     module cache), voice.baseUrl pinned to the stub above.
 *
 * Local run:  npm run e2e:m1
 * Requires:   npx playwright install chromium (one-time, shared with e2e)
 */
// @ts-check
const { defineConfig } = require('@playwright/test');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const repoRoot = path.resolve(__dirname, '..');
const SERVICE = 'http://127.0.0.1:18290';
const STUB = 'http://127.0.0.1:18285';

// SILENT mic source (plan §2 B-layer: the browser microphone source is one
// of the two replaced pieces). Chromium's fake audio device plays a loud
// repeating tone by default, which would drive the server VAD into
// unscripted ASR calls and fail the strict stub — so the fake device is fed
// a looping all-zero WAV instead. Written at config load (one small file in
// the OS temp dir); the uplink then carries real production-captured
// silence, never a stubbed stream.
const SILENCE_WAV = path.join(os.tmpdir(), 'xiaotian-m1-silence-16k.wav');
{
  const rate = 16000;
  const dataLen = rate * 2 * 2; // 2 s of mono s16le
  const wav = Buffer.alloc(44 + dataLen);
  wav.write('RIFF', 0, 'ascii');
  wav.writeUInt32LE(36 + dataLen, 4);
  wav.write('WAVE', 8, 'ascii');
  wav.write('fmt ', 12, 'ascii');
  wav.writeUInt32LE(16, 16);
  wav.writeUInt16LE(1, 20); // PCM
  wav.writeUInt16LE(1, 22); // mono
  wav.writeUInt32LE(rate, 24);
  wav.writeUInt32LE(rate * 2, 28);
  wav.writeUInt16LE(2, 32);
  wav.writeUInt16LE(16, 34);
  wav.write('data', 36, 'ascii');
  wav.writeUInt32LE(dataLen, 40);
  fs.writeFileSync(SILENCE_WAV, wav);
}

// Loopback traffic must never ride the system proxy (same guard as the
// parent config — see its comment for the Privoxy failure mode).
process.env.NO_PROXY = process.env.NO_PROXY
  ? process.env.NO_PROXY + ',127.0.0.1,localhost'
  : '127.0.0.1,localhost';
process.env.no_proxy = process.env.NO_PROXY;

module.exports = defineConfig({
  testDir: './e2e-m1-browser',
  // Explicit per-spec match: each plan step appends its spec file here, so
  // the m1 line never silently picks up another suite's specs (finish,
  // manual-entry, photo-entry, smoke, subject-playback, subject-scope,
  // voice-input). The run-level service below serves the single read-only
  // smoke test. Every stateful classroom test — steps 11–13 included —
  // starts its own service, temp DB and data root through
  // helpers/spec-service.js (plan §2 每用例独立数据库; the shared
  // instance's lesson rows are first-content-wins per subject, so classroom
  // specs must not share them). Specs reset the shared strict stub at their
  // boundaries, so no assertion depends on file execution order or an
  // earlier test's cleanup.
  testMatch: /(smoke|subject-playback|voice-input|manual-entry|subject-scope|photo-entry|finish)\.spec\.js$/,
  timeout: 60_000,
  retries: 0,
  // The strict vendor stub is shared, so its independently reset scripts
  // remain serialized even though classroom databases are per test.
  workers: 1,
  maxFailures: process.env.CI ? undefined : 1,
  reporter: [['list']],
  use: {
    baseURL: SERVICE,
    launchOptions: {
      args: [
        '--use-fake-ui-for-media-stream', // auto-grant mic permission headless
        '--use-fake-device-for-media-stream', // a mic device must exist headless
        '--use-file-for-fake-audio-capture=' + SILENCE_WAV, // …but a SILENT one
      ],
    },
  },
  webServer: [
    {
      cwd: repoRoot,
      command: 'node tests/e2e-m1-browser/helpers/strict-mimo-stub.js --port 18285',
      url: STUB + '/healthz',
      reuseExistingServer: false, // never adopt a stray 18285 listener
      timeout: 20_000,
      // Without gracefulShutdown Playwright SIGKILLs the webServer process
      // group on teardown — fine for the stateless stub, but the option is
      // set uniformly so every server in this config shuts down on SIGTERM.
      gracefulShutdown: { signal: 'SIGTERM', timeout: 15_000 },
    },
    {
      cwd: repoRoot,
      command: 'node tests/e2e-m1-browser/helpers/service-launcher.js'
        + ' --config tests/e2e-m1-browser/config.m1-e2e.yaml'
        + ' --addr 127.0.0.1:18290 --health-addr 127.0.0.1:18291'
        + ' --voice-base-url ' + STUB + '/v1',
      url: SERVICE + '/',
      reuseExistingServer: false, // never adopt a stray 18290 listener
      // gracefulShutdown makes Playwright SIGTERM the whole group and WAIT:
      // the launcher's signal handler then stops the service AND removes
      // its per-run temp root. Default teardown would SIGKILL the group and
      // leak the root (verified empirically — do not remove).
      gracefulShutdown: { signal: 'SIGTERM', timeout: 15_000 },
      // Includes the launcher's `go build` of cmd/service; 3 minutes
      // covers a cold module cache (first `go run`-style compile).
      timeout: 180_000,
    },
  ],
});
