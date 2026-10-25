const assert = require('node:assert');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const root = fs.mkdtempSync(path.join(os.tmpdir(), 'xiaotian-detach-test-'));
const stdoutPath = path.join(root, 'child.out.log');
const stderrPath = path.join(root, 'child.err.log');
const launcher = path.resolve(__dirname, '..', 'scripts', 'start-detached.mjs');

let childPid;
try {
  const started = Date.now();
  const result = spawnSync(
    process.execPath,
    [
      launcher,
      root,
      stdoutPath,
      stderrPath,
      '--',
      process.execPath,
      '-e',
      'setTimeout(() => {}, 30000)',
    ],
    { encoding: 'utf8', timeout: 5_000 },
  );
  const elapsed = Date.now() - started;

  assert.strictEqual(result.error, undefined, result.error && result.error.message);
  assert.strictEqual(result.status, 0, result.stderr);
  assert.ok(elapsed < 3_000, `launcher took ${elapsed}ms instead of returning promptly`);

  const launch = JSON.parse(result.stdout.trim());
  childPid = launch.pid;
  assert.ok(Number.isInteger(childPid) && childPid > 0, 'launcher must return a PID');
  process.kill(childPid, 0);

  console.log(`PASS detached launcher returned in ${elapsed}ms with live PID ${childPid}`);
} finally {
  if (childPid) {
    try {
      process.kill(childPid);
    } catch {
      // It may have already exited after an assertion failure.
    }
  }
  // Give Windows a moment to release the log handles before cleanup.
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 200);
  fs.rmSync(root, { recursive: true, force: true });
}
