# 13-07 Task 2 — serve the PUBLIC mkcert root CA over plain HTTP :8765 so the
# iPad can download it in Safari (standard mkcert-on-iOS flow). The directory
# served contains ONLY the public root cert copies (rootCA.pem / rootCA.crt);
# the CA private key stays in the Windows user profile and is NEVER served.
# Scratch script — NOT committed.
#
# FIREWALL NOTE: uses the BASE python 3.12 install, not the project venv —
# Windows Firewall already has enabled Public-profile inbound allow rules for
# exactly this exe path (program-scoped, any port), so the WLAN (Public
# profile) iPad can reach :8765 with no new rule. The venv python has NO such
# rule and would be silently firewalled.
$ErrorActionPreference = 'Stop'
$root = 'D:\workspace\ai\xiaotian'
Set-Location $root

$listeners = @(Get-NetTCPConnection -LocalPort 8765 -State Listen -ErrorAction SilentlyContinue)
foreach ($procId in ($listeners.OwningProcess | Sort-Object -Unique)) {
    try { Stop-Process -Id $procId -Force -ErrorAction Stop } catch {}
    Start-Sleep -Seconds 1
}

$py = "$env:LOCALAPPDATA\programs\python\python312\python.exe"
if (-not (Test-Path $py)) { throw "base python312 not found at $py" }
$logs = Join-Path $root 'logs'
New-Item -ItemType Directory -Force -Path $logs | Out-Null
$p = Start-Process -FilePath $py `
    -ArgumentList '-m', 'http.server', '8765', '--bind', '0.0.0.0', '--directory', "$root\.local-certs\ipad" `
    -WorkingDirectory $root -WindowStyle Hidden `
    -RedirectStandardError (Join-Path $logs 'rootca-server.err.log') `
    -RedirectStandardOutput (Join-Path $logs 'rootca-server.out.log') -PassThru
Write-Host "rootCA http server started, PID $($p.Id) — http://<LAN-IP>:8765/rootCA.pem"
