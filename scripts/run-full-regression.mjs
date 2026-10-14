#!/usr/bin/env node
/**
 * Full local regression orchestrator (browser-first E2E gate).
 *
 * Order (fail-fast). Evidence under .scratch/regress-<stamp>/:
 *   1. go test ./... -count=1 -short
 *   2. npm test
 *   3. npm run e2e:parent   (hermetic parent + family-journey)
 *   4. npm run e2e:rt       (mock tutor + family-nav)
 *   5. npm run e2e:family   (force-mode hermetic)
 *   6. npm run e2e          when pwsh/run.ps1 available (student vs dev server)
 *   7. npm run e2e:rt-real  when --require-real OR (.env present and not --skip-real)
 *
 * Flags:
 *   --skip-real       never run L3 synthetic real stack
 *   --require-real    fail if e2e:rt-real prerequisites missing
 *   --skip-student    skip legacy npm run e2e (pwsh-dependent)
 *
 * Windows entry: scripts/run-full-regression.ps1 → this file.
 * npm: `npm run regress`
 */
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.resolve(__dirname, '..');
const args = new Set(process.argv.slice(2));
const skipReal = args.has('--skip-real');
const requireReal = args.has('--require-real');
const skipStudent = args.has('--skip-student');

const stamp = new Date().toISOString().replace(/[:.]/g, '-');
const outDir = path.join(ROOT, '.scratch', `regress-${stamp}`);
fs.mkdirSync(outDir, { recursive: true });

function log(msg) {
  const line = `[regress] ${msg}`;
  console.log(line);
  fs.appendFileSync(path.join(outDir, 'regress.log'), line + '\n');
}

function run(label, command, commandArgs, opts = {}) {
  log(`BEGIN ${label}: ${command} ${commandArgs.join(' ')}`);
  const res = spawnSync(command, commandArgs, {
    cwd: ROOT,
    stdio: 'inherit',
    env: { ...process.env, ...opts.env },
    shell: false,
  });
  const code = res.status == null ? 1 : res.status;
  log(`END ${label}: exit=${code}`);
  fs.writeFileSync(
    path.join(outDir, `${label.replace(/[^\w.-]+/g, '_')}.exit`),
    String(code),
  );
  if (code !== 0) {
    log(`FAIL ${label} — evidence: ${outDir}`);
    process.exit(code);
  }
}

function hasCmd(cmd) {
  const probe = process.platform === 'win32' ? 'where' : 'which';
  const res = spawnSync(probe, [cmd], { stdio: 'ignore' });
  return res.status === 0;
}

function envFilePresent() {
  return fs.existsSync(path.join(ROOT, '.env'));
}

log(`evidence dir: ${outDir}`);
log('browser E2E is the default gate; iPad/device rows are optional L4');

run('go-test-short', 'go', ['test', './...', '-count=1', '-short']);
run('npm-test', 'npm', ['test']);
run('e2e-parent', 'npm', ['run', 'e2e:parent']);
run('e2e-rt', 'npm', ['run', 'e2e:rt']);
run('e2e-family', 'npm', ['run', 'e2e:family']);

const canStudent =
  !skipStudent &&
  (hasCmd('pwsh') || hasCmd('powershell')) &&
  fs.existsSync(path.join(ROOT, 'run.ps1'));
if (canStudent) {
  run('e2e-student', 'npm', ['run', 'e2e']);
} else {
  log('SKIP e2e (student): PowerShell launcher unavailable — use e2e:parent/e2e:rt/e2e:family on this host');
}

const wantReal = requireReal || (!skipReal && envFilePresent());
if (wantReal) {
  if (!envFilePresent()) {
    log('ERROR: --require-real set but .env missing');
    process.exit(1);
  }
  run('e2e-rt-real', 'npm', ['run', 'e2e:rt-real']);
} else {
  log('SKIP e2e:rt-real: no .env (pass --require-real to make this mandatory)');
}

log(`PASS full regression — evidence: ${outDir}`);
process.exit(0);
