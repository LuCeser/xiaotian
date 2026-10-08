# stop.ps1 — stop the Home Tutor Agent service started by run.ps1 (either mode).
#
# Stops every listener on the service ports (8080 plain / 8443 TLS / 8081 health)
# plus any leftover bin\service.exe from THIS repository. Unrelated processes are
# left alone (including the 8765 root-CA server run by .scratch\serve-rootca.ps1
# — stop that separately if needed).

$ErrorActionPreference = 'Continue'
$root = Split-Path -Parent $MyInvocation.MyCommand.Definition
$stopped = 0

foreach ($port in 8080, 8443, 8081) {
    $listeners = @(Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue)
    foreach ($procId in ($listeners.OwningProcess | Sort-Object -Unique)) {
        try {
            Stop-Process -Id $procId -Force -ErrorAction Stop
            Write-Host "[stop.ps1] stopped :$port listener (PID $procId)" -ForegroundColor Yellow
            $stopped++
        } catch {
            Write-Host "[stop.ps1] could not stop PID $procId : $_" -ForegroundColor Yellow
        }
    }
}

# Sweep any leftover service.exe whose executable lives in this repo's bin\.
$binPrefix = Join-Path $root 'bin'
Get-CimInstance Win32_Process -Filter "Name='service.exe'" -ErrorAction SilentlyContinue |
    Where-Object { $_.ExecutablePath -and $_.ExecutablePath.StartsWith($binPrefix, [StringComparison]::OrdinalIgnoreCase) } |
    ForEach-Object {
        try {
            Stop-Process -Id $_.ProcessId -Force -ErrorAction Stop
            Write-Host "[stop.ps1] stopped stray service.exe (PID $($_.ProcessId))" -ForegroundColor Yellow
            $stopped++
        } catch {}
    }

if ($stopped -eq 0) {
    Write-Host "[stop.ps1] nothing to stop (no service running)" -ForegroundColor Green
} else {
    Start-Sleep -Seconds 1
    Write-Host "[stop.ps1] done ($stopped process(es) stopped)" -ForegroundColor Green
}
