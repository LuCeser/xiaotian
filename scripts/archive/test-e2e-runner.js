#!/usr/bin/env node

const { spawn } = require('child_process');
const fs = require('fs');

console.log('[BUILD] Building Go test client...');
const build = spawn('go', ['build', '-o', 'test-e2e-client.exe', 'test-e2e-multi-word.go'], {
  cwd: process.cwd(),
  stdio: 'inherit'
});

build.on('exit', (code) => {
  if (code !== 0) {
    console.error('[BUILD] Failed');
    process.exit(1);
  }

  console.log('[BUILD] Success, starting server...');
  const server = spawn('./service.exe', [], {
    cwd: process.cwd(),
    stdio: ['ignore', 'pipe', 'pipe'],
    detached: true
  });

  let serverOutput = '';
  server.stderr.on('data', (data) => {
    const text = data.toString();
    serverOutput += text;
    if (text.includes('listening')) {
      console.log('[SERVER] Ready, starting test...');

      // Give server a moment to fully initialize
      setTimeout(() => {
        console.log('[TEST] Running e2e test...');
        const test = spawn('./test-e2e-client.exe', [], {
          cwd: process.cwd(),
          stdio: 'inherit'
        });

        test.on('exit', (testCode) => {
          console.log(`[TEST] Exit code: ${testCode}`);

          // Kill server
          try {
            process.kill(-server.pid);
          } catch(e) {}

          process.exit(testCode);
        });
      }, 1000);
    }
  });

  server.on('error', (err) => {
    console.error('[SERVER] Failed to start:', err);
    process.exit(1);
  });
});
