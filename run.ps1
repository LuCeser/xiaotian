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
#   .\run.ps1                 # stop old :8080 (if any), load .env, start service
#   (Ctrl+C in the window to stop)
#
# iPad reaches it at: http://<host-LAN-IP>:8080/test-interface.html
# config.yaml ws.allowedOrigins MUST include the host LAN IP (see config.yaml ws: block).

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

# 2. Stop any existing :8080 listener (clean restart). Avoid the $pid automatic var.
$listeners = @(Get-NetTCPConnection -LocalPort 8080 -State Listen -ErrorAction SilentlyContinue)
if ($listeners.Count -gt 0) {
    foreach ($procId in ($listeners.OwningProcess | Sort-Object -Unique)) {
        try {
            Stop-Process -Id $procId -Force -ErrorAction Stop
            Write-Host "[run.ps1] stopped previous :8080 instance (PID $procId)" -ForegroundColor Yellow
        } catch {
            Write-Host "[run.ps1] could not stop PID $procId : $_" -ForegroundColor Yellow
        }
    }
    Start-Sleep -Seconds 2   # let the port release
}

# 3. Start the service (foreground in this window; Ctrl+C to stop).
# Tee stderr+stdout to server.log so the orchestrator can read diagnostics
# (the service logs JSON to stderr per config log.output=stderr). Truncated
# at each start so server.log reflects only the current instance.
$logPath = Join-Path $root 'server.log'
"" | Set-Content $logPath -ErrorAction SilentlyContinue
Write-Host "[run.ps1] building service (incremental)..." -ForegroundColor Cyan
go build -o .\bin\service.exe .\cmd\service
if ($LASTEXITCODE -ne 0) { Write-Host "ERROR: build failed" -ForegroundColor Red; exit 1 }
Write-Host "[run.ps1] starting: .\bin\service.exe -config config.yaml  (tee -> server.log)" -ForegroundColor Cyan
.\bin\service.exe -config config.yaml 2>&1 | Tee-Object -FilePath $logPath
