'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

// Required names come from the manifest, independently of the test filter.
// A successful process alone cannot prove that those tests actually ran.
function assess(tests, expected, errors = []) {
  const counts = { passed: 0, failed: 0, skipped: 0 };
  for (const test of tests) {
    if (test.status === 'passed') counts.passed++;
    else if (test.status === 'skipped') counts.skipped++;
    else counts.failed++;
  }
  errors = [...errors];
  if (!tests.length) errors.push('No tests executed');
  for (const name of expected) {
    if (!tests.some((test) => test.name === name)) {
      errors.push(`Required test did not execute: ${name}`);
    }
  }
  if (counts.failed) errors.push(`${counts.failed} tests failed or did not finish`);
  if (counts.skipped) errors.push(`${counts.skipped} required-suite tests skipped`);
  return { counts, tests, errors };
}

function parseGo(stdout, expected) {
  const tests = [];
  const errors = [];
  for (const line of stdout.split(/\r?\n/).filter((line) => line.trim())) {
    const event = JSON.parse(line);
    if (['pass', 'fail', 'skip'].includes(event.Action) && event.Test) {
      tests.push({
        name: event.Test,
        status: { pass: 'passed', fail: 'failed', skip: 'skipped' }[event.Action],
      });
    } else if (event.Action === 'fail' && !event.Test) {
      errors.push(`Go package failed: ${event.Package || 'unknown'}`);
    }
  }
  return assess(tests, expected, errors);
}

function parseBrowser(stdout, expected) {
  const report = JSON.parse(stdout);
  if (!Array.isArray(report.suites)) throw new Error('Playwright report has no suites');
  const tests = [];
  function visit(suite) {
    for (const spec of suite.specs || []) {
      for (const test of spec.tests || []) {
        const last = (test.results || []).at(-1);
        let status = last ? last.status : 'not-run';
        // Expected failures and flaky retries are not acceptance passes.
        if (status === 'passed' && (test.status !== 'expected' || test.expectedStatus !== 'passed')) {
          status = 'failed';
        }
        tests.push({ name: spec.title, status });
      }
    }
    for (const child of suite.suites || []) visit(child);
  }
  for (const suite of report.suites) visit(suite);
  const errors = (report.errors || []).map((error) => error.message || 'Playwright run error');
  return assess(tests, expected, errors);
}

function resolvePython(root, env = process.env) {
  function findIn(venv) {
    const relative = process.platform === 'win32'
      ? ['Scripts/python.exe', 'bin/python'] : ['bin/python', 'Scripts/python.exe'];
    return relative.map((file) => path.join(venv, file));
  }
  const candidates = env.HTA_PYTHON_INTERPRETER ? [env.HTA_PYTHON_INTERPRETER]
    : env.HTA_E2E_VENV ? findIn(env.HTA_E2E_VENV)
      : ['venv', '.venv', 'third_party/python/.venv'].flatMap((dir) => findIn(path.join(root, dir)));
  const found = candidates.find((file) => fs.existsSync(file) && fs.statSync(file).isFile());
  if (!found) {
    throw new Error('Real Python interpreter missing; set HTA_PYTHON_INTERPRETER or HTA_E2E_VENV to an installed venv');
  }
  return path.resolve(found);
}

function runCommand(step, options) {
  const result = spawnSync(step.command, step.args, {
    cwd: options.root,
    env: options.env,
    encoding: 'utf8',
    maxBuffer: 32 * 1024 * 1024,
    timeout: 600_000,
  });
  const stdout = result.stdout || '';
  const stderr = result.stderr || '';
  const log = path.join(options.logDir, step.label + '.log');
  fs.writeFileSync(log, `STDOUT\n${stdout}\nSTDERR\n${stderr}`, 'utf8');
  let evidence;
  try {
    evidence = step.kind === 'go' ? parseGo(stdout, step.expected)
      : step.kind === 'browser' ? parseBrowser(stdout, step.expected)
        : { counts: { passed: result.status === 0 ? 1 : 0, failed: result.status === 0 ? 0 : 1, skipped: 0 }, tests: [], errors: [] };
  } catch (error) {
    evidence = { counts: { passed: 0, failed: 0, skipped: 0 }, tests: [], errors: [`Invalid test report: ${error.message}`] };
  }
  if (result.error) evidence.errors.push(`Process could not complete: ${result.error.message}`);
  if (result.status !== 0) evidence.errors.push(`Process exited with ${result.status}; signal=${result.signal || 'none'}`);
  return {
    label: step.label,
    kind: step.kind,
    command: [step.command, ...step.args],
    exitCode: result.status,
    log,
    ...evidence,
    status: evidence.errors.length ? 'failed' : 'passed',
  };
}

module.exports = { parseGo, parseBrowser, resolvePython, runCommand };
