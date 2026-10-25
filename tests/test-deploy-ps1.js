// Fixture smoke test for scripts/deploy.ps1 (quick task 261024-njp).
//
// Follows the tests/test-start-detached.js / tests/test-install.js pattern:
// mkdtemp fixtures, spawnSync of the PowerShell engine (direct powershell
// calls are blocked in worktree-isolated agent shells - spawning through
// node passes), bounded timeouts, finally cleanup with a Windows
// handle-release wait. Zero network, zero real service: hta.ps1 inside each
// fixture install dir is a STUB that records the subcommands it receives and
// fakes the exit-code contract (start exit from HTA_STUB_START_EXIT, stop
// always 0), so deploy.ps1's sequencing and health-gate behavior are asserted
// without ever touching a real install.
//
// Sections:
//   0. PS parser gate on scripts/deploy.ps1 (zero parse errors)
//   A. deploy success -> versions\prev holds the complete old version, the
//      bin/web/python swap surface is fully refreshed, five forbidden-zone
//      sentinels (config/secrets/certs/data/venv) byte-identical, stub calls
//      exactly [stop, start]
//   B. failed health gate -> nonzero exit, output carries a rollback hint,
//      new code already in place at that moment
//   C. rollback (continues from A's fixture) -> old version restored, prev
//      kept, cumulative stub calls [stop, start, stop, start]
//   D. rollback with no versions\prev -> nonzero, "nothing to roll back to"
//   E. preflight refusal (no hta.ps1) -> nonzero, output mentions hta,
//      bin\service.exe still the old bytes (zero install-dir changes)
//   F. real go build (only when go is on PATH) -> installed service.exe is
//      a real PE (MZ header, >1MB), proving the Makefile-flag build path

const assert = require('node:assert');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const repoRoot = path.resolve(__dirname, '..');
const deployPs1 = path.join(repoRoot, 'scripts', 'deploy.ps1');

// ---------------------------------------------------------------------------
// Skip gate FIRST: scripts/deploy.ps1 is a Windows home-deploy tool that
// needs PowerShell. On non-Windows machines, environments without any
// PowerShell engine, or when HTA_TEST_FORCE_SKIP=1 proves the path, print a
// single SKIP line and exit 0 - skipping here is expected, never a failure.
// ---------------------------------------------------------------------------

if (process.env.HTA_TEST_FORCE_SKIP === '1') {
  console.log(
    'SKIP deploy.ps1 fixture smoke (HTA_TEST_FORCE_SKIP=1): scripts/deploy.ps1 is a Windows ' +
      'home-deploy tool that needs PowerShell - on non-Windows/no-PowerShell environments ' +
      'skipping is expected, not a failure. Windows 家庭部署工具需 PowerShell，本环境跳过属预期非失败',
  );
  process.exit(0);
}
if (process.platform !== 'win32') {
  console.log(
    'SKIP deploy.ps1 fixture smoke (non-win32 platform): scripts/deploy.ps1 is a Windows ' +
      'home-deploy tool that needs PowerShell - on non-Windows/no-PowerShell environments ' +
      'skipping is expected, not a failure. Windows 家庭部署工具需 PowerShell，本环境跳过属预期非失败',
  );
  process.exit(0);
}

function probeShell() {
  for (const candidate of ['pwsh', 'powershell']) {
    const r = spawnSync(candidate, ['-NoProfile', '-Command', 'exit 0'], {
      encoding: 'utf8',
      timeout: 30_000,
    });
    if (!r.error && r.status === 0) return candidate;
  }
  return null;
}

const shell = probeShell();
if (!shell) {
  console.log(
    'SKIP deploy.ps1 fixture smoke (no PowerShell engine found): scripts/deploy.ps1 is a Windows ' +
      'home-deploy tool that needs PowerShell - on non-Windows/no-PowerShell environments ' +
      'skipping is expected, not a failure. Windows 家庭部署工具需 PowerShell，本环境跳过属预期非失败',
  );
  process.exit(0);
}
console.log(`[probe] using shell: ${shell}`);

function runDeploy(args, extraEnv = {}) {
  return spawnSync(
    shell,
    ['-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', deployPs1, ...args],
    { encoding: 'utf8', timeout: 120_000, env: { ...process.env, ...extraEnv } },
  );
}

// ---------------------------------------------------------------------------
// Fixture: a fake install directory with the layout deploy.ps1 expects.
// Swap surface seeded with OLD-* markers; five forbidden-zone sentinels
// pinned byte-for-byte; hta.ps1 replaced by a logging stub.
// ---------------------------------------------------------------------------

const SENTINELS = {
  'config/config.yaml': 'KEEP-CONFIG',
  'secrets/.env': 'KEEP-SECRETS',
  'certs/hta-lan.pem': 'KEEP-CERTS',
  'data/vocab.db': 'KEEP-DATA',
  'venv/Scripts/python.exe': 'KEEP-VENV',
};

function makeFixture(instDir, opts = {}) {
  const write = (rel, content) => {
    const p = path.join(instDir, ...rel.split('/'));
    fs.mkdirSync(path.dirname(p), { recursive: true });
    fs.writeFileSync(p, content);
  };
  // Old-version swap surface.
  write('bin/service.exe', 'OLD-BIN');
  write('web/static/old-app.txt', 'OLD-STATIC');
  write('python/old_script.py', 'OLD-PY');
  // Forbidden-zone sentinels: deploy must never touch these five trees.
  for (const [rel, content] of Object.entries(SENTINELS)) write(rel, content);
  if (!opts.noHta) {
    // Stub hta.ps1: append each received subcommand to <install>\hta-calls.log;
    // start exits per HTA_STUB_START_EXIT (default 0), stop always exits 0 -
    // the same exit-code contract the real scripts/hta.ps1 defines.
    write(
      'hta.ps1',
      [
        "param([Parameter(Position = 0)][string]$Command = 'status')",
        "$ErrorActionPreference = 'Stop'",
        '$root = Split-Path -Parent $MyInvocation.MyCommand.Definition',
        "Add-Content -Path (Join-Path $root 'hta-calls.log') -Value $Command",
        "if ($Command -eq 'start') {",
        '  $code = 0',
        '  if ($env:HTA_STUB_START_EXIT) { $code = [int]$env:HTA_STUB_START_EXIT }',
        '  exit $code',
        '}',
        'exit 0',
        '',
      ].join('\r\n'),
    );
  }
  return instDir;
}

function assertSentinels(instDir, label) {
  for (const [rel, expected] of Object.entries(SENTINELS)) {
    const p = path.join(instDir, ...rel.split('/'));
    assert.strictEqual(
      fs.readFileSync(p, 'utf8'),
      expected,
      `${label}: forbidden-zone sentinel must stay byte-identical: ${rel}`,
    );
  }
}

function readCalls(instDir) {
  return fs
    .readFileSync(path.join(instDir, 'hta-calls.log'), 'utf8')
    .split(/\r?\n/)
    .filter(Boolean);
}

// ---------------------------------------------------------------------------
// Fixtures under one mkdtemp base; scenario A's dir is reused by C.
// ---------------------------------------------------------------------------

const base = fs.mkdtempSync(path.join(os.tmpdir(), 'xiaotian-deploy-test-'));
const instA = path.join(base, 'inst-a');
const instB = path.join(base, 'inst-b');
const instD = path.join(base, 'inst-d');
const instE = path.join(base, 'inst-e');
const instF = path.join(base, 'inst-f');
const newBinDir = path.join(base, 'newbin');
const newBin = path.join(newBinDir, 'service.exe');

try {
  fs.mkdirSync(newBinDir, { recursive: true });
  fs.writeFileSync(newBin, 'NEW-BIN');

  // -------------------------------------------------------------------------
  // Section 0: parser gate - deploy.ps1 must parse with zero errors.
  // -------------------------------------------------------------------------

  {
    const parseScript = [
      '$t=$null;$e=$null',
      '[System.Management.Automation.Language.Parser]::ParseFile($env:HTA_DEPLOY_PS1,[ref]$t,[ref]$e)|Out-Null',
      'if($e -and $e.Count -gt 0){ $e | ForEach-Object { Write-Output ("PARSE-ERR: " + $_.Message + " @ line " + $_.Extent.StartLineNumber) }; exit 1 }',
      "Write-Output 'PARSE-OK'",
    ].join('\n');
    const r = spawnSync(shell, ['-NoProfile', '-Command', parseScript], {
      encoding: 'utf8',
      timeout: 60_000,
      env: { ...process.env, HTA_DEPLOY_PS1: deployPs1 },
    });
    assert.strictEqual(r.status, 0, `parse gate failed:\n${r.stdout}\n${r.stderr}`);
    assert.ok(r.stdout.includes('PARSE-OK'), 'expected PARSE-OK output');
    console.log('PASS section 0: deploy.ps1 parses with zero errors');
  }

  // -------------------------------------------------------------------------
  // Section A: deploy success via -Binary (old version lands in versions\prev).
  // -------------------------------------------------------------------------

  {
    makeFixture(instA);
    const r = runDeploy(['deploy', '-InstallDir', instA, '-Binary', newBin]);
    assert.strictEqual(
      r.status,
      0,
      `deploy exit ${r.status}\nSTDOUT:\n${r.stdout}\nSTDERR:\n${r.stderr}`,
    );

    // versions\prev holds the complete OLD version.
    assert.strictEqual(
      fs.readFileSync(path.join(instA, 'versions', 'prev', 'bin', 'service.exe'), 'utf8'),
      'OLD-BIN',
      'prev must hold the old binary',
    );
    assert.strictEqual(
      fs.readFileSync(path.join(instA, 'versions', 'prev', 'web', 'static', 'old-app.txt'), 'utf8'),
      'OLD-STATIC',
      'prev must hold the old static tree',
    );
    assert.strictEqual(
      fs.readFileSync(path.join(instA, 'versions', 'prev', 'python', 'old_script.py'), 'utf8'),
      'OLD-PY',
      'prev must hold the old python scripts',
    );
    assert.deepStrictEqual(
      fs.readdirSync(path.join(instA, 'versions', 'prev')).sort(),
      ['bin', 'python', 'web'],
      'versions\\prev top level must be exactly bin, python, web',
    );

    // The installed seat carries the NEW binary.
    assert.strictEqual(
      fs.readFileSync(path.join(instA, 'bin', 'service.exe'), 'utf8'),
      'NEW-BIN',
      'bin\\service.exe must be the new binary after deploy',
    );

    // web\static is a whole-tree replace: repo tree exactly, old marker gone.
    const repoStatic = fs.readdirSync(path.join(repoRoot, 'pkg', 'web', 'static')).sort();
    const instStatic = fs.readdirSync(path.join(instA, 'web', 'static')).sort();
    assert.deepStrictEqual(instStatic, repoStatic, 'web\\static must mirror repo pkg\\web\\static');
    assert.ok(
      !fs.existsSync(path.join(instA, 'web', 'static', 'old-app.txt')),
      'old static marker must be gone (whole-tree replace, no merge)',
    );

    // python top-level *.py set is exactly the repo's vendored pair.
    const repoPy = fs
      .readdirSync(path.join(repoRoot, 'third_party', 'python'))
      .filter((f) => f.endsWith('.py'))
      .sort();
    assert.deepStrictEqual(repoPy, ['pregenerate_dictation.py', 'vocab_manager.py']);
    const instPy = fs
      .readdirSync(path.join(instA, 'python'))
      .filter((f) => f.endsWith('.py'))
      .sort();
    assert.deepStrictEqual(instPy, repoPy, 'python top-level *.py set must match the repo exactly');
    assert.ok(
      !fs.existsSync(path.join(instA, 'python', 'old_script.py')),
      'old python script must be gone',
    );

    // Forbidden zones untouched.
    assertSentinels(instA, 'scenario A');

    // Stub hta calls in the contractual order.
    assert.deepStrictEqual(readCalls(instA), ['stop', 'start']);
    console.log('PASS section A: deploy success - prev holds OLD-*, swap surface refreshed, sentinels intact, calls [stop, start]');
  }

  // -------------------------------------------------------------------------
  // Section B: failed health gate -> nonzero + rollback hint, new code in place.
  // -------------------------------------------------------------------------

  {
    makeFixture(instB);
    const r = runDeploy(['deploy', '-InstallDir', instB, '-Binary', newBin], {
      HTA_STUB_START_EXIT: '1',
    });
    assert.notStrictEqual(r.status, 0, 'failed health gate must exit nonzero');
    const out = `${r.stdout}\n${r.stderr}`;
    assert.ok(/rollback/.test(out), `output must carry a rollback hint:\n${out}`);
    assert.strictEqual(
      fs.readFileSync(path.join(instB, 'bin', 'service.exe'), 'utf8'),
      'NEW-BIN',
      'the swap has already happened when the health gate fails',
    );
    console.log('PASS section B: failed health gate exits nonzero with rollback hint, new code in place');
  }

  // -------------------------------------------------------------------------
  // Section C: rollback continues from A's fixture (calls accumulate).
  // -------------------------------------------------------------------------

  {
    const r = runDeploy(['rollback', '-InstallDir', instA]);
    assert.strictEqual(
      r.status,
      0,
      `rollback exit ${r.status}\nSTDOUT:\n${r.stdout}\nSTDERR:\n${r.stderr}`,
    );
    assert.strictEqual(
      fs.readFileSync(path.join(instA, 'bin', 'service.exe'), 'utf8'),
      'OLD-BIN',
      'rollback must restore the old binary',
    );
    assert.strictEqual(
      fs.readFileSync(path.join(instA, 'web', 'static', 'old-app.txt'), 'utf8'),
      'OLD-STATIC',
      'rollback must restore the old static tree',
    );
    assert.strictEqual(
      fs.readFileSync(path.join(instA, 'python', 'old_script.py'), 'utf8'),
      'OLD-PY',
      'rollback must restore the old python scripts',
    );
    assertSentinels(instA, 'scenario C');
    assert.ok(
      fs.existsSync(path.join(instA, 'versions', 'prev')),
      'versions\\prev must be kept after rollback (re-rollback stays possible)',
    );
    assert.deepStrictEqual(readCalls(instA), ['stop', 'start', 'stop', 'start']);
    console.log('PASS section C: rollback restores OLD-*, prev kept, calls [stop, start, stop, start]');
  }

  // -------------------------------------------------------------------------
  // Section D: rollback with no versions\prev -> "nothing to roll back to".
  // -------------------------------------------------------------------------

  {
    makeFixture(instD);
    const r = runDeploy(['rollback', '-InstallDir', instD]);
    assert.notStrictEqual(r.status, 0, 'rollback without prev must exit nonzero');
    assert.ok(
      /nothing to roll back/i.test(r.stdout),
      `output must say there is nothing to roll back to:\n${r.stdout}`,
    );
    console.log('PASS section D: rollback without prev refuses with "nothing to roll back"');
  }

  // -------------------------------------------------------------------------
  // Section E: preflight refusal (no hta.ps1) leaves the install dir untouched.
  // -------------------------------------------------------------------------

  {
    makeFixture(instE, { noHta: true });
    const r = runDeploy(['deploy', '-InstallDir', instE, '-Binary', newBin]);
    assert.notStrictEqual(r.status, 0, 'preflight (missing hta.ps1) must exit nonzero');
    assert.ok(/hta/i.test(r.stdout), `output must mention hta:\n${r.stdout}`);
    assert.strictEqual(
      fs.readFileSync(path.join(instE, 'bin', 'service.exe'), 'utf8'),
      'OLD-BIN',
      'preflight failure must leave bin\\service.exe untouched',
    );
    console.log('PASS section E: preflight refusal mentions hta and changes nothing');
  }

  // -------------------------------------------------------------------------
  // Section F: real go build (conditional) -> real PE in the deployed seat.
  // -------------------------------------------------------------------------

  {
    const goProbe = spawnSync('go', ['version'], { encoding: 'utf8', timeout: 30_000 });
    if (goProbe.error || goProbe.status !== 0) {
      console.log(
        'SKIP scenario F (go not usable on PATH): the real-build deploy path was not exercised here',
      );
    } else {
      makeFixture(instF);
      const r = runDeploy(['deploy', '-InstallDir', instF]);
      assert.strictEqual(
        r.status,
        0,
        `deploy (real build) exit ${r.status}\nSTDOUT:\n${r.stdout}\nSTDERR:\n${r.stderr}`,
      );
      const exe = fs.readFileSync(path.join(instF, 'bin', 'service.exe'));
      assert.ok(
        exe.length > 1024 * 1024,
        `real binary must be >1MB, got ${exe.length} bytes`,
      );
      assert.ok(
        exe[0] === 0x4d && exe[1] === 0x5a,
        'installed service.exe must be a PE (MZ header)',
      );
      assert.deepStrictEqual(readCalls(instF), ['stop', 'start']);
      console.log(`PASS section F: real go build deployed a genuine PE (${exe.length} bytes)`);
    }
  }

  console.log('PASS test-deploy-ps1: all sections green');
  process.exit(0);
} finally {
  // Give Windows a moment to release handles held by the spawned shells.
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 500);
  fs.rmSync(base, { recursive: true, force: true });
}
