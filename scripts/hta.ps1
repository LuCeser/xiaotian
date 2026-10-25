# hta.ps1 - daily start/stop/status control for a DEPLOYED Home Tutor Agent
# install directory (the layout created by scripts\install.ps1: bin\service.exe,
# config\config.yaml, secrets\.env, logs\).
#
# scripts\install.ps1 copies this file into the install directory. Run it FROM
# the install directory - every path below is anchored to the directory this
# script itself lives in (same pattern run.ps1 / stop.ps1 use for the dev repo).
#
# Usage:
#   .\hta.ps1 start    # inject secrets\.env (fallback .env) into this process,
#                      #   launch bin\service.exe -config config\config.yaml
#                      #   DETACHED (hidden window, stdout/stderr to log files),
#                      #   wait until THIS pid owns the health listener (max
#                      #   10s), then print the iPad entry URL. Exit 0 = ready.
#   .\hta.ps1 stop     # stop listeners on the two configured ports plus stray
#                      #   service.exe under this install's bin\. Foreign port
#                      #   owners are NEVER killed (guard G1). Idempotent.
#   .\hta.ps1 status   # GET /health probe + listener/process report. Exit 0 =
#                      #   healthy, exit 1 = not (machine-readable contract
#                      #   reused by scripts\deploy.ps1's post-start health gate).
#
# Ports are NEVER hardcoded: they are parsed from config\config.yaml in this
# directory (server.addr / server.healthAddr) on every run.
#
# Windows PowerShell 5.1 compatible: no pwsh-only syntax, no YAML module
# dependency, English-only output (5.1 codepage safety).

param([Parameter(Position = 0)][ValidateSet('start', 'stop', 'status')][string]$Command = 'status')

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $MyInvocation.MyCommand.Definition

# ---------------------------------------------------------------------------
# Config parsing: resolve server.addr / server.healthAddr from the install
# directory's config\config.yaml. The file is treated as untrusted text:
# line-based regex parse + top-level block tracking + strict port validation;
# the file's content is never evaluated as code.
# ---------------------------------------------------------------------------

function Convert-HtaHostPort {
    param([string]$RawValue, [string]$Label)
    $v = $RawValue.Trim()
    if ((($v.StartsWith('"') -and $v.EndsWith('"')) -or
         ($v.StartsWith("'") -and $v.EndsWith("'"))) -and $v.Length -ge 2) {
        $v = $v.Substring(1, $v.Length - 2).Trim()
    }
    $sep = $v.LastIndexOf(':')
    if ($sep -lt 0) {
        Write-Host "[hta.ps1] ERROR: cannot parse $Label value '$RawValue' as host:port" -ForegroundColor Red
        exit 1
    }
    $hostPart = ''
    if ($sep -gt 0) { $hostPart = $v.Substring(0, $sep).Trim() }
    $portPart = $v.Substring($sep + 1).Trim()
    $port = 0
    if (-not [int]::TryParse($portPart, [ref]$port) -or $port -lt 1 -or $port -gt 65535) {
        Write-Host "[hta.ps1] ERROR: $Label port value '$portPart' is not a valid port (expected 1-65535)" -ForegroundColor Red
        exit 1
    }
    return [pscustomobject]@{ Host = $hostPart; Port = $port }
}

function Read-HtaServerConfig {
    $configPath = Join-Path $root 'config\config.yaml'
    if (-not (Test-Path $configPath)) {
        Write-Host "[hta.ps1] ERROR: $configPath not found - run scripts\install.ps1 first (it creates the install layout)" -ForegroundColor Red
        exit 1
    }
    $publicPort = $null
    $healthHost = '127.0.0.1'
    $healthPort = $null
    $block = ''
    foreach ($line in (Get-Content $configPath)) {
        if ($line -match '^([A-Za-z_]\w*)\s*:\s*$') {
            # Top-level block header (column 0). Indented keys never match.
            $block = $Matches[1].ToLowerInvariant()
            continue
        }
        if ($block -ne 'server') { continue }
        $t = $line.Trim()
        if (-not $t -or $t.StartsWith('#')) { continue }
        if ($t -match '^addr\s*:\s*(.+?)\s*$') {
            $publicPort = (Convert-HtaHostPort -RawValue $Matches[1] -Label 'server.addr').Port
        }
        elseif ($t -match '^healthAddr\s*:\s*(.+?)\s*$') {
            $hp = Convert-HtaHostPort -RawValue $Matches[1] -Label 'server.healthAddr'
            if ($hp.Host) { $healthHost = $hp.Host }
            $healthPort = $hp.Port
        }
    }
    if (-not $publicPort -or -not $healthPort) {
        Write-Host "[hta.ps1] ERROR: could not resolve server.addr / server.healthAddr in $configPath" -ForegroundColor Red
        exit 1
    }
    return [pscustomobject]@{
        PublicPort = [int]$publicPort
        HealthHost = $healthHost
        HealthPort = [int]$healthPort
    }
}

# ---------------------------------------------------------------------------
# Environment: install the service's secrets into THIS process so service.exe
# and its Python subprocesses inherit them via os.Getenv. Secrets are counted,
# never echoed (they must not leak into logs or console output).
# ---------------------------------------------------------------------------

function Import-HtaEnv {
    $envFile = $null
    foreach ($candidate in @((Join-Path $root 'secrets\.env'), (Join-Path $root '.env'))) {
        if (Test-Path $candidate) { $envFile = $candidate; break }
    }
    if (-not $envFile) {
        Write-Host "[hta.ps1] WARNING: no secrets\.env or .env in $root - TTS audio generation will fail" -ForegroundColor Yellow
        return
    }
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
    Write-Host "[hta.ps1] loaded $loaded var(s) from $envFile" -ForegroundColor Green
}

# ---------------------------------------------------------------------------
# Port/process helpers shared by start cleanup, stop and status.
# ---------------------------------------------------------------------------

function Get-HtaPortOwnerPids {
    param([int[]]$Ports)
    $owners = @()
    foreach ($port in $Ports) {
        $listeners = @(Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue)
        foreach ($procId in (@($listeners | ForEach-Object { $_.OwningProcess }) | Sort-Object -Unique)) {
            if ($owners -notcontains $procId) { $owners += $procId }
        }
    }
    # Plain array (NO ,@() wrapper): callers collect with @(...) - a comma-wrapped
    # return survives @(fn) collection as a nested array and leaks Object[] into
    # -Id integer parameters (and makes .Count report 1 even when empty).
    return @($owners)
}

function Get-HtaProcessLabel {
    param([int]$ProcId)
    $proc = Get-Process -Id $ProcId -ErrorAction SilentlyContinue
    if (-not $proc) { return "PID $ProcId (already exited)" }
    return "PID $ProcId ($($proc.Name))"
}

# Guard G1 (threat T-quick-njo-01): a PID may be killed only if it is OUR
# service - process name is service.exe (Get-Process Name drops the extension)
# OR its executable path lives under this install's bin\. Anything else is a
# foreign owner and must never be stopped.
function Test-HtaOursToKill {
    param([int]$TargetPid)
    $proc = Get-Process -Id $TargetPid -ErrorAction SilentlyContinue
    if (-not $proc) { return $false }
    if ($proc.Name -ieq 'service' -or $proc.Name -ieq 'service.exe') { return $true }
    $exePath = $null
    try { $exePath = $proc.Path } catch { $exePath = $null }
    if ($exePath) {
        $binPrefix = Join-Path $root 'bin'
        if ($exePath.StartsWith($binPrefix, [StringComparison]::OrdinalIgnoreCase)) { return $true }
    }
    return $false
}

# First listener owner on the port that passes the G1 identity check (our
# service.exe), or $null when none. Used by start's readiness gate.
function Get-HtaGuardedPortOwner {
    param([int]$Port)
    $listeners = @(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue)
    foreach ($conn in $listeners) {
        $ownerPid = [int]$conn.OwningProcess
        if ($ownerPid -gt 0 -and (Test-HtaOursToKill -TargetPid $ownerPid)) { return $ownerPid }
    }
    return $null
}

function Get-HtaLanIPv4 {
    $addrs = @(Get-NetIPAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue |
        Where-Object { $_.IPAddress -notlike '127.*' -and $_.IPAddress -notlike '169.254.*' } |
        ForEach-Object { $_.IPAddress })
    if ($addrs.Count -gt 0) { return $addrs[0] }
    return $null
}

# ---------------------------------------------------------------------------
# start: preflight -> env -> clear our ports (guarded) -> truncate logs ->
# detached launch -> own-the-listener readiness gate -> entry URLs.
# ---------------------------------------------------------------------------

function Invoke-HtaStart {
    $cfg = Read-HtaServerConfig
    $exe = Join-Path $root 'bin\service.exe'
    if (-not (Test-Path $exe)) {
        Write-Host "[hta.ps1] ERROR: $exe not found - run scripts\deploy.ps1 first (it builds and stages bin\service.exe)" -ForegroundColor Red
        exit 1
    }
    $logDir = Join-Path $root 'logs'
    New-Item -ItemType Directory -Force -Path $logDir | Out-Null

    Import-HtaEnv

    # Clear previous listeners on our two ports (clean restart), under guard G1.
    # Snapshot BOTH ports once and dedupe: one service PID usually owns both
    # listeners, and per-port kill loops re-visit the just-killed PID's stale
    # TCP-table row on the second port (a dead PID is nothing-to-do, never a
    # foreign owner).
    $killed = 0
    foreach ($procId in (Get-HtaPortOwnerPids -Ports @([int]$cfg.PublicPort, [int]$cfg.HealthPort))) {
        $proc = Get-Process -Id $procId -ErrorAction SilentlyContinue
        if (-not $proc) { continue }   # stale row of an already-dead owner
        if (Test-HtaOursToKill -TargetPid $procId) {
            try {
                Stop-Process -Id $procId -Force -ErrorAction Stop
                Write-Host "[hta.ps1] stopped previous service process (PID $procId)" -ForegroundColor Yellow
                $killed++
            } catch {
                Write-Host "[hta.ps1] could not stop PID $procId : $_" -ForegroundColor Yellow
            }
        } else {
            Write-Host "[hta.ps1] ERROR: service port is held by foreign process $(Get-HtaProcessLabel -ProcId $procId) - refusing to kill it; stop that process or pick different deployment ports" -ForegroundColor Red
            exit 1
        }
    }
    if ($killed -gt 0) { Start-Sleep -Seconds 2 }   # let the ports release

    $logOut = Join-Path $logDir 'server.out.log'
    $logErr = Join-Path $logDir 'server.err.log'
    "" | Set-Content $logOut -ErrorAction SilentlyContinue
    "" | Set-Content $logErr -ErrorAction SilentlyContinue

    # Detached launch. PS 5.1's Start-Process redirection leaks the caller's
    # inherited stdio PIPE handles into the launched tree (run.ps1's
    # scripts\start-detached.mjs exists for exactly this reason) - any caller
    # that captures this script's output then waits on pipe EOF forever.
    # Win32_Process.Create instead spawns through the WMI provider
    # (WmiPrvSE.exe), so NOTHING from this process's handle chain reaches the
    # service. A cmd /c wrapper performs the dual file redirection; its paths
    # are relative to CurrentDirectory (= install dir), so no quoting is
    # needed and the log contract is unchanged: logs\server.out.log +
    # logs\server.err.log, cleared above and re-created by the redirect.
    $cmdLine = "`"$env:ComSpec`" /d /c bin\service.exe -config config\config.yaml 1>logs\server.out.log 2>logs\server.err.log"
    $startup = ([wmiclass]'Win32_ProcessStartup').CreateInstance()
    $startup.ShowWindow = 0   # SW_HIDE - no console window flash on launch
    $launch = Invoke-WmiMethod -Class Win32_Process -Name Create -ArgumentList $cmdLine, $root, $startup
    if ([int]$launch.ReturnValue -ne 0) {
        Write-Host "[hta.ps1] ERROR: detached launch failed (Win32_Process.Create returned $($launch.ReturnValue))" -ForegroundColor Red
        exit 1
    }
    $wrapperPid = [int]$launch.ProcessId
    Write-Host "[hta.ps1] service launching in background (wrapper PID $wrapperPid; logs: logs\server.out.log / logs\server.err.log)" -ForegroundColor Cyan

    # Bounded readiness wait: a listener on the health port whose owner passes
    # the G1 identity check (our bin\service.exe) must appear within ~10s.
    # Both service ports were cleared above, so the first guard-passing owner
    # IS this launch. The cmd wrapper exits only after service.exe exits, so
    # a dead wrapper with no listener means the launch already failed.
    $servicePid = $null
    $ready = $false
    foreach ($i in 1..20) {
        $ownerPid = Get-HtaGuardedPortOwner -Port $cfg.HealthPort
        if ($ownerPid) { $servicePid = $ownerPid; $ready = $true; break }
        if (-not (Get-Process -Id $wrapperPid -ErrorAction SilentlyContinue)) { break }
        Start-Sleep -Milliseconds 500
    }
    if (-not $ready) {
        $wrapperAlive = [bool](Get-Process -Id $wrapperPid -ErrorAction SilentlyContinue)
        Write-Host "[hta.ps1] ERROR: no bin\service.exe owned health port $($cfg.HealthPort) within 10s (wrapper alive=$wrapperAlive)" -ForegroundColor Red
        Write-Host "[hta.ps1] last log lines (logs\server.err.log):" -ForegroundColor Red
        Get-Content $logErr -Tail 5 -ErrorAction SilentlyContinue
        # Kill the failed launch: stray service.exe under this install's bin\
        # plus the cmd wrapper if it is still lingering.
        $binPrefix = Join-Path $root 'bin'
        Get-CimInstance Win32_Process -Filter "Name='service.exe'" -ErrorAction SilentlyContinue |
            Where-Object { $_.ExecutablePath -and $_.ExecutablePath.StartsWith($binPrefix, [StringComparison]::OrdinalIgnoreCase) } |
            ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
        if ($wrapperAlive) {
            Stop-Process -Id $wrapperPid -Force -ErrorAction SilentlyContinue
        }
        exit 1
    }

    Write-Host "[hta.ps1] :$($cfg.HealthPort) is listening on PID $servicePid - ready" -ForegroundColor Green
    Write-Host "[hta.ps1] health probe: http://$($cfg.HealthHost):$($cfg.HealthPort)/health" -ForegroundColor Cyan
    $lanIp = Get-HtaLanIPv4
    if ($lanIp) {
        Write-Host "[hta.ps1] iPad entry:   https://${lanIp}:$($cfg.PublicPort)/tutor/" -ForegroundColor Green
        Write-Host "[hta.ps1] parent entry: https://${lanIp}:$($cfg.PublicPort)/parent.html" -ForegroundColor Green
    } else {
        Write-Host "[hta.ps1] WARNING: no LAN IPv4 address found on this machine - substitute the real LAN IP below" -ForegroundColor Yellow
        Write-Host "[hta.ps1] iPad entry:   https://<LAN-IP>:$($cfg.PublicPort)/tutor/" -ForegroundColor Green
    }
    Write-Host "[hta.ps1] stop with: .\hta.ps1 stop" -ForegroundColor Cyan
    exit 0
}

# ---------------------------------------------------------------------------
# stop: guarded per-port kills + bin-prefix stray sweep + idempotent verdict.
# ---------------------------------------------------------------------------

function Invoke-HtaStop {
    $cfg = Read-HtaServerConfig
    $ports = @([int]$cfg.PublicPort, [int]$cfg.HealthPort)
    $killed = 0

    # Snapshot the owners of BOTH ports once and dedupe to distinct integers:
    # the same service PID usually owns both listeners, and killing it on the
    # first port leaves a stale listener row for the second query - a per-port
    # loop would re-visit the dead PID and misclassify it as a foreign owner.
    foreach ($procId in (Get-HtaPortOwnerPids -Ports $ports)) {
        $proc = Get-Process -Id $procId -ErrorAction SilentlyContinue
        if (-not $proc) {
            # Stale TCP-table row of an already-dead process: the port is
            # effectively released - idempotent success, NOT a refusal.
            continue
        }
        if (Test-HtaOursToKill -TargetPid $procId) {
            try {
                Stop-Process -Id $procId -Force -ErrorAction Stop
                Write-Host "[hta.ps1] stopped service process (PID $procId)" -ForegroundColor Yellow
                $killed++
            } catch {
                Write-Host "[hta.ps1] could not stop PID $procId : $_" -ForegroundColor Yellow
            }
        } else {
            Write-Host "[hta.ps1] refusing to kill foreign process - $(Get-HtaProcessLabel -ProcId $procId)" -ForegroundColor Yellow
        }
    }

    # Sweep stray service.exe instances whose executable lives in THIS install's bin\.
    $binPrefix = Join-Path $root 'bin'
    Get-CimInstance Win32_Process -Filter "Name='service.exe'" -ErrorAction SilentlyContinue |
        Where-Object { $_.ExecutablePath -and $_.ExecutablePath.StartsWith($binPrefix, [StringComparison]::OrdinalIgnoreCase) } |
        ForEach-Object {
            try {
                Stop-Process -Id $_.ProcessId -Force -ErrorAction Stop
                Write-Host "[hta.ps1] stopped stray service.exe (PID $($_.ProcessId))" -ForegroundColor Yellow
                $killed++
            } catch {}
        }

    if ($killed -gt 0) { Start-Sleep -Seconds 1 }

    # Idempotent verdict: success iff no ALIVE owner remains on either port.
    # The TCP table can keep a listener row around briefly after the owning
    # process dies - such dead-PID rows do not count as "still held".
    $remaining = @()
    foreach ($procId in (Get-HtaPortOwnerPids -Ports $ports)) {
        if (Get-Process -Id $procId -ErrorAction SilentlyContinue) { $remaining += $procId }
    }
    if ($remaining.Count -eq 0) {
        if ($killed -eq 0) {
            Write-Host "[hta.ps1] nothing to stop (no service running)" -ForegroundColor Green
        } else {
            Write-Host "[hta.ps1] done ($killed process(es) stopped); ports :$($cfg.PublicPort)/:$($cfg.HealthPort) released" -ForegroundColor Green
        }
        exit 0
    }
    foreach ($procId in $remaining) {
        Write-Host "[hta.ps1] ERROR: port still held after stop - $(Get-HtaProcessLabel -ProcId $procId)" -ForegroundColor Red
    }
    Write-Host "[hta.ps1] ERROR: stop incomplete (typically a foreign owner the guard refused to kill)" -ForegroundColor Red
    exit 1
}

# ---------------------------------------------------------------------------
# status: /health probe + listener/process report. Exit code contract:
# 0 = healthy (probe 200), 1 = not healthy. Consumed by scripts\deploy.ps1.
# ---------------------------------------------------------------------------

function Invoke-HtaStatus {
    $cfg = Read-HtaServerConfig
    $lanIp = Get-HtaLanIPv4
    if ($lanIp) {
        Write-Host "[hta.ps1] public port :$($cfg.PublicPort) | entry: https://${lanIp}:$($cfg.PublicPort)/tutor/" -ForegroundColor Cyan
    } else {
        Write-Host "[hta.ps1] public port :$($cfg.PublicPort) | entry: https://<LAN-IP>:$($cfg.PublicPort)/tutor/" -ForegroundColor Cyan
    }

    $healthy = $false
    $healthUrl = "http://$($cfg.HealthHost):$($cfg.HealthPort)/health"
    try {
        $resp = Invoke-WebRequest -Uri $healthUrl -UseBasicParsing -TimeoutSec 3
        if ($resp.StatusCode -eq 200) {
            $healthy = $true
            Write-Host "[hta.ps1] health probe $healthUrl -> 200 OK" -ForegroundColor Green
        } else {
            Write-Host "[hta.ps1] health probe $healthUrl -> HTTP $($resp.StatusCode)" -ForegroundColor Red
        }
    } catch {
        Write-Host "[hta.ps1] health probe $healthUrl -> FAILED ($($_.Exception.Message))" -ForegroundColor Red
    }

    foreach ($port in @($cfg.PublicPort, $cfg.HealthPort)) {
        $listeners = @(Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue)
        if ($listeners.Count -eq 0) {
            Write-Host "[hta.ps1] :$port - no listener" -ForegroundColor Yellow
            continue
        }
        foreach ($procId in (@($listeners | ForEach-Object { $_.OwningProcess }) | Sort-Object -Unique)) {
            $proc = Get-Process -Id $procId -ErrorAction SilentlyContinue
            $pname = '<exited>'
            if ($proc) { $pname = $proc.Name }
            Write-Host "[hta.ps1] :$port - listener PID $procId ($pname)" -ForegroundColor Cyan
        }
    }

    $binPrefix = Join-Path $root 'bin'
    $strays = @(Get-CimInstance Win32_Process -Filter "Name='service.exe'" -ErrorAction SilentlyContinue |
        Where-Object { $_.ExecutablePath -and $_.ExecutablePath.StartsWith($binPrefix, [StringComparison]::OrdinalIgnoreCase) })
    foreach ($s in $strays) {
        Write-Host "[hta.ps1] service.exe under bin\ : PID $($s.ProcessId) ($($s.ExecutablePath))" -ForegroundColor Cyan
    }

    if ($healthy) {
        Write-Host "STATUS: healthy" -ForegroundColor Green
        exit 0
    }
    Write-Host "STATUS: unhealthy" -ForegroundColor Red
    exit 1
}

# ---------------------------------------------------------------------------
# Dispatch (ValidateSet already restricts Command; default 'status' is the
# read-only safe choice).
# ---------------------------------------------------------------------------

switch ($Command) {
    'start'  { Invoke-HtaStart }
    'stop'   { Invoke-HtaStop }
    'status' { Invoke-HtaStatus }
    default  { Write-Host "[hta.ps1] ERROR: unknown command '$Command' (use start | stop | status)" -ForegroundColor Red; exit 1 }
}
