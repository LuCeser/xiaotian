#!/usr/bin/env node
/**
 * 必测清单核验 for the milestone-1 browser line (issue #58, e2e-test-plan
 * §6 / §7 step 16): a green Playwright exit code is NOT enough — the plan
 * forbids the case where the browser, Python or the launcher goes missing
 * and the run quietly reports nothing (「不能因 Python、浏览器或启动器
 * 缺失而全部 skip 后通过」).
 *
 * Reads the Playwright JSON report (PLAYWRIGHT_JSON_OUTPUT_NAME) and fails
 * unless the B-layer core set actually executed:
 *   - every required spec file contributed >= 1 test,
 *   - >= MIN_TOTAL tests ran, zero skipped, zero flaky.
 *
 * The file list mirrors tests/playwright.m1.config.js testMatch; a new spec
 * raises MIN_TOTAL when it lands (keep both in sync until plan step 27's
 * acceptance manifest replaces this floor check).
 */
import fs from 'node:fs';

const REQUIRED_FILES = [
  'smoke.spec.js',
  'subject-playback.spec.js',
  'voice-input.spec.js',
  'finish.spec.js',
];
const MIN_TOTAL = 7; // 1 smoke + 1 subject-playback + 1 voice-input + 4 finish

const reportPath = process.argv[2];
if (!reportPath) {
  console.error('usage: node scripts/check-m1-report.mjs <playwright-json-report>');
  process.exit(2);
}

let report;
try {
  report = JSON.parse(fs.readFileSync(reportPath, 'utf8'));
} catch (err) {
  console.error(`[m1-report-check] FAIL: cannot read report ${reportPath}: ${err.message}`);
  process.exit(1);
}

const problems = [];
const stats = report.stats || {};
const ran = (stats.expected || 0) + (stats.unexpected || 0) + (stats.flaky || 0);
if (ran < MIN_TOTAL) {
  problems.push(`only ${ran} tests executed (floor ${MIN_TOTAL}) — specs dropped from the run?`);
}
if (stats.skipped) problems.push(`${stats.skipped} test(s) skipped — B-layer cases must run, not skip`);
if (stats.flaky) problems.push(`${stats.flaky} flaky test(s)`);

// Count specs per file; suites nest under test.describe, and the file path
// lives on each suite object.
const perFile = new Map();
const walk = (suite) => {
  for (const spec of suite.specs || []) {
    perFile.set(suite.file, (perFile.get(suite.file) || 0) + 1);
  }
  for (const child of suite.suites || []) walk(child);
};
for (const suite of report.suites || []) walk(suite);

for (const name of REQUIRED_FILES) {
  const count = [...perFile.entries()]
    .filter(([file]) => file && file.endsWith(name))
    .reduce((sum, [, n]) => sum + n, 0);
  if (count < 1) problems.push(`required spec file ran no test: ${name}`);
}

if (problems.length) {
  console.error('[m1-report-check] FAIL:\n  - ' + problems.join('\n  - '));
  process.exit(1);
}
console.log(`[m1-report-check] ok: ${ran} tests ran, 0 skipped, all ${REQUIRED_FILES.length} core spec files present`);
