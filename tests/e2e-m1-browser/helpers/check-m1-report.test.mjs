// check-m1-report.test.mjs — PR #73 review @0d2d125, both P2s pinned as
// in-memory Playwright-JSON fixtures (no browser, no services):
//   P2-1 identity: deleting a required finish case while adding a filler
//       test in smoke satisfied every OLD condition (4 files present,
//       7 rows, 0 skip/flaky) — only per-case file+title identity closes
//       it.
//   P2-2 real pass: stats.expected counts a test.fail() expected failure
//       exactly like a normal pass, so stats arithmetic accepted it — the
//       check must demand result status 'passed' on every test row.
// Run: node --test tests/e2e-m1-browser/helpers/check-m1-report.test.mjs
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import { checkM1Report, REQUIRED_CASES } from '../../../scripts/check-m1-report.mjs';

const HERE = path.dirname(fileURLToPath(import.meta.url)); // .../e2e-m1-browser/helpers
const SPEC_DIR = path.resolve(HERE, '..');
const SCRIPT = path.resolve(HERE, '..', '..', '..', 'scripts', 'check-m1-report.mjs');

// One file-suite holding one test row (passing by default).
const caseSuite = (file, title, { expectedStatus = 'passed', status = 'passed', results } = {}) => ({
  file,
  specs: [{ file, title, tests: [{ expectedStatus, results: results ?? [{ status }] }] }],
});

// The seven required titles as an INDEPENDENT copy — deriving them from
// REQUIRED_CASES would make the canary vacuous.
const TITLES = [
  ['smoke.spec.js', 'real pages and REST use one fresh isolated service without vendor traffic'],
  ['subject-playback.spec.js', 'J05 mid-ask button pick: real epoch switch, no turn_start, natural new-opening ack'],
  ['voice-input.spec.js', 'synthetic mic rides the production capture path: format, silence, isolation, release'],
  ['finish.spec.js', 'J15 active end stops the classroom: ask-park, mid-greeting, listening'],
  ['finish.spec.js', 'J13+J14 last word → one check entry, masked answers, real check submit, natural end'],
  ['finish.spec.js', 'J16 finish chinese, reload → the english classroom opens on its own words, both submit independently'],
  ['finish.spec.js', 'J16 empty vocabulary ends through the real no-due result'],
];

const greenReport = () => ({
  suites: TITLES.map(([f, t]) => caseSuite(`tests/e2e-m1-browser/${f}`, t)),
  stats: { expected: 7, skipped: 0, flaky: 0, unexpected: 0 },
});

const withCase = (report, file, title, mods) => ({
  ...report,
  suites: report.suites.map((s) =>
    s.file.endsWith(file) && s.specs[0].title === title ? caseSuite(s.file, title, mods) : s),
});

test('green report: the seven required identities, each a real pass', () => {
  assert.deepEqual(checkM1Report(greenReport()), []);
});

test('P2-1: required finish case deleted + smoke filler added must fail by identity', () => {
  const report = greenReport();
  const suites = report.suites.filter((s) => s.specs[0].title !== TITLES[6][1]);
  suites.push(caseSuite('tests/e2e-m1-browser/smoke.spec.js', 'filler test that is not in the required list'));
  const problems = checkM1Report({ ...report, suites }); // 4 files, 7 rows, 0 skip — old floor stayed green
  assert.ok(problems.some((p) => p.includes(TITLES[6][1])), problems.join('\n'));
});

test('P2-2: a test.fail() expected failure is rejected though stats.expected counts it fine', () => {
  const report = withCase(greenReport(), 'finish.spec.js', TITLES[3][1], { expectedStatus: 'failed', status: 'failed' });
  const problems = checkM1Report(report); // stats.expected stays 7 — the old blind spot
  assert.ok(problems.some((p) => p.includes("expectedStatus 'failed'")), problems.join('\n'));
});

test('a skipped required case did not run', () => {
  const report = withCase(greenReport(), 'voice-input.spec.js', TITLES[2][1], { status: 'skipped' });
  const problems = checkM1Report({ ...report, stats: { expected: 6, skipped: 1, flaky: 0, unexpected: 0 } });
  assert.ok(problems.some((p) => p.includes("'skipped'")), problems.join('\n'));
});

test('flaky retry-pass is rejected via stats.flaky', () => {
  const report = withCase(greenReport(), 'smoke.spec.js', TITLES[0][1], { results: [{ status: 'failed' }, { status: 'passed' }] });
  const problems = checkM1Report({ ...report, stats: { expected: 6, skipped: 0, flaky: 1, unexpected: 0 } });
  assert.ok(problems.some((p) => p.includes('flaky')), problems.join('\n'));
});

test('rename canary: every required title still exists verbatim in its spec source', () => {
  for (const [file, title] of REQUIRED_CASES) {
    const src = fs.readFileSync(path.join(SPEC_DIR, file), 'utf8');
    assert.ok(src.includes(`'${title}'`), `${file} no longer declares required test '${title}' — update REQUIRED_CASES`);
  }
  // The list itself cannot quietly shrink or leak into other files.
  assert.equal(REQUIRED_CASES.length, 7);
  assert.deepEqual(
    [...new Set(REQUIRED_CASES.map(([f]) => f))].sort(),
    ['finish.spec.js', 'smoke.spec.js', 'subject-playback.spec.js', 'voice-input.spec.js'],
  );
});

test('CLI wiring: usage exits 2, unreadable/broken report exits 1, green exits 0', () => {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'm1-report-check-'));
  try {
    const good = path.join(tmp, 'green.json');
    fs.writeFileSync(good, JSON.stringify(greenReport()));
    const broken = path.join(tmp, 'broken.json');
    fs.writeFileSync(broken, '{not json');
    const run = (...args) => spawnSync(process.execPath, [SCRIPT, ...args], { encoding: 'utf8' });
    assert.equal(run().status, 2);
    assert.equal(run(path.join(tmp, 'absent.json')).status, 1);
    assert.equal(run(broken).status, 1);
    const okRun = run(good);
    assert.equal(okRun.status, 0, okRun.stderr);
    assert.ok(okRun.stdout.includes('required cases ran by identity'));
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
});
