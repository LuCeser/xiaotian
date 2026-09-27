/**
 * Playwright browser-E2E configuration — student dictation web UI.
 *
 * The service under test is the REAL Go backend started by the repo's dev
 * launcher (run.ps1): it loads .env (TTS credentials), kills any stale :8080
 * listener, runs `go run ./cmd/service -config config.yaml`, and tees output
 * to server.log. Static files come from pkg/web/static at route "/".
 *
 * Local run:  npm run e2e   (or: npx playwright test)
 * Requires:   npx playwright install chromium  (one-time)
 */
// @ts-check
const { defineConfig } = require('@playwright/test');

module.exports = defineConfig({
  testDir: 'tests/e2e',
  // Session start waits on real FSRS + TTS pregeneration (pregenTimeout is
  // 120s server-side); keep per-test budget generous.
  timeout: 120_000,
  retries: 0,
  // One backend, one vocab pool: two students starting sessions concurrently
  // race for the same due cards (a locked card never serves twice), so the
  // browser suite must run strictly serially.
  workers: 1,
  // Stop on first failure — each test drives the same live service state.
  maxFailures: process.env.CI ? undefined : 1,
  reporter: [['list']],
  use: {
    baseURL: 'http://localhost:8080',
    // The service is a LAN dev server; default actionability checks are
    // enough, no need to trace videos on failure for now.
  },
  webServer: {
    // Repo-root dev launcher: loads .env, frees :8080, starts the service,
    // tees logs to server.log (read it when the webServer never comes up).
    command: 'pwsh -NoProfile -File run.ps1',
    url: 'http://localhost:8080/',
    reuseExistingServer: true,
    // First `go run` compiles the whole module; 3 minutes covers a cold cache.
    timeout: 180_000,
  },
});
