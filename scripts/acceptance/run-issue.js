#!/usr/bin/env node
'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const { resolvePython, runCommand } = require('./evidence');

const root = path.resolve(__dirname, '../..');
const manifest = JSON.parse(fs.readFileSync(path.join(__dirname, 'manifest.json'), 'utf8'));

function printMatrix(id, issue) {
  console.log(`Issue #${id} — ${issue.title}\nADR: ${issue.adr}`);
  for (const ac of issue.acceptance) {
    console.log(`${ac.id} ${ac.text}`);
    for (const evidence of ac.evidence) console.log(`  ${evidence}`);
  }
  console.log('Human verification remaining (outside automated results):');
  for (const item of issue.human_remaining) console.log(`  - ${item}`);
  if (!issue.human_remaining.length) console.log('  None listed for this issue');
}

function git(args) {
  const result = spawnSync('git', args, { cwd: root, encoding: 'utf8' });
  if (result.error || result.status !== 0) throw new Error('Cannot record Git revision and worktree state');
  return result.stdout.trim();
}

function goStep(label, selection, e2e) {
  const escaped = selection.expected.map((name) => name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'));
  return {
    label, kind: 'go', command: 'go', expected: selection.expected,
    args: ['test', '-json', '-count=1', '-timeout=5m', ...(e2e ? ['-tags=e2e'] : []),
      '-run', `^(?:${escaped.join('|')})$`, ...selection.packages],
  };
}

function main() {
  const args = process.argv.slice(2).filter((arg) => arg !== '--');
  const id = args[0];
  if (!manifest[id] || args.slice(1).some((arg) => arg !== '--dry-matrix')) {
    console.error(`Usage: node scripts/acceptance/run-issue.js <${Object.keys(manifest).join('|')}> [--dry-matrix]`);
    return 2;
  }
  const issue = manifest[id];
  printMatrix(id, issue);
  if (args.includes('--dry-matrix')) return 0;

  const stamp = new Date().toISOString().replace(/[:.]/g, '-');
  const logDir = path.resolve(process.env.ACCEPTANCE_LOG_DIR
    || path.join(root, '.scratch', 'acceptance', `issue-${id}`, `${stamp}-${process.pid}`));
  fs.mkdirSync(logDir, { recursive: true });
  const summaryPath = path.join(logDir, 'summary.json');
  const report = {
    version: 1, issue: id, scope: 'automated', startedAt: new Date().toISOString(),
    commit: null, dirty: null, status: 'running', humanRemaining: issue.human_remaining,
    steps: [], notExecuted: [], errors: [],
  };
  function save() { fs.writeFileSync(summaryPath, JSON.stringify(report, null, 2) + '\n', 'utf8'); }
  const steps = [];
  try {
    report.commit = git(['rev-parse', 'HEAD']);
    report.dirty = git(['status', '--porcelain']).length > 0;
    // Real-Python suites can otherwise skip with exit 0. Resolve it before
    // either Go or the isolated browser config starts doing useful work.
    const env = { ...process.env, HTA_PYTHON_INTERPRETER: resolvePython(root) };
    for (const file of (issue.unit && issue.unit.npm) || []) {
      steps.push({ label: `unit-${path.basename(file, '.js')}`, kind: 'node-script', command: process.execPath, args: [path.join(root, 'tests', file)] });
    }
    if (issue.unit && issue.unit.go) steps.push(goStep('go-unit', issue.unit.go, false));
    if (issue.go) steps.push(goStep('go-e2e', issue.go, true));
    if (issue.browser) {
      // Resolve the package's exported metadata, then run its declared JS
      // binary with Node. POSIX .bin shims are not Windows executables.
      const packagePath = require.resolve('@playwright/test/package.json');
      const packageInfo = JSON.parse(fs.readFileSync(packagePath, 'utf8'));
      const cli = path.join(path.dirname(packagePath), packageInfo.bin.playwright);
      steps.push({ label: 'browser-e2e', kind: 'browser', command: process.execPath,
        args: [cli, 'test', '-c', issue.browser.config, issue.browser.file, '--reporter=json'], expected: issue.browser.expected });
    }
    save();
    for (let index = 0; index < steps.length; index++) {
      const step = steps[index];
      console.log(`RUN ${step.label}`);
      const result = runCommand(step, { root, env, logDir });
      report.steps.push(result);
      console.log(`${result.status}: ${JSON.stringify(result.counts)} (${result.kind}; ${result.log})`);
      save();
      if (result.status !== 'passed') {
        report.notExecuted = steps.slice(index + 1).map((pending) => pending.label);
        throw new Error(`${step.label}: ${result.errors.join('; ')}`);
      }
    }
    report.status = 'passed';
  } catch (error) {
    report.status = 'failed';
    report.errors.push(error.message);
    console.error(error.message);
  }
  report.finishedAt = new Date().toISOString();
  save();
  console.log(`Automated result: ${report.status}. Report: ${summaryPath}`);
  console.log(`Human verification remaining: ${report.humanRemaining.length}. Issue state is managed separately.`);
  return report.status === 'passed' ? 0 : 1;
}

try {
  process.exitCode = main();
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
}
