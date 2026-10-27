// m1-config-reporter.test.mjs — pins the PR #91 CI contract wiring: when
// PLAYWRIGHT_JSON_OUTPUT_NAME is exported, tests/playwright.m1.config.js must
// attach the json reporter with an outputFile resolved against the REPO ROOT.
// Playwright ≥1.4x joins that env var (a bare NAME) onto the reporter's
// default outputDir — the config file's own dir (tests/) when the reporter
// comes from the CLI — so CI's `--reporter=list,json` wrote
// tests/reports/m1-results.json while scripts/check-m1-report.mjs read
// reports/ at the root and failed with ENOENT. The env var must be set
// before the config module loads, hence the child process (same spawnSync
// precedent as check-m1-report.test.mjs's CLI-wiring test).
// Run: node --test tests/e2e-m1-browser/helpers/m1-config-reporter.test.mjs
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

const HERE = path.dirname(fileURLToPath(import.meta.url)); // .../e2e-m1-browser/helpers
const REPO_ROOT = path.resolve(HERE, '..', '..', '..');
const CONFIG = path.join(REPO_ROOT, 'tests', 'playwright.m1.config.js');

// The value ci.yml exports and the path its check step passes to
// check-m1-report.mjs — both repo-root relative, so the join is the contract.
const CI_NAME = 'reports/m1-results.json';

const reporterWith = (envValue) => {
  const env = { ...process.env };
  if (envValue === undefined) delete env.PLAYWRIGHT_JSON_OUTPUT_NAME;
  else env.PLAYWRIGHT_JSON_OUTPUT_NAME = envValue;
  const run = spawnSync(
    process.execPath,
    ['-e', 'process.stdout.write(JSON.stringify(require(process.argv[1]).reporter))', CONFIG],
    { encoding: 'utf8', env },
  );
  assert.equal(run.status, 0, run.stderr);
  return JSON.parse(run.stdout);
};

test('env set: json reporter attached with outputFile anchored at the repo root', () => {
  const reporter = reporterWith(CI_NAME);
  assert.deepEqual(reporter, [
    ['list'],
    ['json', { outputFile: path.join(REPO_ROOT, 'reports', 'm1-results.json') }],
  ]);
  // The exact ci.yml check step invocation: `node scripts/check-m1-report.mjs
  // reports/m1-results.json` from the repo root must read this very file.
  // CI_NAME is slash-shaped (checker argv), but path.relative emits the
  // PLATFORM separator (`reports\m1-results.json` on Windows), so a strict
  // string equal fails there. Compare segment arrays: it still only passes
  // when outputFile is exactly <repoRoot>/reports/m1-results.json — an
  // outside-the-root result surfaces as a `..`/absolute segment and mismatches.
  const [name, options] = reporter[1];
  assert.equal(name, 'json');
  assert.deepEqual(
    path.relative(REPO_ROOT, options.outputFile).split(path.sep),
    CI_NAME.split('/'),
  );
});

test('segment comparison holds under path.win32 semantics', () => {
  // The chain above replayed with explicit Windows path semantics, so a
  // POSIX-only run still proves Windows passes: resolve() accepts the
  // forward-slash CI_NAME but re-separates to backslashes, which is exactly
  // why the old plain string equal broke on Windows while segments match.
  const winRoot = 'C:\\xiaotian';
  const winOutputFile = path.win32.resolve(winRoot, CI_NAME);
  assert.equal(winOutputFile, 'C:\\xiaotian\\reports\\m1-results.json');
  assert.notEqual(path.win32.relative(winRoot, winOutputFile), CI_NAME);
  assert.deepEqual(
    path.win32.relative(winRoot, winOutputFile).split(path.win32.sep),
    CI_NAME.split('/'),
  );
});

test('env unset: plain local run keeps the bare list reporter', () => {
  assert.deepEqual(reporterWith(undefined), [['list']]);
});

test('absolute env value passes through un-reanchored (path.resolve semantics)', () => {
  // Must be a genuinely absolute path (drive-qualified on Windows):
  // path.resolve anchors a bare `\tmp\…` onto the current drive, so such a
  // value would NOT pass through and the deepEqual would fail there.
  // os.tmpdir() is absolute and normalized on every platform, so resolve
  // returns it as-is.
  const abs = path.join(os.tmpdir(), 'elsewhere', 'm1.json');
  assert.deepEqual(reporterWith(abs), [['list'], ['json', { outputFile: abs }]]);
});
