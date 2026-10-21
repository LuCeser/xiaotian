// service-launcher.test.js — step-4 verification (issue #58, e2e-test-plan
// §7 step 4): the isolated launcher boots the REAL service binary built
// from this checkout on its own ports + temp root, the public and health
// listeners answer, the shared SQLite DB lands inside the temp root, and
// stop() tears the process and the root down (relaunch then works).
// Start/stop only — no classroom flow, no vendor traffic.
// Run: node --test tests/e2e-m1-browser/helpers/service-launcher.test.js
// (Set HTA_E2E_SERVICE_BIN to reuse a prebuilt binary; the test builds
// cmd/service once otherwise.)
'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const http = require('node:http');
const os = require('node:os');
const path = require('node:path');
const { before, after, test } = require('node:test');
const { buildServiceBinary, launchService } = require('./service-launcher.js');

const repoRoot = path.resolve(__dirname, '..', '..', '..');
// Step 4 reuses the parent suite's committed template: every port/path it
// declares is overridden by the launcher's env, so the choice only fixes
// the remaining boot shape (staticDir, script paths). Step 5 lands the m1
// line's own minimal template.
const template = path.join(repoRoot, 'tests', 'e2e', 'config.parent-e2e.yaml');

let binary;
let binDir;
before(() => {
  if (process.env.HTA_E2E_SERVICE_BIN) {
    binary = process.env.HTA_E2E_SERVICE_BIN;
    return;
  }
  // binDir must be assigned BEFORE the build: a failed build still leaves
  // the mkdtemp dir, and after() runs even when before() throws.
  binDir = fs.mkdtempSync(path.join(os.tmpdir(), 'xiaotian-m1-bin-'));
  binary = buildServiceBinary(repoRoot, path.join(binDir, 'service'));
});

after(() => {
  if (binDir) fs.rmSync(binDir, { recursive: true, force: true });
});

function getJson(url) {
  return new Promise((resolve, reject) => {
    http.get(url, (res) => {
      const chunks = [];
      res.on('data', (c) => chunks.push(c));
      res.on('end', () => resolve({ status: res.statusCode, body: Buffer.concat(chunks).toString('utf8') }));
    }).on('error', reject);
  });
}

test('launch boots the real service isolated; stop tears process and root down', async () => {
  const handle = await launchService({ repoRoot, template, binary, readyTimeoutMs: 60000 });
  try {
    const [page, health, lessons] = await Promise.all([
      getJson(handle.url),
      getJson(handle.healthUrl),
      getJson('http://' + handle.addr + '/api/v1/lessons'),
    ]);
    assert.equal(page.status, 200);
    assert.match(page.body, /<!DOCTYPE html>|<html/i);
    assert.equal(health.status, 200);
    // Pure-Go read route (D-12, zero Python spawns) answers on the fresh DB.
    assert.equal(lessons.status, 200);
    assert.ok(Array.isArray(JSON.parse(lessons.body).lessons) || Array.isArray(JSON.parse(lessons.body)));
    // The shared Go+Python SQLite DB landed inside the launcher's temp root.
    assert.ok(fs.existsSync(path.join(handle.root, 'vocab.db')));
    // Dual distinct loopback ports.
    assert.notEqual(handle.addr, handle.healthAddr);
    assert.match(handle.addr, /^127\.0\.0\.1:\d+$/);
    assert.match(handle.healthAddr, /^127\.0\.0\.1:\d+$/);
  } finally {
    await handle.stop();
  }
  assert.notEqual(handle.proc.exitCode, null); // process really exited
  assert.equal(fs.existsSync(handle.root), false); // root really removed
});

test('relaunch after stop gets a fresh root and serves again', async () => {
  const first = await launchService({ repoRoot, template, binary, readyTimeoutMs: 60000 });
  const firstRoot = first.root;
  await first.stop();
  const second = await launchService({ repoRoot, template, binary, readyTimeoutMs: 60000 });
  try {
    assert.notEqual(second.root, firstRoot);
    const page = await getJson(second.url);
    assert.equal(page.status, 200);
    assert.equal(fs.existsSync(firstRoot), false);
  } finally {
    await second.stop();
  }
});
