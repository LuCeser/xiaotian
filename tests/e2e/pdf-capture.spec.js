// pdf-capture.spec.js — the Issue #7 fixture-regen tool (NOT a test).
// Skipped unless HTA_CAPTURE_PDF_DIR is set; when set, it uploads each
// fixture PDF through the real page so the browser's own pdf.js renders
// the pages, and the mimo stub (started with --save-unregistered by the
// config) saves every page image it does not recognize into
// HTA_CAPTURE_PDF_DIR. The saved unreg-NNN.jpg files are then renamed to
// their fixture names and given sibling .reply.json files — the only
// source of the EXACT page-image bytes the stub can byte-match later
// (they are produced by this Chromium build's canvas; ADR-0004).
//
// Run:
//   mkdir -p tests/e2e/.pdf-capture
//   HTA_CAPTURE_PDF_DIR=tests/e2e/.pdf-capture npm run e2e:parent -- tests/e2e/pdf-capture.spec.js
// (Windows PowerShell: $env:HTA_CAPTURE_PDF_DIR='tests/e2e/.pdf-capture'; npm run e2e:parent -- tests/e2e/pdf-capture.spec.js)
'use strict';

const { test } = require('@playwright/test');

test.skip(!process.env.HTA_CAPTURE_PDF_DIR, 'capture tool — set HTA_CAPTURE_PDF_DIR to run');

for (const pdf of ['pdf-textbook.pdf', 'pdf-retry.pdf']) {
  test(`capture page images of ${pdf}`, async ({ page }) => {
    await page.goto('/parent.html');
    await page.getByRole('tab', { name: '词库' }).click();
    await page.locator('#addSection').waitFor();
    await page.getByRole('tab', { name: '照片导入' }).click();
    await page.locator('#add-panel-photo').waitFor();
    // Issue #10: the subject radio is required before 开始识别 runs.
    await page.locator('input[name="parent-category-photo"][value="english"]').check();
    await page.locator('#parent-photo-file').setInputFiles(
      'tests/e2e/fixtures/' + pdf);
    await page.getByRole('button', { name: '开始识别' }).click();
    // Every page fails against the stub (nothing registered yet) — the
    // run settles with all chips failed, which is exactly the state that
    // made the stub save each page's bytes.
    await page.locator('.pdf-page-chip[data-status="failed"]').first()
      .waitFor({ timeout: 60_000 });
    await page.waitForFunction(
      () => document.querySelectorAll('.pdf-page-chip[data-status="failed"]').length ===
        document.querySelectorAll('.pdf-page-chip').length,
      null, { timeout: 120_000 });
  });
}
