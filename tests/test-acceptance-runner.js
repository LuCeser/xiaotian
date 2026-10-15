'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { test } = require('node:test');
const { parseGo, parseBrowser, resolvePython, runCommand } = require('../scripts/acceptance/evidence');

function temporary(t) {
  const prefix = path.join(os.tmpdir(), 'xiaotian-acceptance-');
  const dir = fs.mkdtempSync(prefix);
  t.after(() => {
    assert.ok(path.resolve(dir).startsWith(path.resolve(prefix)));
    fs.rmSync(dir, { recursive: true, force: true });
  });
  return dir;
}

function goEvent(name, action) {
  return JSON.stringify({ Package: 'example/package', Test: name, Action: action }) + '\n';
}

function browserReport(cases, errors = []) {
  return JSON.stringify({ suites: [{ suites: [{ specs: cases.map(([name, status, outcome = 'expected']) => ({
    title: name, tests: [{ expectedStatus: 'passed', status: outcome, results: status ? [{ status }] : [] }],
  })) }] }], errors });
}

test('Go evidence requires each named test and records real terminal events', () => {
  const report = parseGo(goEvent('TestFirst', 'pass') + goEvent('TestSecond', 'pass'), ['TestFirst', 'TestSecond']);
  assert.deepEqual(report.counts, { passed: 2, failed: 0, skipped: 0 });
  assert.deepEqual(report.errors, []);
  const incomplete = parseGo(goEvent('TestUnrelated', 'pass'), ['TestFirst']);
  assert.match(incomplete.errors.join(' '), /Required test did not execute: TestFirst/);
});

test('Go zero matches and skipped real-Python suites fail even with process exit 0', () => {
  assert.match(parseGo('', ['TestRealPython']).errors.join(' '), /No tests executed/);
  const skipped = parseGo(goEvent('TestRealPython', 'skip'), ['TestRealPython']);
  assert.equal(skipped.counts.skipped, 1);
  assert.ok(skipped.errors.length);
});

test('Go failed tests, package failures and malformed reports are not passes', () => {
  assert.equal(parseGo(goEvent('TestBroken', 'fail'), ['TestBroken']).counts.failed, 1);
  assert.match(parseGo(JSON.stringify({ Package: 'broken', Action: 'fail' }), []).errors.join(' '), /Go package failed/);
  assert.throws(() => parseGo('not JSON', ['TestFirst']));
});

test('Playwright evidence reads nested suites and requires expected test titles', () => {
  const report = parseBrowser(browserReport([['first', 'passed'], ['second', 'passed']]), ['first', 'second']);
  assert.deepEqual(report.counts, { passed: 2, failed: 0, skipped: 0 });
  assert.deepEqual(report.errors, []);
  assert.match(parseBrowser(browserReport([['unrelated', 'passed']]), ['first']).errors.join(' '), /Required test did not execute/);
});

test('Playwright zero matches, skips and incomplete runs all fail', () => {
  assert.ok(parseBrowser(browserReport([]), ['first']).errors.length);
  const skipped = parseBrowser(browserReport([['first', 'skipped', 'skipped']]), ['first']);
  assert.equal(skipped.counts.skipped, 1);
  assert.ok(skipped.errors.length);
  assert.equal(parseBrowser(browserReport([['first', null]]), ['first']).counts.failed, 1);
});

test('Playwright flaky/expected failures and global errors cannot hide behind a passing result', () => {
  assert.equal(parseBrowser(browserReport([['first', 'passed', 'flaky']]), ['first']).counts.failed, 1);
  assert.equal(parseBrowser(browserReport([['first', 'failed']]), ['first']).counts.failed, 1);
  assert.match(parseBrowser(browserReport([['first', 'passed']], [{ message: 'server died' }]), ['first']).errors.join(' '), /server died/);
  assert.throws(() => parseBrowser('{}', ['first']), /no suites/);
});

test('Python discovery supports both venv layouts and explicit overrides fail clearly', (t) => {
  const root = temporary(t);
  const venv = path.join(root, 'third_party', 'python', '.venv');
  const posix = path.join(venv, 'bin', 'python');
  const windows = path.join(venv, 'Scripts', 'python.exe');
  fs.mkdirSync(path.dirname(posix), { recursive: true });
  fs.writeFileSync(posix, 'fixture');
  assert.equal(resolvePython(root, {}), posix);
  fs.mkdirSync(path.dirname(windows), { recursive: true });
  fs.writeFileSync(windows, 'fixture');
  assert.equal(resolvePython(root, { HTA_PYTHON_INTERPRETER: windows }), windows);
  assert.equal(resolvePython(root, { HTA_E2E_VENV: venv }), process.platform === 'win32' ? windows : posix);
  assert.throws(() => resolvePython(root, { HTA_PYTHON_INTERPRETER: path.join(root, 'missing') }), /interpreter missing/);
});

test('child process launch failures and nonzero exits retain evidence but fail the step', (t) => {
  const root = temporary(t);
  const options = { root, env: process.env, logDir: root };
  const missing = runCommand({ label: 'missing', kind: 'node-script', command: path.join(root, 'missing-executable'), args: [] }, options);
  assert.equal(missing.status, 'failed');
  assert.match(missing.errors.join(' '), /Process could not complete/);
  const failed = runCommand({ label: 'nonzero', kind: 'go', command: process.execPath,
    args: ['-e', `process.stdout.write(${JSON.stringify(goEvent('TestFirst', 'pass'))}); process.exit(7)`], expected: ['TestFirst'] }, options);
  assert.equal(failed.counts.passed, 1);
  assert.equal(failed.status, 'failed');
  assert.match(failed.errors.join(' '), /exited with 7/);
  assert.ok(fs.existsSync(failed.log));
});

test('real child JSON output succeeds only with the required passing tests', (t) => {
  const root = temporary(t);
  const report = browserReport([['first', 'passed']]);
  const result = runCommand({ label: 'normal', kind: 'browser', command: process.execPath,
    args: ['-e', `process.stdout.write(${JSON.stringify(report)})`], expected: ['first'] }, { root, env: process.env, logDir: root });
  assert.equal(result.status, 'passed');
  assert.deepEqual(result.counts, { passed: 1, failed: 0, skipped: 0 });
});
