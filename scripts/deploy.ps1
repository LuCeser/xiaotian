# deploy.ps1 - one-command upgrade / rollback for a DEPLOYED Home Tutor Agent
# install directory (the layout scripts\install.ps1 creates and the sibling
# hta.ps1 controls day-to-day).
#
# WHAT "deploy" DOES (the default action), in a FIXED order:
#   a. Preflight the install dir and the repo-side payload - any miss exits
#      with the install directory UNTOUCHED.
#   b. Resolve the new service.exe: -Binary if given (must exist - no silent
#      fallback to a local build, or a typo would deploy different code than
#      you intended), otherwise build from THIS repo into a TEMP staging dir.
#      The build happens BEFORE the stop ON PURPOSE: a failed build must
#      never take the running service down. Staging never lands in the
#      repo's bin\ - that exe belongs to run.ps1 on the dev machine.
#   c. Stop the running instance via the install dir's hta.ps1 (as a
#      SUBPROCESS - see Invoke-Hta for why a dot-call would be fatal).
#   d. Back the CURRENT bin\web\python up to versions\prev BEFORE any swap,
#      so prev always holds a complete old version. Single slot: an existing
#      prev is discarded first, which also makes re-running deploy idempotent.
#   e. Swap in the new binary, a WHOLE-TREE web\static replace (no merge -
#      files deleted upstream must not linger) and the new top-level
#      third_party\python *.py scripts (top-level files only).
#   f. Start via hta.ps1. Its exit code IS the health gate: hta start waits
#      up to 10s for the new process to own the health listener, so deploy
#      runs no second health probe. Failure prints a single copy-paste
#      rollback command - the old version is sitting in versions\prev.
#
# "rollback" swaps versions\prev back into bin\web\python and restarts via
# hta.ps1. prev is KEPT after a rollback, so rollback can be re-run.
#
# SAFETY INVARIANT (write-surface whitelist):
#   The ONLY paths this script writes, replaces or deletes are:
#     <InstallDir>\bin\, <InstallDir>\web\, <InstallDir>\python\,
#     <InstallDir>\versions\   and the TEMP staging directory.
#   It NEVER reads, writes or deletes config, secrets, certs, data
#   directories or the venv. All runtime data lives OUTSIDE that swap
#   surface, driven by config.yaml's python block keys (vocabDBPath /
#   dataDir / cacheDir / manifestDir / envFile) - which is why swapping
#   exactly these three directories upgrades the service with zero Go code
#   changes. There is deliberately no wildcard delete at the InstallDir
#   root: every Remove-Item names a specific subdirectory.
#
# Usage:
#   .\scripts\deploy.ps1 -InstallDir D:\hta                # build + upgrade
#   .\scripts\deploy.ps1 -InstallDir D:\hta -Binary C:\path\service.exe
#       # deploy a PRE-BUILT binary - only use binaries you trust; anything
#       # given here is placed into the deployed bin\service.exe seat as-is
#   .\scripts\deploy.ps1 rollback -InstallDir D:\hta       # roll back
#
# Windows PowerShell 5.1 compatible: no pwsh-only syntax, English-only output
# (5.1 codepage safety). Same style as run.ps1 / stop.ps1 / hta.ps1.

param(
    [Parameter(Position = 0)][ValidateSet('deploy','rollback')][string]$Action = 'deploy',
    [Parameter(Mandatory = $true)][string]$InstallDir,
    [string]$Binary
)

$ErrorActionPreference = 'Stop'
$RepoRoot = Split-Path -Parent $PSScriptRoot

# ---------------------------------------------------------------------------
# Shared paths (resolved once; every write below stays inside the whitelist).
# ---------------------------------------------------------------------------

$HtaPath    = Join-Path $InstallDir 'hta.ps1'
$InstBin    = Join-Path $InstallDir 'bin'
$InstBinExe = Join-Path $InstBin 'service.exe'
$InstWeb    = Join-Path $InstallDir 'web'
$InstStatic = Join-Path $InstWeb 'static'
$InstPython = Join-Path $InstallDir 'python'
$Versions   = Join-Path $InstallDir 'versions'
$Prev       = Join-Path $Versions 'prev'
$StaticSrc  = Join-Path $RepoRoot 'pkg\web\static'
$PySrc      = Join-Path $RepoRoot 'third_party\python'

function Invoke-Hta {
    # Run the install dir's hta.ps1 as a SUBPROCESS and return its exit code.
    # Do NOT dot-call (`& $HtaPath stop`): a dot-called child script's `exit 1`
    # executes in THIS process and would terminate deploy.ps1 itself. A
    # subprocess keeps the stop/start contract a plain $LASTEXITCODE integer
    # we can branch on, while the child's console output still reaches the user.
    param([string]$SubCommand)
    & powershell.exe -NoProfile -ExecutionPolicy Bypass -File $HtaPath $SubCommand | Out-Host
    return [int]$LASTEXITCODE
}

function Invoke-Deploy {
    # ---- a. Preflight: anything missing -> exit, install dir UNTOUCHED.
    $missing = @()
    if (-not (Test-Path -LiteralPath $InstallDir -PathType Container)) { $missing += "install dir not found: $InstallDir" }
    if (-not (Test-Path -LiteralPath $HtaPath -PathType Leaf))         { $missing += "hta.ps1 not found in install dir: $HtaPath" }
    if (-not (Test-Path -LiteralPath $InstBinExe -PathType Leaf))      { $missing += "existing bin\service.exe not found: $InstBinExe (does this dir look like a real install?)" }
    if (-not (Test-Path -LiteralPath $StaticSrc -PathType Container))  { $missing += "repo static payload not found: $StaticSrc" }
    if (-not (Get-ChildItem -LiteralPath $PySrc -Filter '*.py' -File -ErrorAction SilentlyContinue)) { $missing += "repo python scripts not found: $PySrc\*.py" }
    if ($missing.Count -gt 0) {
        foreach ($m in $missing) { Write-Host "[deploy.ps1] PREFLIGHT ERROR: $m" -ForegroundColor Red }
        Write-Host "[deploy.ps1] aborting before any change - the install directory is UNTOUCHED" -ForegroundColor Red
        exit 1
    }

    # ---- b. Resolve/build the new binary. Build (or -Binary check) runs
    # BEFORE the stop: a failure here leaves the old instance running and
    # the install dir untouched.
    $newExe = $null
    $staging = $null
    if ($Binary) {
        if (-not (Test-Path -LiteralPath $Binary -PathType Leaf)) {
            Write-Host "[deploy.ps1] ERROR: -Binary was given but the file does not exist: $Binary" -ForegroundColor Red
            Write-Host "[deploy.ps1] refusing to silently fall back to a local build (a path typo would deploy different code than intended). Install dir UNTOUCHED." -ForegroundColor Red
            exit 1
        }
        $newExe = (Resolve-Path -LiteralPath $Binary).Path
        Write-Host "[deploy.ps1] using pre-built binary: $newExe" -ForegroundColor Cyan
    } else {
        $staging = Join-Path ([System.IO.Path]::GetTempPath()) ("hta-deploy-" + (Get-Date -Format 'yyyyMMdd-HHmmss'))
        New-Item -ItemType Directory -Force -Path $staging | Out-Null
        $newExe = Join-Path $staging 'service.exe'
        Write-Host "[deploy.ps1] building service.exe (CGO_ENABLED=0, same flags as 'make build') into TEMP staging..." -ForegroundColor Cyan
        Push-Location $RepoRoot
        try {
            $env:CGO_ENABLED = '0'
            go build -ldflags '-s -w' -o $newExe ./cmd/service
        } finally {
            Pop-Location
        }
        if ($LASTEXITCODE -ne 0) {
            Write-Host "[deploy.ps1] ERROR: go build failed (exit $LASTEXITCODE)" -ForegroundColor Red
            Write-Host "[deploy.ps1] the old instance is STILL RUNNING and the install dir is UNTOUCHED (the build runs before the stop on purpose)" -ForegroundColor Red
            exit 1
        }
    }

    # ---- c. Stop the running instance. hta stop is idempotent ("nothing to
    # stop" exits 0), so a nonzero here is a REAL failure - abort while the
    # install dir is still untouched.
    Write-Host "[deploy.ps1] stopping the running instance (hta.ps1 stop)..." -ForegroundColor Cyan
    $stopCode = Invoke-Hta -SubCommand 'stop'
    if ($stopCode -ne 0) {
        Write-Host "[deploy.ps1] ERROR: hta.ps1 stop failed (exit $stopCode) - aborting; install dir UNTOUCHED" -ForegroundColor Red
        exit 1
    }

    # ---- d. Backup current bin\web\python into versions\prev BEFORE any
    # swap, so prev always holds a COMPLETE old version. Single slot: an
    # existing prev is discarded (idempotent re-runs).
    if (Test-Path -LiteralPath $Prev) {
        Write-Host "[deploy.ps1] discarding stale backup at versions\prev" -ForegroundColor Yellow
        Remove-Item -LiteralPath $Prev -Recurse -Force
    }
    New-Item -ItemType Directory -Force -Path $Versions | Out-Null
    New-Item -ItemType Directory -Force -Path $Prev | Out-Null
    foreach ($dir in @($InstBin, $InstWeb, $InstPython)) {
        Copy-Item -LiteralPath $dir -Destination (Join-Path $Prev (Split-Path -Leaf $dir)) -Recurse -Force
    }

    # ---- e. Swap in the new bits (all inside the whitelist).
    Copy-Item -LiteralPath $newExe -Destination $InstBinExe -Force
    if (Test-Path -LiteralPath $InstStatic) {
        Remove-Item -LiteralPath $InstStatic -Recurse -Force   # whole-tree replace, no merge
    }
    Copy-Item -LiteralPath $StaticSrc -Destination $InstStatic -Recurse -Force
    # Top-level *.py files ONLY (no -Recurse): anything that ever nests a
    # subtree under python\ can never be touched by this sweep.
    Get-ChildItem -LiteralPath $InstPython -Filter '*.py' -File -ErrorAction SilentlyContinue |
        Remove-Item -Force
    Copy-Item -Path (Join-Path $PySrc '*.py') -Destination $InstPython -Force

    # ---- f. Start + health gate: hta start's exit code IS the contract.
    Write-Host "[deploy.ps1] starting the new version (hta.ps1 start)..." -ForegroundColor Cyan
    $startCode = Invoke-Hta -SubCommand 'start'
    if ($startCode -ne 0) {
        Write-Host "[deploy.ps1] ERROR: start/health check FAILED (hta.ps1 start exit $startCode) - the new code is in place but the service is not healthy" -ForegroundColor Red
        Write-Host "[deploy.ps1] roll back with: .\scripts\deploy.ps1 rollback -InstallDir $InstallDir" -ForegroundColor Yellow
        Write-Host "[deploy.ps1] service logs: $(Join-Path $InstallDir 'logs')" -ForegroundColor Yellow
        exit 1
    }
    Write-Host "[deploy.ps1] deploy complete - new version is up (entry URLs printed by hta.ps1 above)" -ForegroundColor Green
}

function Invoke-Rollback {
    # ---- Preflight: prev must hold a complete old version, or there is
    # nothing to roll back to.
    $missing = @()
    if (-not (Test-Path -LiteralPath $InstallDir -PathType Container)) { $missing += "install dir not found: $InstallDir" }
    if (-not (Test-Path -LiteralPath $HtaPath -PathType Leaf))         { $missing += "hta.ps1 not found in install dir: $HtaPath" }
    $prevMissing = -not (Test-Path -LiteralPath $Prev -PathType Container)
    if ($prevMissing) {
        Write-Host "[deploy.ps1] ERROR: nothing to roll back to - $Prev not found (has any deploy completed on this install dir?)" -ForegroundColor Red
        exit 1
    }
    foreach ($name in @('bin', 'web', 'python')) {
        if (-not (Test-Path -LiteralPath (Join-Path $Prev $name) -PathType Container)) {
            $missing += "backup incomplete: versions\prev\$name not found"
        }
    }
    if ($missing.Count -gt 0) {
        foreach ($m in $missing) { Write-Host "[deploy.ps1] PREFLIGHT ERROR: $m" -ForegroundColor Red }
        Write-Host "[deploy.ps1] aborting - the install directory is UNTOUCHED" -ForegroundColor Red
        exit 1
    }

    Write-Host "[deploy.ps1] stopping the running instance (hta.ps1 stop)..." -ForegroundColor Cyan
    $stopCode = Invoke-Hta -SubCommand 'stop'
    if ($stopCode -ne 0) {
        Write-Host "[deploy.ps1] ERROR: hta.ps1 stop failed (exit $stopCode) - aborting; install dir UNTOUCHED" -ForegroundColor Red
        exit 1
    }

    # Swap versions\prev back in. prev itself is KEPT, so a rollback can be
    # re-run (e.g. after a failed post-rollback start).
    foreach ($name in @('bin', 'web', 'python')) {
        Remove-Item -LiteralPath (Join-Path $InstallDir $name) -Recurse -Force
        Copy-Item -LiteralPath (Join-Path $Prev $name) -Destination (Join-Path $InstallDir $name) -Recurse -Force
    }

    Write-Host "[deploy.ps1] starting the rolled-back version (hta.ps1 start)..." -ForegroundColor Cyan
    $startCode = Invoke-Hta -SubCommand 'start'
    if ($startCode -ne 0) {
        Write-Host "[deploy.ps1] ERROR: start after rollback FAILED (hta.ps1 start exit $startCode)" -ForegroundColor Red
        Write-Host "[deploy.ps1] check the service logs under $(Join-Path $InstallDir 'logs') - versions\prev is still intact, fix the cause and re-run rollback" -ForegroundColor Yellow
        exit 1
    }
    Write-Host "[deploy.ps1] rollback complete - the previous version is up again" -ForegroundColor Green
}

switch ($Action) {
    'deploy'   { Invoke-Deploy }
    'rollback' { Invoke-Rollback }
    default    { Write-Host "[deploy.ps1] ERROR: unknown action '$Action' (use deploy | rollback)" -ForegroundColor Red; exit 1 }
}
