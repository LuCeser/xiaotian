#!/usr/bin/env node
/**
 * Start a long-lived process without retaining the caller's stdio handles.
 *
 * OpenCode's shell tool on Windows waits for pipe EOF. A daemon started
 * directly from PowerShell can inherit those pipes and keep the tool call in
 * `running` after the launcher itself has finished. Node's detached process
 * group plus explicit file descriptors avoids that inheritance.
 * See https://github.com/anomalyco/opencode/issues/32504.
 *
 * Usage:
 *   node scripts/start-detached.mjs <cwd> <stdout-log> <stderr-log> -- <command> [args...]
 *
 * Prints one JSON object containing the child PID after the OS reports that
 * the process was spawned. Readiness remains the caller's responsibility.
 */
import { closeSync, openSync } from 'node:fs';
import { spawn } from 'node:child_process';

const separator = process.argv.indexOf('--', 2);
if (separator !== 5 || process.argv.length < 7) {
  console.error(
    'usage: start-detached.mjs <cwd> <stdout-log> <stderr-log> -- <command> [args...]',
  );
  process.exit(2);
}

const [, , cwd, stdoutPath, stderrPath] = process.argv;
const command = process.argv[separator + 1];
const args = process.argv.slice(separator + 2);

const stdoutFd = openSync(stdoutPath, 'a');
const stderrFd = openSync(stderrPath, 'a');

let child;
try {
  child = spawn(command, args, {
    cwd,
    detached: true,
    env: process.env,
    stdio: ['ignore', stdoutFd, stderrFd],
    windowsHide: true,
  });

  await new Promise((resolve, reject) => {
    child.once('spawn', resolve);
    child.once('error', reject);
  });

  child.unref();
  process.stdout.write(`${JSON.stringify({ pid: child.pid })}\n`);
} catch (error) {
  console.error(error instanceof Error ? error.message : String(error));
  process.exitCode = 1;
} finally {
  closeSync(stdoutFd);
  closeSync(stderrFd);
}
