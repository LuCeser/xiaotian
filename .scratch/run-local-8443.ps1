# 13-07 Task 2 — local HTTPS pre-acceptance launcher (scratch, NOT committed).
# Loads .env into the child process env (same parser as run.ps1), then starts
# bin/service.exe with config.local.yaml (HTTPS on 0.0.0.0:8443, health 8081).
# Re-running it is a clean restart (stops the previous :8443 listener first).
$ErrorActionPreference = 'Stop'
$root = 'D:\workspace\ai\xiaotian'
Set-Location $root

$envFile = Join-Path $root '.env'
if (Test-Path $envFile) {
    Get-Content $envFile | ForEach-Object {
        $line = $_.Trim()
        if (-not $line -or $line.StartsWith('#')) { return }
        if ($line.StartsWith('export ')) { $line = $line.Substring(7).Trim() }
        $idx = $line.IndexOf('=')
        if ($idx -le 0) { return }
        $key = $line.Substring(0, $idx).Trim()
        $val = $line.Substring($idx + 1).Trim()
        if (($val.StartsWith('"') -and $val.EndsWith('"')) -or
            ($val.StartsWith("'") -and $val.EndsWith("'"))) {
            $val = $val.Substring(1, $val.Length - 2)
        }
        [Environment]::SetEnvironmentVariable($key, $val, 'Process')
    }
} else {
    Write-Warning "no .env found — MIMO_API_KEY will be unset (voice tutor degraded)"
}

$listeners = @(Get-NetTCPConnection -LocalPort 8443 -State Listen -ErrorAction SilentlyContinue)
foreach ($procId in ($listeners.OwningProcess | Sort-Object -Unique)) {
    try { Stop-Process -Id $procId -Force -ErrorAction Stop; Write-Host "stopped previous :8443 instance (PID $procId)" } catch {}
    Start-Sleep -Seconds 1
}

$p = Start-Process -FilePath (Join-Path $root 'bin\service.exe') `
    -ArgumentList '-config', 'config.local.yaml' `
    -WorkingDirectory $root -WindowStyle Hidden `
    -RedirectStandardError (Join-Path $root 'server-8443.err.log') `
    -RedirectStandardOutput (Join-Path $root 'server-8443.out.log') -PassThru
Write-Host "service started, PID $($p.Id), logs: server-8443.err.log / server-8443.out.log"
