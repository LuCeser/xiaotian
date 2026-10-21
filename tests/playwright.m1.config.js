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
const path = require('node:path');

const repoRoot = path.resolve(__dirname, '..');
const SERVICE = 'http://127.0.0.1:18290';
const STUB = 'http://127.0.0.1:18285';

// Loopback traffic must never ride the system proxy (same guard as the
// parent config — see its comment for the Privoxy failure mode).
process.env.NO_PROXY = process.env.NO_PROXY
  ? process.env.NO_PROXY + ',127.0.0.1,localhost'
  : '127.0.0.1,localhost';
process.env.no_proxy = process.env.NO_PROXY;

module.exports = defineConfig({
  testDir: './e2e-m1-browser',
  // Explicit per-spec match: each plan step appends its spec file here, so
  // the m1 line never silently picks up another suite's specs.
  testMatch: /smoke\.spec\.js$/,
  timeout: 60_000,
  retries: 0,
  // One isolated instance + one vocabulary DB: serialize (two actors
  // mutating the same unit would interleave asserts).
  workers: 1,
  maxFailures: process.env.CI ? undefined : 1,
  reporter: [['list']],
  use: {
    baseURL: SERVICE,
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
