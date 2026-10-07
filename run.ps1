# run.ps1 — start (or restart) the Home Tutor Agent service with .env loaded.
#
# Self-contained dev launcher. Loads .env (VOLCENGINE_TTS_* etc.) into THIS
# process so the Go service and its Python subprocess inherit them via
# os.Getenv (the Go service does NOT auto-load .env; pkg/infra config.go has
# no godotenv). Stops any existing :8080 listener first, so re-running this
# script is a clean restart.
#
# Phase 8 gap-closure fix (G-08-2): ensures service runs from repository root
# so config.yaml is found and static file serving works correctly.
#
# Usage:
#   .\run.ps1                 # HTTP  :8080  (config.yaml)      — plain dev mode
#   .\run.ps1 -Https          # HTTPS :8443  (config.local.yaml) — secure context,
#                              #   REQUIRED for browser microphone (getUserMedia)
#   Service starts in the BACKGROUND (detached, hidden window). Stop with:
#   .\stop.ps1
#
# -Https serves https://localhost:8443/tutor/ with the mkcert LAN certificate
# (.local-certs/hta-lan.pem). The mkcert root CA must be trusted by the client
# browser (it is on this machine; iPads install it via the 8765 distribution
# page). Health endpoint stays on 127.0.0.1:8081; logs tee to
# logs/server-8443.err.log. HTTP :8080 and HTTPS :8443 can run side by side.
#
# iPad reaches it at: http://<host-LAN-IP>:8080/test-interface.html
# config.yaml ws.allowedOrigins MUST include the host LAN IP (see config.yaml ws: block).

param([switch]$Https)

$ErrorActionPreference = 'Stop'
$root    = Split-Path -Parent $MyInvocation.MyCommand.Definition
$envFile = Join-Path $root '.env'
Set-Location $root

Write-Host "==> Working directory: $(Get-Location)" -ForegroundColor Cyan

# Bootstrap config.yaml from the example on first run (Phase 8 gap fix G-08-2)
if (-not (Test-Path "config.yaml")) {
    if (Test-Path "config.example.yaml") {
        Write-Host "==> config.yaml not found — copying from config.example.yaml" -ForegroundColor Yellow
        Copy-Item "config.example.yaml" "config.yaml"
        Write-Host "==> Created config.yaml. Edit python.interpreter to point at your venv." -ForegroundColor Yellow
    } else {
        Write-Host "ERROR: Neither config.yaml nor config.example.yaml found in $root" -ForegroundColor Red
        exit 1
    }
}

# Verify the static directory exists (fail-fast with a clear message)
$staticDir = "./pkg/web/static"
if (-not (Test-Path $staticDir)) {
    Write-Host "ERROR: Static directory $staticDir not found. Student interface will not load." -ForegroundColor Red
    exit 1
}

# Mode selection: -Https uses config.local.yaml (8443 TLS + 8081 health),
# default uses config.yaml (8080 plain). Logs tee per mode.
$configFile = 'config.yaml';   $port = 8080; $logName = 'server.log'
$entryUrl   = 'http://localhost:8080/test-interface.html'
if ($Https) {
    $configFile = 'config.local.yaml'; $port = 8443; $logName = 'server-8443.err.log'
    $entryUrl   = 'https://localhost:8443/tutor/'
    if (-not (Test-Path $configFile)) {
        Write-Host "ERROR: $configFile not found in $root (gitignored local channel config)" -ForegroundColor Red
        exit 1
    }
    foreach ($f in @('.local-certs/hta-lan.pem', '.local-certs/hta-lan-key.pem')) {
        if (-not (Test-Path $f)) {
            Write-Host "ERROR: TLS $f not found — mkcert cert missing (see 13-CHECKPOINTS 通道段)" -ForegroundColor Red
            exit 1
        }
    }
}

# 1. Load .env into the process environment.
if (Test-Path $envFile) {
    $loaded = 0
    Get-Content $envFile | ForEach-Object {
        $line = $_.Trim()
        if (-not $line -or $line.StartsWith('#')) { return }
        if ($line.StartsWith('export ')) { $line = $line.Substring(7).Trim() }   # tolerate bash-style
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
    Write-Host "[run.ps1] loaded $loaded var(s) from .env" -ForegroundColor Green
} else {
    Write-Host "[run.ps1] WARNING: no .env at $envFile — TTS audio generation will fail" -ForegroundColor Yellow
}

# 2. Stop any existing listener on this mode's port (clean restart). Avoid the $pid automatic var.
$listeners = @(Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue)
if ($listeners.Count -gt 0) {
    foreach ($procId in ($listeners.OwningProcess | Sort-Object -Unique)) {
        try {
            Stop-Process -Id $procId -Force -ErrorAction Stop
            Write-Host "[run.ps1] stopped previous :$port instance (PID $procId)" -ForegroundColor Yellow
        } catch {
            Write-Host "[run.ps1] could not stop PID $procId : $_" -ForegroundColor Yellow
        }
    }
    Start-Sleep -Seconds 2   # let the port release
}

# 3. Build, then start the service DETACHED (background, hidden window).
# Logs go via redirect: stderr -> the mode's log (the service logs JSON to
# stderr per config log.output=stderr), stdout -> companion .out log.
# Truncated at each start so logs reflect only the current instance.
$logDir = Join-Path $root 'logs'
New-Item -ItemType Directory -Force -Path $logDir | Out-Null
$logErr = Join-Path $logDir $logName
$logOut = if ($Https) { Join-Path $logDir 'server-8443.out.log' } else { Join-Path $logDir 'server.out.log' }
"" | Set-Content $logErr -ErrorAction SilentlyContinue
"" | Set-Content $logOut -ErrorAction SilentlyContinue
Write-Host "[run.ps1] building service (incremental)..." -ForegroundColor Cyan
go build -o .\bin\service.exe .\cmd\service
if ($LASTEXITCODE -ne 0) { Write-Host "ERROR: build failed" -ForegroundColor Red; exit 1 }
$p = Start-Process -FilePath (Join-Path $root 'bin\service.exe') `
    -ArgumentList '-config', $configFile `
    -WorkingDirectory $root -WindowStyle Hidden `
    -RedirectStandardError $logErr -RedirectStandardOutput $logOut -PassThru
Write-Host "[run.ps1] service started in background, PID $($p.Id)  (logs: logs\$logName / $(Split-Path -Leaf $logOut))" -ForegroundColor Green
Write-Host "[run.ps1] entry URL: $entryUrl" -ForegroundColor Cyan
if ($Https) { Write-Host "[run.ps1] mic-ready secure context; health: http://127.0.0.1:8081/health" -ForegroundColor Cyan }

# Bounded readiness wait: port listening within ~10s (early exit -> log tail hint).
$ready = $false
foreach ($i in 1..20) {
    if ($p.HasExited) { break }
    if (Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue) { $ready = $true; break }
    Start-Sleep -Milliseconds 500
}
if ($ready) {
    Write-Host "[run.ps1] :$port is listening — ready" -ForegroundColor Green
} else {
    Write-Host "[run.ps1] WARNING: :$port not listening within 10s (exit=$($p.HasExited))" -ForegroundColor Yellow
    Write-Host "[run.ps1] last log lines (logs\$logName):" -ForegroundColor Yellow
    Get-Content $logErr -Tail 5 -ErrorAction SilentlyContinue
}
Write-Host "[run.ps1] stop with: .\stop.ps1" -ForegroundColor Cyan
