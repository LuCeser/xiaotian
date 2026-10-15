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
    plannedSteps: [], steps: [], notExecuted: [], errors: [],
    counts: { go: { passed: 0, failed: 0, skipped: 0 }, browser: { passed: 0, failed: 0, skipped: 0 }, 'node-script': { passed: 0, failed: 0, skipped: 0 } },
  };
  function save() { fs.writeFileSync(summaryPath, JSON.stringify(report, null, 2) + '\n', 'utf8'); }
  const steps = [];
  try {
    for (const file of (issue.unit && issue.unit.npm) || []) {
      steps.push({ label: `unit-${path.basename(file, '.js')}`, kind: 'node-script', command: process.execPath, args: [path.join(root, 'tests', file)] });
    }
    if (issue.unit && issue.unit.go) steps.push(goStep('go-unit', issue.unit.go, false));
    if (issue.go) steps.push(goStep('go-e2e', issue.go, true));
    if (issue.browser) {
      // Use the installed package's JS CLI. POSIX .bin shims are not
      // Windows executables; the command is known even before npm ci.
      const cli = path.join(root, 'node_modules', '@playwright', 'test', 'cli.js');
      steps.push({ label: 'browser-e2e', kind: 'browser', command: process.execPath,
        args: [cli, 'test', '-c', issue.browser.config, issue.browser.file, '--reporter=json'], expected: issue.browser.expected });
    }
    report.plannedSteps = steps.map((step) => ({ label: step.label, kind: step.kind,
      command: [step.command, ...step.args], expected: step.expected || [] }));
    report.notExecuted = steps.map((step) => step.label);
    save();
    report.commit = git(['rev-parse', 'HEAD']);
    report.dirty = git(['status', '--porcelain']).length > 0;
    // Real-Python suites can otherwise skip with exit 0. Preflight runs
    // after planning, so every unexecuted command remains in the report.
    const env = { ...process.env, HTA_PYTHON_INTERPRETER: resolvePython(root) };
    const browser = steps.find((step) => step.kind === 'browser');
    if (browser && !fs.existsSync(browser.args[0])) {
      throw new Error('Playwright CLI missing; install repository dependencies with npm ci');
    }
    save();
    for (let index = 0; index < steps.length; index++) {
      const step = steps[index];
      console.log(`RUN ${step.label}`);
      const result = runCommand(step, { root, env, logDir });
      report.steps.push(result);
      report.notExecuted = steps.slice(index + 1).map((pending) => pending.label);
      for (const status of ['passed', 'failed', 'skipped']) report.counts[step.kind][status] += result.counts[status];
      console.log(`${result.status}: ${JSON.stringify(result.counts)} (${result.kind}; ${result.log})`);
      save();
      if (result.status !== 'passed') {
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
