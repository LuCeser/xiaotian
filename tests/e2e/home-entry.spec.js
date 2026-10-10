/**
 * tests/e2e/home-entry.spec.js — family home entry e2e (Phase 13.3 Plan 04,
 * ROADMAP success criterion 4), run against the REAL Go backend
 * (tests/playwright.config.js: webServer = run.ps1 on :8080).
 *
 * /home.html is a pure static file served by http.FileServer over
 * pkg/web/static — zero route changes (PATTERNS §9). It carries the two
 * family doors (→ /tutor/ and → /parent.html) and NO gate: consensus
 * decision 2 keeps the family surface free of PIN/session/permission
 * elements, so the page must not even contain an <input> or a <script>.
 *
 * The legacy / page (index.html + index-banner.js) is the explicitly
 * labeled RT-10 degraded fallback and must stay byte-untouched — pinned
 * here behaviorally (title + banner + /tutor/ link) AND via a git
 * working-tree assertion scoped to EXACTLY those two source files.
 */
// @ts-check
const { test, expect } = require('@playwright/test');
const { execFileSync } = require('node:child_process');
const path = require('node:path');

// tests/e2e/ → repo root. Derived from __dirname so the git assertion
// below is independent of the process CWD playwright happens to use.
const REPO_ROOT = path.resolve(__dirname, '..', '..');

test('/home.html serves the two doors with no gate', async ({ page }) => {
  const res = await page.goto('/home.html');
  expect(res.status(), '/home.html answers 200 from the real backend').toBe(200);
  await expect(page).toHaveTitle('听写小助手');

  // Door 1: the student tutor. Door 2: the parent workspace (13.3-03 tabs).
  await expect(page.locator('a[href="/tutor/"]')).toHaveText('开始今天的听写');
  await expect(page.locator('a[href="/parent.html"]')).toHaveText('家长：记录与词库');

  // Pure navigation (consensus decision 2): no PIN/session/permission
  // surface of any kind — zero <input> elements, zero <script> tags.
  expect(await page.locator('input').count(), 'no input elements').toBe(0);
  expect(await page.locator('script').count(), 'no script tags').toBe(0);
});

test('legacy / fallback page keeps its degraded banner and tutor link', async ({ page }) => {
  const res = await page.goto('/');
  expect(res.status()).toBe(200);
  await expect(page).toHaveTitle('听写小助手');

  // The RT-10 degraded banner (index-banner.js) installs itself as the
  // first child of <body> and links to /tutor/ — anchors mirror the file's
  // actual copy verbatim.
  const banner = page.locator('#rt-degraded-banner');
  await expect(banner).toBeVisible();
  await expect(banner).toContainText('此页面为降级备用模式');
  const link = banner.locator('a');
  await expect(link).toHaveAttribute('href', '/tutor/');
  await expect(link).toHaveText('/tutor/');
});

test('git working tree: the / fallback sources stay untouched', async () => {
  // Scoped to EXACTLY the two fallback files — nothing else about the tree
  // (which legitimately carries this plan's own new files) participates.
  const out = execFileSync(
    'git',
    ['status', '--porcelain', '--',
      'pkg/web/static/index.html', 'pkg/web/static/js/index-banner.js'],
    { cwd: REPO_ROOT, encoding: 'utf8' }
  );
  expect(out, 'index.html / index-banner.js must have zero working-tree changes').toBe('');
});
