#!/usr/bin/env node
/**
 * Issue acceptance runner — maps GitHub issue AC checkboxes to the
 * automated evidence that already lives in the repo (Playwright parent
 * suite + pybridge -tags=e2e + selected npm unit tests).
 *
 * Usage:
 *   node scripts/acceptance/run-issue.js 6
 *   node scripts/acceptance/run-issue.js 7 --dry-matrix
 *   npm run e2e:accept -- 6
 *
 * Exit 0 only when every automated command for that issue passes.
 * Human-remaining items are printed but do not fail the run.
 */
// @ts-check
'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const root = path.resolve(__dirname, '../..');
const manifest = JSON.parse(
  fs.readFileSync(path.join(__dirname, 'manifest.json'), 'utf8'),
);

const args = process.argv.slice(2).filter((a) => a !== '--');
const dry = args.includes('--dry-matrix');
const issueArg = args.find((a) => !a.startsWith('--'));
if (!issueArg || !manifest[issueArg]) {
  const known = Object.keys(manifest).sort().join(', ');
  console.error(`Usage: node scripts/acceptance/run-issue.js <${known}> [--dry-matrix]`);
  process.exit(2);
}

const issue = manifest[issueArg];
const logDir = process.env.ACCEPTANCE_LOG_DIR
  || path.join('/opt/cursor/artifacts', `issue-${issueArg}-acceptance`);
fs.mkdirSync(logDir, { recursive: true });

function banner(title) {
  console.log('\n' + '='.repeat(72));
  console.log(title);
  console.log('='.repeat(72));
}

function printMatrix() {
  banner(`Issue #${issueArg} — ${issue.title}`);
  console.log(`ADR: ${issue.adr}`);
  console.log(`Close when green: ${issue.close ? 'yes' : 'no (human remaining)'}`);
  console.log('');
  for (const ac of issue.acceptance) {
    console.log(`${ac.id}  ${ac.text}`);
    for (const ev of ac.evidence) {
      console.log(`       ← ${ev}`);
    }
  }
  if (issue.human_remaining && issue.human_remaining.length) {
    console.log('\nHuman remaining (does not fail this runner):');
    for (const h of issue.human_remaining) {
      console.log(`  - ${h}`);
    }
  }
}

/**
 * @param {string} label
 * @param {string} cmd
 * @param {string[]} cmdArgs
 * @param {NodeJS.ProcessEnv} [env]
 */
function run(label, cmd, cmdArgs, env) {
  banner(`RUN ${label}`);
  console.log(`$ ${cmd} ${cmdArgs.join(' ')}`);
  const logPath = path.join(logDir, `${label.replace(/[^\w.-]+/g, '_')}.log`);
  const res = spawnSync(cmd, cmdArgs, {
    cwd: root,
    env: { ...process.env, ...(env || {}) },
    encoding: 'utf8',
    maxBuffer: 32 * 1024 * 1024,
  });
  const out = `${res.stdout || ''}${res.stderr || ''}`;
  fs.writeFileSync(logPath, out, 'utf8');
  process.stdout.write(out);
  if (res.status !== 0) {
    console.error(`\nFAILED ${label} (exit ${res.status}) — log: ${logPath}`);
    process.exit(res.status || 1);
  }
  console.log(`\nOK ${label} — log: ${logPath}`);
}

printMatrix();
if (dry) {
  process.exit(0);
}

if (issue.unit && issue.unit.npm) {
  for (const file of issue.unit.npm) {
    run(`unit-${file}`, process.execPath, [path.join('tests', file)]);
  }
}

if (issue.go) {
  const runFlag = issue.go.run ? ['-run', issue.go.run] : [];
  run(
    'go-e2e',
    'go',
    ['test', '-tags=e2e', '-count=1', '-v', ...runFlag, ...issue.go.packages],
    {
      UV_INDEX_URL: process.env.UV_INDEX_URL || 'https://pypi.tuna.tsinghua.edu.cn/simple',
      HTA_PYTHON_INTERPRETER:
        process.env.HTA_PYTHON_INTERPRETER
        || path.join(root, 'third_party/python/.venv/bin/python'),
    },
  );
}

if (issue.browser) {
  run(
    'browser-e2e',
    path.join(root, 'node_modules/.bin/playwright'),
    [
      'test',
      '-c', issue.browser.config,
      issue.browser.grep,
    ],
  );
}

banner(`Issue #${issueArg} automated acceptance GREEN`);
console.log(`Logs: ${logDir}`);
if (issue.human_remaining && issue.human_remaining.length) {
  console.log('Leave issue OPEN — apply closeout body (AC automation checked) and ready-for-human.');
  console.log(`  scripts/acceptance/closeouts/${issueArg.padStart(2, '0')}-body.md`);
} else if (issue.close) {
  console.log('Ready to close — apply closeout body + comment when you have issue write access:');
  console.log(`  bash scripts/acceptance/apply-closeout.sh ${issueArg}`);
}
process.exit(0);
