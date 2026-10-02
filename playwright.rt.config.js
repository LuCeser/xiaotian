/**
 * Playwright config for the mock /rt tutor e2e (Phase 13, Plan 05).
 *
 * DELIBERATELY SEPARATE from playwright.config.js (review H-1): the legacy
 * config's testDir is tests/e2e with NO testMatch/testIgnore filter and its
 * webServer boots the REAL Go backend via run.ps1 — if rt-loop.spec.js
 * lived there, `npm run e2e` would run mock-protocol tests against the
 * production service. Directory isolation (testDir tests/e2e-rt) keeps the
 * legacy suite untouched: `npx playwright test --list` must NOT list
 * rt-loop (asserted in the plan's acceptance criteria).
 *
 * Local run:  npm run e2e:rt   (or: npx playwright test -c playwright.rt.config.js)
 * Requires:   npx playwright install chromium  (one-time, shared with e2e)
 */
// @ts-check
const { defineConfig } = require('@playwright/test');

// Loopback traffic must never ride the system proxy. Dev machines behind a
// local proxy (Privoxy & co.) export HTTP_PROXY without a loopback NO_PROXY,
// and Playwright's webServer readiness probe + page.request() honor those
// vars — the proxy cannot reach 127.0.0.1 and answers 500, so the suite
// times out waiting for a server that is actually up. Chromium itself
// implicitly bypasses localhost for page/ws traffic; this covers the
// Playwright-side requests. Set at config load, before anything probes.
process.env.NO_PROXY = process.env.NO_PROXY
  ? process.env.NO_PROXY + ',127.0.0.1,localhost'
  : '127.0.0.1,localhost';
process.env.no_proxy = process.env.NO_PROXY;

module.exports = defineConfig({
  testDir: 'tests/e2e-rt',
  // 10 scripted rounds ≈ 12-15 s per test on a slow machine; stay generous.
  timeout: 90_000,
  fullyParallel: false,
  // One mock server, one sessions map: serial execution keeps the
  // per-session state assertions deterministic.
  workers: 1,
  retries: 0,
  maxFailures: process.env.CI ? undefined : 1,
  reporter: [['list']],
  use: {
    baseURL: 'http://127.0.0.1:8099',
    launchOptions: {
      // Auto-grant mic permission and provide a fake audio capture device
      // so getUserMedia resolves headlessly (T-13-08: test-env flags only).
      args: [
        '--use-fake-ui-for-media-stream',
        '--use-fake-device-for-media-stream'
      ]
    }
  },
  webServer: {
    command: 'node tests/e2e-rt/mock-rt-server.js --port 8099',
    url: 'http://127.0.0.1:8099/',
    reuseExistingServer: true,
    timeout: 20_000
  }
});
