// Fixture smoke test for scripts/install.ps1 (quick task 261024-njn).
//
// Follows the tests/test-start-detached.js pattern: mkdtemp fixtures,
// spawnSync, bounded timeouts, finally cleanup with a Windows handle-release
// wait. Zero network: the venv step is driven by a stub uv.cmd that records
// its arguments and fakes the venv\Scripts\python.exe artifact, so the uv
// call contract (Makefile py-bootstrap shape, Tsinghua index) is asserted
// without ever touching a real package index.
//
// Sections:
//   0. PS 5.1 parser gate on scripts/install.ps1 (zero parse errors)
//   1. positive run -> full install layout (11 artifact classes)
//   2. generated config key-by-key assertions (ports, absolute paths,
//      TLS pair, double allowedOrigins, forceStudentMode, no /opt/hta)
//   3. stub uv call contract (--python 3.12 venv + pip install -r, index URL)
//   4. INSTALL-NOTES.md (entry URLs, firewall text, cert expiry or fallback)
//   5. idempotent second run -> protected files byte-identical
//   6. negative trio -> dev-port refusal, identical-port refusal, missing
//      payload list (all missing items in ONE output)

const assert = require('node:assert');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const repoRoot = path.resolve(__dirname, '..');
const installer = path.join(repoRoot, 'scripts', 'install.ps1');
const PY_INDEX = 'https://pypi.tuna.tsinghua.edu.cn/simple';

// ---------------------------------------------------------------------------
// Shell probe: prefer pwsh, fall back to powershell; neither -> SKIP.
// ---------------------------------------------------------------------------

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
  console.log('SKIP install.ps1 fixture smoke: neither pwsh nor powershell is available on PATH');
  process.exit(0);
}
console.log(`[probe] using shell: ${shell}`);

function runInstaller(args, extraEnv = {}) {
  return spawnSync(
    shell,
    ['-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', installer, ...args],
    { encoding: 'utf8', timeout: 180_000, env: { ...process.env, ...extraEnv } },
  );
}

function mustExist(p, label) {
  assert.ok(fs.existsSync(p), `expected to exist: ${label || p} (${p})`);
}

// ---------------------------------------------------------------------------
// Section 0: parser gate - install.ps1 must parse with zero errors.
// ---------------------------------------------------------------------------

{
  const parseScript = [
    '$t=$null;$e=$null',
    '[System.Management.Automation.Language.Parser]::ParseFile($env:HTA_INSTALLER_PATH,[ref]$t,[ref]$e)|Out-Null',
    'if($e -and $e.Count -gt 0){ $e | ForEach-Object { Write-Output ("PARSE-ERR: " + $_.Message + " @ line " + $_.Extent.StartLineNumber) }; exit 1 }',
    "Write-Output 'PARSE-OK'",
  ].join('\n');
  const r = spawnSync(shell, ['-NoProfile', '-Command', parseScript], {
    encoding: 'utf8',
    timeout: 60_000,
    env: { ...process.env, HTA_INSTALLER_PATH: installer },
  });
  assert.strictEqual(r.status, 0, `parse gate failed:\n${r.stdout}\n${r.stderr}`);
  assert.ok(r.stdout.includes('PARSE-OK'), 'expected PARSE-OK output');
  console.log('PASS section 0: install.ps1 parses with zero errors');
}

// ---------------------------------------------------------------------------
// Fixture build
// ---------------------------------------------------------------------------

const srcDir = fs.mkdtempSync(path.join(os.tmpdir(), 'xiaotian-install-src-'));
const instDir = fs.mkdtempSync(path.join(os.tmpdir(), 'xiaotian-install-dst-'));
const stubDir = fs.mkdtempSync(path.join(os.tmpdir(), 'xiaotian-install-stub-'));
const stubLog = path.join(stubDir, 'uv-stub.log');

function write(rel, content) {
  const p = path.join(srcDir, rel);
  fs.mkdirSync(path.dirname(p), { recursive: true });
  fs.writeFileSync(p, content);
}

try {
  // Source payload fixture (mirrors the installer's inventory list).
  fs.copyFileSync(path.join(repoRoot, 'config.example.yaml'), path.join(srcDir, 'config.example.yaml'));
  write('bin/service.exe', 'dummy service binary');
  write('pkg/web/static/index.html', '<!doctype html><title>fixture</title>');
  write('pkg/web/static/js/app.js', '// fixture');
  write('third_party/python/vocab_manager.py', '# fixture\n');
  write('third_party/python/pregenerate_dictation.py', '# fixture\n');
  fs.copyFileSync(
    path.join(repoRoot, 'third_party', 'python', 'requirements.txt'),
    path.join(srcDir, 'third_party', 'python', 'requirements.txt'),
  );
  write('third_party/python/.env.example', '# fixture template (no real secrets)\n');
  write('scripts/hta.ps1', "Write-Host 'dummy hta.ps1 fixture'\n");

  // Certificates: prefer the repo's real mkcert pair (exercises the expiry
  // date branch); fall back to a deliberately invalid PEM (exercises the
  // "expiry unknown" degradation branch - install must still succeed).
  let usingRealCerts = false;
  const realPem = path.join(repoRoot, '.local-certs', 'hta-lan.pem');
  const realKey = path.join(repoRoot, '.local-certs', 'hta-lan-key.pem');
  if (fs.existsSync(realPem) && fs.existsSync(realKey)) {
    fs.mkdirSync(path.join(srcDir, '.local-certs'), { recursive: true });
    fs.copyFileSync(realPem, path.join(srcDir, '.local-certs', 'hta-lan.pem'));
    fs.copyFileSync(realKey, path.join(srcDir, '.local-certs', 'hta-lan-key.pem'));
    usingRealCerts = true;
  } else {
    write('.local-certs/hta-lan.pem', '-----BEGIN CERTIFICATE-----\n!!!not-valid-base64!!!\n-----END CERTIFICATE-----\n');
    write('.local-certs/hta-lan-key.pem', '-----BEGIN PRIVATE KEY-----\n!!!not-valid-base64!!!\n-----END PRIVATE KEY-----\n');
  }

  // Stub uv.cmd: records its argument line plus the UV_INDEX_URL it saw, and
  // materializes <target>\Scripts\python.exe for `uv venv ... <target>`.
  write(
    '../stub-uv.cmd',
    [
      '@echo off',
      'echo ARGS: %*>> "%UV_STUB_LOG%"',
      'echo UV_INDEX_URL=%UV_INDEX_URL%>> "%UV_STUB_LOG%"',
      'if /I "%~1"=="venv" (',
      '  if not exist "%~4\\Scripts" mkdir "%~4\\Scripts"',
      '  type nul > "%~4\\Scripts\\python.exe"',
      ')',
      'exit /b 0',
      '',
    ].join('\r\n'),
  );
  fs.renameSync(path.join(srcDir, '..', 'stub-uv.cmd'), path.join(stubDir, 'uv.cmd'));

  const stubEnv = {
    PATH: stubDir + path.delimiter + process.env.PATH,
    UV_STUB_LOG: stubLog,
  };
  const baseArgs = [
    '-SkipFirewall',
    '-LanIp', '192.0.2.10',
    '-PublicPort', '9443',
    '-HealthPort', '9444',
    '-InstallDir', instDir,
    '-SourceDir', srcDir,
  ];

  // ---------------------------------------------------------------------------
  // Section 1: positive run -> complete layout
  // ---------------------------------------------------------------------------

  {
    const r = runInstaller(baseArgs, stubEnv);
    assert.strictEqual(r.status, 0, `installer exit ${r.status}\nSTDOUT:\n${r.stdout}\nSTDERR:\n${r.stderr}`);
    const layout = [
      path.join(instDir, 'bin', 'service.exe'),
      path.join(instDir, 'web', 'static', 'index.html'),
      path.join(instDir, 'web', 'static', 'js', 'app.js'),
      path.join(instDir, 'python', 'vocab_manager.py'),
      path.join(instDir, 'python', 'pregenerate_dictation.py'),
      path.join(instDir, 'python', 'requirements.txt'),
      path.join(instDir, 'config', 'config.yaml'),
      path.join(instDir, 'certs', 'hta-lan.pem'),
      path.join(instDir, 'certs', 'hta-lan-key.pem'),
      path.join(instDir, 'secrets', '.env'),
      path.join(instDir, 'venv', 'Scripts', 'python.exe'),
      path.join(instDir, 'data'),
      path.join(instDir, 'logs'),
      path.join(instDir, 'hta.ps1'),
      path.join(instDir, 'INSTALL-NOTES.md'),
    ];
    for (const p of layout) mustExist(p);
    assert.ok(
      r.stdout.includes('Initializing vocab schema'),
      'installer must run the vocab schema init step (issue #78)',
    );
    console.log('PASS section 1: positive run exit 0, full install layout present');
  }

  // ---------------------------------------------------------------------------
  // Section 2: generated config key-by-key
  // ---------------------------------------------------------------------------

  {
    const cfg = fs.readFileSync(path.join(instDir, 'config', 'config.yaml'), 'utf8');
    assert.ok(cfg.includes('addr: "0.0.0.0:9443"'), 'addr must use 9443');
    assert.ok(cfg.includes('healthAddr: "127.0.0.1:9444"'), 'healthAddr must use 9444');
    assert.ok(cfg.includes(`staticDir: '${path.join(instDir, 'web', 'static')}'`), 'staticDir must be an absolute install path');
    assert.ok(cfg.includes(`certFile: '${path.join(instDir, 'certs', 'hta-lan.pem')}'`), 'tls.certFile must point at the copied cert');
    assert.ok(cfg.includes(`keyFile: '${path.join(instDir, 'certs', 'hta-lan-key.pem')}'`), 'tls.keyFile must point at the copied key');
    assert.ok(cfg.includes(`interpreter: '${path.join(instDir, 'venv', 'Scripts', 'python.exe')}'`), 'interpreter must be the Windows-layout venv python');
    assert.ok(cfg.includes(`scriptsDir: '${path.join(instDir, 'python')}'`), 'scriptsDir must be the install python dir');
    assert.ok(cfg.includes(`vocabScript: '${path.join(instDir, 'python', 'vocab_manager.py')}'`), 'vocabScript must be the full install path (bridge launches it verbatim)');
    assert.ok(cfg.includes(`pregenScript: '${path.join(instDir, 'python', 'pregenerate_dictation.py')}'`), 'pregenScript must be the full install path (bridge launches it verbatim)');
    const dataDir = path.join(instDir, 'data');
    assert.ok(cfg.includes(`vocabDBPath: '${path.join(dataDir, 'vocab.db')}'`), 'vocabDBPath must live in the data dir');
    assert.ok(cfg.includes(`dataDir: '${dataDir}'`), 'dataDir must be absolute');
    assert.ok(cfg.includes(`cacheDir: '${path.join(dataDir, 'cache', 'audio')}'`), 'cacheDir must live in the data dir');
    assert.ok(cfg.includes(`manifestDir: '${path.join(dataDir, 'cache')}'`), 'manifestDir must live in the data dir');
    assert.ok(cfg.includes(`envFile: '${path.join(dataDir, 'dictation.env')}'`), 'envFile must live in the data dir');
    const originLines = cfg.split('\n').filter((l) => /^  allowedOrigins: /.test(l) && l.includes('192.0.2.10'));
    assert.strictEqual(originLines.length, 2, 'ws AND rt allowedOrigins must both carry the LAN IP');
    assert.ok(cfg.includes('forceStudentMode: true'), 'rt.forceStudentMode must be true');
    assert.ok(!cfg.includes('/opt/hta'), 'no /opt/hta residue allowed');
    assert.ok(!cfg.includes('0.0.0.0:8080') && !cfg.includes('127.0.0.1:8081'), 'no dev-port residue allowed');
    console.log('PASS section 2: generated config matches every transform key');
  }

  // ---------------------------------------------------------------------------
  // Section 3: stub uv call contract
  // ---------------------------------------------------------------------------

  {
    const log = fs.readFileSync(stubLog, 'utf8');
    const venvTarget = path.join(instDir, 'venv');
    assert.ok(log.includes(`venv --python 3.12 ${venvTarget}`), `uv venv call contract missing from stub log:\n${log}`);
    const venvPy = path.join(instDir, 'venv', 'Scripts', 'python.exe');
    const reqPath = path.join(instDir, 'python', 'requirements.txt');
    assert.ok(
      log.includes(`pip install --python ${venvPy} -r ${reqPath}`),
      `uv pip install call contract missing from stub log:\n${log}`,
    );
    assert.ok(log.includes(`UV_INDEX_URL=${PY_INDEX}`), 'installer must set UV_INDEX_URL to the Tsinghua index for uv');
    console.log('PASS section 3: uv venv + pip install call contract (Tsinghua index, Windows layout)');
  }

  // ---------------------------------------------------------------------------
  // Section 4: INSTALL-NOTES.md
  // ---------------------------------------------------------------------------

  {
    const notes = fs.readFileSync(path.join(instDir, 'INSTALL-NOTES.md'), 'utf8');
    assert.ok(notes.includes('https://192.0.2.10:9443/tutor/'), 'notes must carry the iPad student entry URL');
    assert.ok(notes.includes('parent.html'), 'notes must carry the parent page URL');
    assert.ok(notes.includes('netsh advfirewall'), 'notes must carry the firewall command text');
    assert.ok(notes.includes('hta.ps1 start'), 'notes must carry the start hint');
    assert.ok(notes.includes('secrets\\.env'), 'notes must mention the secrets file (name only)');
    if (usingRealCerts) {
      assert.ok(/\b(19|20)\d\d-\d\d-\d\d\b/.test(notes), 'real-cert branch: notes must contain the parsed expiry date');
      assert.ok(!notes.includes('expiry: unknown'), 'real-cert branch must not degrade');
    } else {
      assert.ok(notes.includes('unknown'), 'fake-PEM branch: notes must degrade to an explicit unknown expiry');
      assert.ok(notes.includes('openssl x509'), 'fake-PEM branch: notes must carry the manual check command');
    }
    console.log(`PASS section 4: INSTALL-NOTES.md (${usingRealCerts ? 'real-cert' : 'degraded'} branch)`);
  }

  // ---------------------------------------------------------------------------
  // Section 5: idempotent second run
  // ---------------------------------------------------------------------------

  {
    const cfgPath = path.join(instDir, 'config', 'config.yaml');
    const envPath = path.join(instDir, 'secrets', '.env');
    const certPath = path.join(instDir, 'certs', 'hta-lan.pem');
    const keepPath = path.join(instDir, 'data', 'keep.txt');

    fs.appendFileSync(cfgPath, '\n# MARKER-CFG\n');
    fs.appendFileSync(envPath, '\nMARKER-ENV=1\n');
    fs.appendFileSync(certPath, '\nMARKER-CERT\n');
    fs.writeFileSync(keepPath, 'keep me\n');

    const before = {
      cfg: fs.readFileSync(cfgPath, 'utf8'),
      env: fs.readFileSync(envPath, 'utf8'),
      cert: fs.readFileSync(certPath, 'utf8'),
      keep: fs.readFileSync(keepPath, 'utf8'),
    };

    const r = runInstaller(baseArgs, stubEnv);
    assert.strictEqual(r.status, 0, `second run exit ${r.status}\nSTDOUT:\n${r.stdout}\nSTDERR:\n${r.stderr}`);
    assert.ok(r.stdout.includes('keeping existing config'), 'second run must print the config keep-hint');

    assert.strictEqual(fs.readFileSync(cfgPath, 'utf8'), before.cfg, 'config.yaml must be byte-identical on re-run');
    assert.strictEqual(fs.readFileSync(envPath, 'utf8'), before.env, 'secrets\\.env must be byte-identical on re-run');
    assert.strictEqual(fs.readFileSync(certPath, 'utf8'), before.cert, 'cert must be byte-identical on re-run');
    assert.strictEqual(fs.readFileSync(keepPath, 'utf8'), before.keep, 'data dir contents must be untouched');
    console.log('PASS section 5: idempotent second run - all four protected artifacts byte-identical');
  }

  // ---------------------------------------------------------------------------
  // Section 6: negative trio
  // ---------------------------------------------------------------------------

  {
    const devPort = runInstaller([...baseArgs.slice(0, baseArgs.indexOf('-PublicPort') + 1), '8080'], stubEnv);
    assert.notStrictEqual(devPort.status, 0, '-PublicPort 8080 must be refused');
    assert.ok(/8080/.test(devPort.stdout) && /development-reserved/.test(devPort.stdout),
      `refusal must name the dev-reserved port:\n${devPort.stdout}`);

    const samePorts = runInstaller([
      '-SkipFirewall', '-LanIp', '192.0.2.10', '-PublicPort', '9555', '-HealthPort', '9555',
      '-InstallDir', instDir, '-SourceDir', srcDir,
    ], stubEnv);
    assert.notStrictEqual(samePorts.status, 0, 'identical public/health ports must be refused');

    fs.rmSync(path.join(srcDir, 'bin', 'service.exe'));
    fs.rmSync(path.join(srcDir, 'scripts', 'hta.ps1'));
    const missing = runInstaller(baseArgs, stubEnv);
    assert.notStrictEqual(missing.status, 0, 'missing payload must be refused');
    assert.ok(missing.stdout.includes('service.exe') && missing.stdout.includes('hta.ps1'),
      `one run must list ALL missing items (service.exe AND hta.ps1):\n${missing.stdout}`);
    console.log('PASS section 6: negative trio (dev port, identical ports, missing payload listed in full)');
  }

  console.log('PASS test-install: all sections green');
  process.exit(0);
} finally {
  // Give Windows a moment to release handles held by the spawned shells.
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 500);
  for (const dir of [srcDir, instDir, stubDir]) {
    fs.rmSync(dir, { recursive: true, force: true });
  }
}
