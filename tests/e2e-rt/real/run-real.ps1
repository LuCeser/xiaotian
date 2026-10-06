# run-real.ps1 — L3 isolated-instance E2E launcher (layer 3 of the four-layer
# E2E isolation design; see docs/E2E-ISOLATION-SPEC.md).
#
# RUNBOOK
#   Run:      npm run e2e:rt-real
#             (or: powershell -NoProfile -ExecutionPolicy Bypass -File tests/e2e-rt/real/run-real.ps1
#                  [-EnvFile <path-to-.env>] [-Python <venv-python.exe>])
#   Duration: ~4 minutes (full 10-word dictation lesson at real-time pace).
#   Evidence: .scratch/e2e-rt-data/ (gitignored) — assertion matrix
#             e2e-results.json, JSONL timelines, server log. Raw evidence
#             never enters git (T-13-30).
#
# PREREQUISITES
#   - .env carrying VOLCENGINE_TTS_* / MIMO_API_KEY (search order: explicit
#     -EnvFile param > repo-root .env > the git main checkout's .env when
#     this repo is a linked worktree — never hardcoded machine paths).
#   - venv python interpreter with sqlite3 (search order: explicit -Python
#     param > repo-root venvs > the git main checkout's venvs).
#   - go and node on PATH; ports 127.0.0.1:8790 and :8791 FREE (the script
#     refuses to start — and never kills anything by port — if occupied).
#
# ISOLATION / SAFETY CONTRACT
#   - Boots bin/service-e2e.exe (independent binary name — never overwrites a
#     running bin/service.exe) with tests/e2e-rt/real/config.e2e.yaml:
#     independent ports 8790/8791, independent DB under .scratch/e2e-rt-data/,
#     pinned rt.lessonId=e2e-rt-lesson. Zero contact with production.
#   - Fresh-DB lifecycle: .scratch/e2e-rt-data/ is deleted and recreated on
#     every run — idempotent, no residue, every run a brand-new lesson.
#   - Teardown kills ONLY the PID this script recorded at Start-Process
#     (finally-bounded). It NEVER stops processes by port and never touches
#     bin/service.exe or any other instance.
param(
    [string]$EnvFile,
    [string]$Python
)

$ErrorActionPreference = 'Stop'

# Repo root: this script lives at tests/e2e-rt/real/ (three levels deep).
$root = (Resolve-Path (Join-Path $PSScriptRoot '..\..\..')).Path
Set-Location $root
$dataDir = Join-Path $root '.scratch\e2e-rt-data'
$svcBin  = Join-Path $root 'bin\service-e2e.exe'
$svcCfg  = 'tests/e2e-rt/real/config.e2e.yaml'
$driver  = 'tests/e2e-rt/real/driver-rt-lesson.mjs'

# Git main checkout root (when this repo is a linked worktree) — used ONLY
# to BORROW .env / the venv interpreter / locate the production DB path for
# the driver's read-only isolation proofs. Derived from git metadata at
# runtime; no machine path is written into this script.
function Get-MainCheckoutRoot {
    try {
        $commonDir = (& git rev-parse --git-common-dir 2>$null)
        if ($LASTEXITCODE -ne 0 -or -not $commonDir) { return $null }
        $commonDir = $commonDir.Trim()
        if (-not [System.IO.Path]::IsPathRooted($commonDir)) {
            $commonDir = Join-Path $root $commonDir
        }
        return (Resolve-Path (Join-Path $commonDir '..')).Path
    } catch { return $null }
}
$mainRoot = Get-MainCheckoutRoot

# --- resolve .env -----------------------------------------------------------
$envFileResolved = $null
foreach ($cand in @($EnvFile, (Join-Path $root '.env'),
        $(if ($mainRoot) { Join-Path $mainRoot '.env' } else { $null }))) {
    if ($cand -and (Test-Path $cand)) { $envFileResolved = $cand; break }
}
if (-not $envFileResolved) {
    Write-Host "ERROR: no .env found (searched: -EnvFile param, $root\.env$(if ($mainRoot) { ", $mainRoot\.env" }))." -ForegroundColor Red
    Write-Host "The isolated instance needs VOLCENGINE_TTS_* / MIMO_API_KEY to speak. Pass -EnvFile <path>."
    exit 1
}

# --- resolve venv python interpreter ----------------------------------------
$pythonResolved = $null
$pyCandidates = @($Python,
    (Join-Path $root 'venv\Scripts\python.exe'),
    (Join-Path $root '.venv\Scripts\python.exe'),
    (Join-Path $root 'third_party\python\.venv\Scripts\python.exe'))
if ($mainRoot) {
    $pyCandidates += (Join-Path $mainRoot 'venv\Scripts\python.exe')
    $pyCandidates += (Join-Path $mainRoot 'third_party\python\.venv\Scripts\python.exe')
}
foreach ($cand in $pyCandidates) {
    if ($cand -and (Test-Path $cand)) { $pythonResolved = $cand; break }
}
if (-not $pythonResolved) {
    Write-Host "ERROR: no venv python interpreter found (searched: -Python param, repo-root venv/.venv/third_party venv$(if ($mainRoot) { ", main-checkout venvs under $mainRoot" }))." -ForegroundColor Red
    Write-Host "Create one (uv venv --python 3.12 + pip install -r third_party/python/requirements.txt) or pass -Python <path>."
    exit 1
}
if ($Python -and -not (Test-Path $Python)) {
    Write-Host "ERROR: -Python '$Python' does not exist." -ForegroundColor Red
    exit 1
}

Write-Host "[run-real] root: $root"
Write-Host "[run-real] env file: $envFileResolved"
Write-Host "[run-real] python:   $pythonResolved"

# --- toolchain sanity -------------------------------------------------------
foreach ($tool in @('go', 'node', 'git')) {
    if (-not (Get-Command $tool -ErrorAction SilentlyContinue)) {
        Write-Host "ERROR: '$tool' not on PATH." -ForegroundColor Red
        exit 1
    }
}

# --- 1. load .env into THIS process (run.ps1 parsing loop, verbatim) --------
$loaded = 0
Get-Content $envFileResolved | ForEach-Object {
    $line = $_.Trim()
    if (-not $line -or $line.StartsWith('#')) { return }
    if ($line.StartsWith('export ')) { $line = $line.Substring(7).Trim() }
    $idx = $line.IndexOf('=')
    if ($idx -le 0) { return }
    $key = $line.Substring(0, $idx).Trim()
    $val = $line.Substring($idx + 1).Trim()
    if (($val.StartsWith('"')  -and $val.EndsWith('"'))  -or
        ($val.StartsWith("'")  -and $val.EndsWith("'"))) {
        $val = $val.Substring(1, $val.Length - 2)
    }
    [Environment]::SetEnvironmentVariable($key, $val, 'Process')
    $loaded++
}
Write-Host "[run-real] loaded $loaded var(s) from $(Split-Path -Leaf $envFileResolved)"

# --- 2. port preflight: 8790/8791 must be FREE (fail loudly, never kill) ----
foreach ($port in @(8790, 8791)) {
    $existing = Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue
    if ($existing) {
        $pids = ($existing.OwningProcess | Sort-Object -Unique) -join ', '
        Write-Host "ERROR: port $port is already in use (PID(s): $pids). Free it manually —" -ForegroundColor Red
        Write-Host "this launcher never stops processes by port (only its own recorded PID)."
        exit 1
    }
}

# --- 3. fresh-DB lifecycle: delete and recreate the isolated data dir --------
if (Test-Path $dataDir) {
    Remove-Item -Recurse -Force $dataDir
}
New-Item -ItemType Directory -Force -Path $dataDir | Out-Null
Write-Host "[run-real] fresh isolated data dir: $dataDir"

# --- 4. build the INDEPENDENTLY-NAMED binary ---------------------------------
New-Item -ItemType Directory -Force -Path (Join-Path $root 'bin') | Out-Null
Write-Host "[run-real] building service-e2e.exe..." -ForegroundColor Cyan
go build -o $svcBin ./cmd/service
if ($LASTEXITCODE -ne 0) { Write-Host "ERROR: go build failed" -ForegroundColor Red; exit 1 }

# --- 5. boot the isolated instance (detached; stderr → server-e2e.err.log) ---
$logErr = Join-Path $dataDir 'server-e2e.err.log'
$logOut = Join-Path $dataDir 'server-e2e.out.log'
$env:HTA_PYTHON_INTERPRETER = $pythonResolved
$svc = Start-Process -FilePath $svcBin `
    -ArgumentList '-config', $svcCfg `
    -WorkingDirectory $root -WindowStyle Hidden `
    -RedirectStandardError $logErr -RedirectStandardOutput $logOut -PassThru
$svcPid = $svc.Id
Write-Host "[run-real] isolated instance started, PID $svcPid (8790/8791; logs: server-e2e.err.log)"

$nodeExit = 1
try {
    # --- 6. health wait: TCP connect to 127.0.0.1:8791, bounded 30 s ----------
    $ready = $false
    foreach ($i in 1..60) {
        if ($svc.HasExited) { break }
        $client = New-Object System.Net.Sockets.TcpClient
        try {
            $connectTask = $client.ConnectAsync('127.0.0.1', 8791)
            if ($connectTask.Wait(250) -and $client.Connected) { $ready = $true; break }
        } catch { } finally { $client.Close() }
        Start-Sleep -Milliseconds 250
    }
    if (-not $ready) {
        Write-Host "ERROR: isolated instance not listening on 127.0.0.1:8791 within 30s (exited=$($svc.HasExited))." -ForegroundColor Red
        Write-Host "[run-real] last log lines (server-e2e.err.log):" -ForegroundColor Yellow
        Get-Content $logErr -Tail 15 -ErrorAction SilentlyContinue
        exit 1
    }
    Write-Host "[run-real] health listener up — launching driver" -ForegroundColor Green

    # --- 7. run the driver with the E2E_RT_* environment ---------------------
    $env:E2E_RT_URL         = 'ws://127.0.0.1:8790/rt'
    $env:E2E_RT_ORIGIN      = 'http://127.0.0.1:8790'
    $env:E2E_RT_DB          = (Join-Path $dataDir 'vocab.db')
    $env:E2E_RT_PY          = $pythonResolved
    $env:E2E_RT_SERVER_LOG  = $logErr
    $env:E2E_RT_OUT         = $dataDir
    $env:E2E_RT_LESSON      = 'e2e-rt-lesson'
    $env:E2E_RT_FIXTURES    = (Join-Path $root 'tests\e2e-rt\real\fixtures')
    $prodDb = $null
    if ($mainRoot) {
        $cand = Join-Path $mainRoot '.hta-data\vocab.db'
        if (Test-Path $cand) { $prodDb = $cand }
    }
    if ($prodDb) { $env:E2E_RT_PROD_DB = $prodDb }
    else { Remove-Item Env:E2E_RT_PROD_DB -ErrorAction SilentlyContinue }

    & node $driver
    $nodeExit = $LASTEXITCODE
} finally {
    # --- 8. teardown: ONLY the PID this script recorded. Never by port. ------
    if ($svcPid) {
        $alive = Get-Process -Id $svcPid -ErrorAction SilentlyContinue
        if ($alive) {
            Stop-Process -Id $svcPid -Force -ErrorAction SilentlyContinue
            Write-Host "[run-real] stopped isolated instance (PID $svcPid)"
        } else {
            Write-Host "[run-real] isolated instance (PID $svcPid) already exited"
        }
    }
}

exit $nodeExit
