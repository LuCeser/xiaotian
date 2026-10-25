# install.ps1 - one-shot installer: turn this Windows machine into the
# always-on home deployment host (quick task 261024-njn).
#
# Copies everything the service needs from a repo checkout (-SourceDir,
# default: the directory above this script) into an install directory
# (-InstallDir, default D:\hta):
#   bin\service.exe, web\static, vendored Python scripts, a production
#   config\config.yaml TRANSFORMED from config.example.yaml (absolute paths,
#   custom data dir, TLS block pointing at the copied mkcert cert, LAN IP in
#   ws/rt allowedOrigins, ports avoiding the dev-reserved 8080/8443/8081),
#   certs\, a uv-built venv (Windows layout, Tsinghua index), a netsh
#   firewall allow rule for the public port (degrades to printed instructions
#   without admin), scripts\hta.ps1, and INSTALL-NOTES.md (cert expiry date).
#
# IDEMPOTENT AND DELETION-FREE: never overwrites secrets\.env,
# config\config.yaml, certs\*, or data\ contents; contains no Remove-Item
# at all. Re-running is always safe.
#
# Usage:
#   .\scripts\install.ps1                          # defaults: D:\hta, ports 9443/9444
#   .\scripts\install.ps1 -InstallDir D:\hta -PublicPort 9443 -HealthPort 9444
#   .\scripts\install.ps1 -SkipFirewall            # test/CI: skip netsh entirely
#
# The companion files live in the same batch:
#   scripts\hta.ps1  (daily start/stop; batch item 261024-njo)
#   scripts\deploy.ps1 (upgrade/rollback; batch item 261024-njp)

param(
    [string]$InstallDir = 'D:\hta',
    [string]$SourceDir = '',
    [string]$DataDir = '',
    [int]$PublicPort = 9443,
    [int]$HealthPort = 9444,
    [string]$LanIp = '',
    [string]$PyIndex = 'https://pypi.tuna.tsinghua.edu.cn/simple',
    [switch]$SkipFirewall
)

$ErrorActionPreference = 'Stop'

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

function Fail([string]$Message) {
    Write-Host "ERROR: $Message" -ForegroundColor Red
    exit 1
}

# Dev-reserved ports (run.ps1 modes): 8080 plain HTTP, 8443 HTTPS, 8081 health.
$script:DevReservedPorts = @(8080, 8443, 8081)
$script:FirewallRuleName = "HTA Deploy $PublicPort"
$script:FirewallCommand = 'netsh advfirewall firewall add rule name="HTA Deploy {0}" dir=in action=allow protocol=TCP localport={0}' -f $PublicPort

# Quote a path as a YAML single-quoted scalar (backslashes stay literal).
function ConvertTo-YamlSingleQuoted([string]$Path) {
    return "'" + $Path.Replace("'", "''") + "'"
}

# Resolve dirs that default off other params (param defaults cannot reference
# siblings in PS 5.1).
if (-not $SourceDir) {
    if ($PSScriptRoot) { $SourceDir = Split-Path -Parent $PSScriptRoot } else { $SourceDir = (Get-Location).Path }
}
if (-not $DataDir) { $DataDir = Join-Path $InstallDir 'data' }

# [System.Net.IPAddress]::TryParse + IPv4-only + loopback rejection.
function Test-ValidLanIp([string]$Value) {
    $parsed = $null
    if (-not [System.Net.IPAddress]::TryParse($Value, [ref]$parsed)) { return $false }
    if ($parsed.AddressFamily -ne [System.Net.Sockets.AddressFamily]::InterNetwork) { return $false }
    if ($parsed.Equals([System.Net.IPAddress]::Loopback)) { return $false }
    return $true
}

function Get-AutoLanIp {
    # Prefer a PHYSICAL network adapter: virtual NICs (WSL / Hyper-V / Tailscale /
    # OpenVPN / Bluetooth) report Virtual=$true and their addresses are
    # unreachable from iPads on the LAN. Pick physical adapters that are Up,
    # lowest InterfaceMetric first (the default-route interface), then fall back
    # to any Up adapter, and return the first non-loopback / non-APIPA IPv4.
    $adapters = @(Get-NetAdapter -ErrorAction SilentlyContinue)
    $ordered = @($adapters | Where-Object { -not $_.Virtual -and $_.Status -eq 'Up' } | Sort-Object InterfaceMetric)
    if ($ordered.Count -eq 0) {
        $ordered = @($adapters | Where-Object { $_.Status -eq 'Up' } | Sort-Object InterfaceMetric)
    }
    foreach ($a in $ordered) {
        $ip = Get-NetIPAddress -AddressFamily IPv4 -InterfaceIndex $a.InterfaceIndex -ErrorAction SilentlyContinue |
            Where-Object { $_.IPAddress -notlike '127.*' -and $_.IPAddress -notlike '169.254.*' } |
            Select-Object -First 1
        if ($ip) { return $ip.IPAddress }
    }
    return ''
}

function Test-IsAdmin {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($identity)
    return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

# Extract the leaf certificate's NotAfter (yyyy-MM-dd) from a PEM file.
# PS 5.1's .NET cannot parse PEM text but can take DER bytes: collect the
# base64 between the BEGIN/END markers, FromBase64String to byte[], build an
# X509Certificate2 with the @(,) array guard against flattening. Any failure
# throws; the caller degrades to an explicit "unknown" note (warning, never
# an install failure) - planner-verified on PS 5.1.26100 with a real mkcert
# cert (NotAfter 2029-01-03).
function Get-CertNotAfterDate([string]$PemPath) {
    $inBlock = $false
    $b64Lines = @()
    foreach ($line in (Get-Content -Path $PemPath)) {
        if ($line -like '-----BEGIN CERTIFICATE-----*') { $inBlock = $true; continue }
        if ($line -like '-----END CERTIFICATE-----*') { break }
        if ($inBlock) { $b64Lines += $line.Trim() }
    }
    if ($b64Lines.Count -eq 0) { throw "no PEM CERTIFICATE block found in $PemPath" }
    $der = [Convert]::FromBase64String(($b64Lines -join ''))
    $cert = New-Object System.Security.Cryptography.X509Certificates.X509Certificate2 -ArgumentList @(, $der)
    if ($null -eq $cert) { throw "could not build X509Certificate2 from $PemPath" }
    return $cert.NotAfter.ToString('yyyy-MM-dd')
}

# Transform config.example.yaml into the production config, one exact
# full-line match per transform point (no substring, no regex). Each pattern
# must hit EXACTLY once; any miss/double-hit is a hard error listing every
# unmatched transform point, so a future config.example.yaml reword fails the
# install loudly instead of silently generating a miswired config.
function ConvertTo-ProductionConfig {
    param(
        [string]$ExamplePath,
        [string]$InstallDirPath,
        [string]$DataDirPath,
        [int]$PubPort,
        [int]$HthPort,
        [string]$Lip
    )

    $qInstall = ConvertTo-YamlSingleQuoted $InstallDirPath
    $qData = ConvertTo-YamlSingleQuoted $DataDirPath
    $originsLine = "  allowedOrigins: [localhost, 127.0.0.1, $Lip]"

    # Array (not hashtable) to keep a deterministic order and per-item counts.
    $patterns = @(
        @{ Id = 'server.addr';        Match = '  addr: "0.0.0.0:8080"';                Replace = @('  addr: "0.0.0.0:' + $PubPort + '"') },
        @{ Id = 'server.healthAddr';  Match = '  healthAddr: "127.0.0.1:8081"';        Replace = @('  healthAddr: "127.0.0.1:' + $HthPort + '"') },
        @{ Id = 'server.staticDir';   Match = '  staticDir: ./pkg/web/static';         Replace = @('  staticDir: ' + (ConvertTo-YamlSingleQuoted (Join-Path $InstallDirPath 'web\static'))) },
        @{ Id = 'server.tls-insert';  Match = '  staticRoute: /';                      Replace = @('  staticRoute: /', '  tls:', "    certFile: $(ConvertTo-YamlSingleQuoted (Join-Path $InstallDirPath 'certs\hta-lan.pem'))", "    keyFile: $(ConvertTo-YamlSingleQuoted (Join-Path $InstallDirPath 'certs\hta-lan-key.pem'))") },
        @{ Id = 'python.interpreter'; Match = '  interpreter: /opt/hta/venv/bin/python'; Replace = @('  interpreter: ' + (ConvertTo-YamlSingleQuoted (Join-Path $InstallDirPath 'venv\Scripts\python.exe'))) },
        @{ Id = 'python.scriptsDir';  Match = '  scriptsDir: third_party/python';      Replace = @('  scriptsDir: ' + (ConvertTo-YamlSingleQuoted (Join-Path $InstallDirPath 'python'))) },
        @{ Id = 'python.vocabDBPath'; Match = '  vocabDBPath: /opt/hta/data/vocab.db'; Replace = @('  vocabDBPath: ' + (ConvertTo-YamlSingleQuoted (Join-Path $DataDirPath 'vocab.db'))) },
        @{ Id = 'python.dataDir';     Match = '  dataDir: /opt/hta/data';              Replace = @('  dataDir: ' + $qData) },
        @{ Id = 'python.cacheDir';    Match = '  cacheDir: /opt/hta/data/cache/audio'; Replace = @('  cacheDir: ' + (ConvertTo-YamlSingleQuoted (Join-Path $DataDirPath 'cache\audio'))) },
        @{ Id = 'python.manifestDir'; Match = '  manifestDir: /opt/hta/data/cache';    Replace = @('  manifestDir: ' + (ConvertTo-YamlSingleQuoted (Join-Path $DataDirPath 'cache'))) },
        @{ Id = 'python.envFile';     Match = '  envFile: /opt/hta/data/dictation.env'; Replace = @('  envFile: ' + (ConvertTo-YamlSingleQuoted (Join-Path $DataDirPath 'dictation.env'))) },
        @{ Id = 'ws.allowedOrigins';  Match = 'ws:';                                   Replace = @('ws:', $originsLine) },
        @{ Id = 'rt.allowedOrigins';  Match = 'rt:';                                   Replace = @('rt:', $originsLine, '  forceStudentMode: true') }
    )

    $hits = @{}
    foreach ($p in $patterns) { $hits[$p.Id] = 0 }

    $out = New-Object System.Collections.Generic.List[string]
    $droppedCommentLines = 0
    foreach ($line in (Get-Content -Path $ExamplePath)) {
        # Pure-comment lines mentioning the old /opt/hta deployment layout
        # are dropped so the generated config carries zero /opt/hta residue
        # (the pre-write self-check demands it). Comment-only: no semantic loss.
        if ($line -match '^\s*#' -and $line.Contains('/opt/hta')) {
            $droppedCommentLines++
            continue
        }
        $matched = $false
        foreach ($p in $patterns) {
            if ($line -ceq $p.Match) {
                $hits[$p.Id] = $hits[$p.Id] + 1
                foreach ($r in $p.Replace) { $out.Add($r) }
                $matched = $true
                break
            }
        }
        if (-not $matched) { $out.Add($line) }
    }

    $misses = @($patterns | Where-Object { $hits[$_.Id] -ne 1 } |
        ForEach-Object { "{0} (line matched {1} time(s), expected exactly 1): expected line [{2}]" -f $_.Id, $hits[$_.Id], $_.Match })
    if ($misses.Count -gt 0) {
        throw (@("config.example.yaml transform failed - $($misses.Count) transform point(s) did not match exactly once (the example file has drifted):") + $misses) -join "`n"
    }
    if ($droppedCommentLines -gt 0) {
        Write-Host "[install] dropped $droppedCommentLines comment line(s) mentioning /opt/hta from the generated config" -ForegroundColor DarkGray
    }

    return $out
}

# Nine-point pre-write self-check on the generated config (plan context B).
function Test-GeneratedConfig([string[]]$Lines, [string]$InstallDirPath, [string]$DataDirPath, [int]$PubPort, [int]$HthPort, [string]$Lip) {
    $text = $Lines -join "`n"
    $venvPy = Join-Path $InstallDirPath 'venv\Scripts\python.exe'
    $checks = @(
        @{ Name = "addr uses public port $PubPort";        Ok = ($text -like ('*"0.0.0.0:' + $PubPort + '"*')) },
        @{ Name = "healthAddr uses health port $HthPort";  Ok = ($text -like ('*"127.0.0.1:' + $HthPort + '"*')) },
        @{ Name = 'staticDir points into the install dir'; Ok = $text.Contains((Join-Path $InstallDirPath 'web\static')) },
        @{ Name = 'tls certFile points into certs\';       Ok = $text.Contains((Join-Path $InstallDirPath 'certs\hta-lan.pem')) },
        @{ Name = 'tls keyFile points into certs\';        Ok = $text.Contains((Join-Path $InstallDirPath 'certs\hta-lan-key.pem')) },
        @{ Name = 'interpreter points at the venv python'; Ok = $text.Contains($venvPy) },
        @{ Name = 'scriptsDir points into python\';        Ok = $text.Contains((Join-Path $InstallDirPath 'python')) },
        @{ Name = 'vocabDBPath points into the data dir';  Ok = $text.Contains((Join-Path $DataDirPath 'vocab.db')) },
        @{ Name = 'dataDir points into the data dir';      Ok = $text.Contains($DataDirPath) },
        @{ Name = 'cacheDir points into the data dir';     Ok = $text.Contains((Join-Path $DataDirPath 'cache\audio')) },
        @{ Name = 'manifestDir points into the data dir';  Ok = $text.Contains((Join-Path $DataDirPath 'cache')) },
        @{ Name = 'envFile points into the data dir';      Ok = $text.Contains((Join-Path $DataDirPath 'dictation.env')) },
        @{ Name = 'both allowedOrigins lines carry the LAN IP'; Ok = ((@($Lines | Where-Object { $_ -like '  allowedOrigins:*' -and $_ -like "*$Lip*" }).Count) -eq 2) },
        @{ Name = 'forceStudentMode is true';              Ok = ($text -like '*forceStudentMode: true*') },
        @{ Name = 'no /opt/hta residue';                   Ok = (-not $text.Contains('/opt/hta')) },
        @{ Name = 'no dev addr 0.0.0.0:8080 residue';      Ok = (-not $text.Contains('"0.0.0.0:8080"')) },
        @{ Name = 'no dev healthAddr 127.0.0.1:8081 residue'; Ok = (-not $text.Contains('"127.0.0.1:8081"')) }
    )
    $failed = @($checks | Where-Object { -not $_.Ok })
    if ($failed.Count -gt 0) {
        throw (@("generated config failed the pre-write self-check ($($failed.Count) item(s)):") + ($failed | ForEach-Object { " - $($_.Name)" })) -join "`n"
    }
}

# ---------------------------------------------------------------------------
# 1. Parameter validation (plan context E)
# ---------------------------------------------------------------------------

try {
    Write-Host "==> HTA one-shot installer" -ForegroundColor Cyan
    Write-Host "    SourceDir : $SourceDir"
    Write-Host "    InstallDir: $InstallDir"
    Write-Host "    DataDir   : $DataDir"

    if (-not (Test-Path -LiteralPath $SourceDir -PathType Container)) {
        Fail "SourceDir not found: $SourceDir"
    }

    foreach ($portSpec in @(@{ Name = '-PublicPort'; Value = $PublicPort }, @{ Name = '-HealthPort'; Value = $HealthPort })) {
        $pv = $portSpec.Value
        if ($pv -lt 1 -or $pv -gt 65535) {
            Fail "$($portSpec.Name) $pv out of range (1-65535)"
        }
        if ($script:DevReservedPorts -contains $pv) {
            Fail "$($portSpec.Name) $pv is a development-reserved port ($($script:DevReservedPorts -join '/ ') are used by run.ps1 dev modes). Pick a different port."
        }
    }
    if ($PublicPort -eq $HealthPort) {
        Fail "-PublicPort and -HealthPort must differ (both are $PublicPort)"
    }
    foreach ($portSpec in @(@{ Name = '-PublicPort'; Value = $PublicPort }, @{ Name = '-HealthPort'; Value = $HealthPort })) {
        $listeners = @(Get-NetTCPConnection -LocalPort $portSpec.Value -State Listen -ErrorAction SilentlyContinue)
        if ($listeners.Count -gt 0) {
            Write-Host "WARNING: port $($portSpec.Value) already has a listener (PID(s) $(($listeners.OwningProcess | Sort-Object -Unique) -join ',')) - the service may fail to bind; consider a different $($portSpec.Name)" -ForegroundColor Yellow
        }
    }

    if ($LanIp) {
        if (-not (Test-ValidLanIp $LanIp)) {
            Fail "-LanIp '$LanIp' is not a valid non-loopback IPv4 literal"
        }
    } else {
        $LanIp = Get-AutoLanIp
        if (-not $LanIp) {
            Fail "could not auto-detect a LAN IPv4 address - pass one explicitly with -LanIp <x.x.x.x>"
        }
        Write-Host "    LanIp     : $LanIp (auto-detected; override with -LanIp)"
    }

    # ---------------------------------------------------------------------------
    # 2. Payload inventory - collect ALL missing items, then exit once
    # ---------------------------------------------------------------------------

    $pyFiles = @(Get-ChildItem -LiteralPath (Join-Path $SourceDir 'third_party\python') -Filter '*.py' -File -ErrorAction SilentlyContinue)
    $payload = @(
        @{ Path = (Join-Path $SourceDir 'bin\service.exe');        Hint = 'build it first: go build -o bin/service.exe ./cmd/service (or let scripts/deploy.ps1 / batch item 261024-njp do it)' },
        @{ Path = (Join-Path $SourceDir 'pkg\web\static');         Hint = 'static frontend missing from the checkout' },
        @{ Path = (Join-Path $SourceDir 'third_party\python\vocab_manager.py');       Hint = 'vendored Python payload missing from the checkout' },
        @{ Path = (Join-Path $SourceDir 'third_party\python\pregenerate_dictation.py'); Hint = 'vendored Python payload missing from the checkout' },
        @{ Path = (Join-Path $SourceDir 'third_party\python\requirements.txt');       Hint = 'vendored lockfile missing from the checkout (the venv step installs exactly this file)' },
        @{ Path = (Join-Path $SourceDir 'config.example.yaml');    Hint = 'config transform source missing from the checkout' },
        @{ Path = (Join-Path $SourceDir '.local-certs\hta-lan.pem');     Hint = 'mkcert LAN certificate missing - generate with mkcert (see .local-certs docs)' },
        @{ Path = (Join-Path $SourceDir '.local-certs\hta-lan-key.pem'); Hint = 'mkcert LAN key missing - generate with mkcert (see .local-certs docs)' },
        @{ Path = (Join-Path $SourceDir 'scripts\hta.ps1');        Hint = 'daily start/stop script not produced yet (batch item 261024-njo); run that item, then re-run this installer' }
    )
    $missing = @($payload | Where-Object { -not (Test-Path -LiteralPath $_.Path) })
    if ($pyFiles.Count -eq 0) {
        $missing += @{ Path = (Join-Path $SourceDir 'third_party\python\*.py'); Hint = 'no vendored Python scripts found' }
    }
    if ($missing.Count -gt 0) {
        Write-Host "ERROR: payload inventory failed - $($missing.Count) item(s) missing from SourceDir:" -ForegroundColor Red
        foreach ($m in $missing) {
            Write-Host "  - $($m.Path)" -ForegroundColor Red
            Write-Host "    fix: $($m.Hint)" -ForegroundColor Yellow
        }
        exit 1
    }
    $venvPython = Join-Path $InstallDir 'venv\Scripts\python.exe'
    if (-not (Test-Path -LiteralPath $venvPython)) {
        if (-not (Get-Command uv -ErrorAction SilentlyContinue)) {
            Fail "venv needs to be created but uv was not found in PATH (install uv: https://docs.astral.sh/uv/). If the venv already exists this check is skipped."
        }
    }

    # ---------------------------------------------------------------------------
    # 3. Directory skeleton (New-Item -Force is idempotent)
    # ---------------------------------------------------------------------------

    $created = @()
    $kept = @()
    $skipped = @()
    foreach ($dir in @('bin', 'web', 'python', 'config', 'certs', 'secrets', 'logs')) {
        $p = Join-Path $InstallDir $dir
        if (-not (Test-Path -LiteralPath $p)) { $created += $dir }
        New-Item -ItemType Directory -Force -Path $p | Out-Null
    }
    if (-not (Test-Path -LiteralPath $DataDir)) { $created += $DataDir }
    New-Item -ItemType Directory -Force -Path $DataDir | Out-Null

    # ---------------------------------------------------------------------------
    # 4. Copies - each item with its explicit refresh semantics
    # ---------------------------------------------------------------------------

    # Refreshable (-Force): binary, static frontend, vendored python, hta.ps1.
    Copy-Item -LiteralPath (Join-Path $SourceDir 'bin\service.exe') -Destination (Join-Path $InstallDir 'bin\service.exe') -Force
    Copy-Item -LiteralPath (Join-Path $SourceDir 'pkg\web\static') -Destination (Join-Path $InstallDir 'web\static') -Recurse -Force
    foreach ($py in $pyFiles) {
        Copy-Item -LiteralPath $py.FullName -Destination (Join-Path $InstallDir 'python') -Force
    }
    Copy-Item -LiteralPath (Join-Path $SourceDir 'third_party\python\requirements.txt') -Destination (Join-Path $InstallDir 'python\requirements.txt') -Force
    Copy-Item -LiteralPath (Join-Path $SourceDir 'scripts\hta.ps1') -Destination (Join-Path $InstallDir 'hta.ps1') -Force

    # Fill-if-missing (never overwrite): TLS leaf cert + key.
    foreach ($certFile in @('hta-lan.pem', 'hta-lan-key.pem')) {
        $dst = Join-Path $InstallDir "certs\$certFile"
        if (Test-Path -LiteralPath $dst) {
            $kept += "certs\$certFile"
            Write-Host "[install] keeping existing certs\$certFile (delete it to refresh from SourceDir)" -ForegroundColor Yellow
        } else {
            Copy-Item -LiteralPath (Join-Path $SourceDir ".local-certs\$certFile") -Destination $dst
            $created += "certs\$certFile"
        }
    }
    # iPad root CA: copy if present in SourceDir and missing at target; a
    # missing source is a note, not a failure (new-iPad enrollment aid only).
    foreach ($caFile in @('rootCA.pem', 'rootCA.crt')) {
        $src = Join-Path $SourceDir ".local-certs\ipad\$caFile"
        if (Test-Path -LiteralPath $src) {
            $dstDir = Join-Path $InstallDir 'certs\ipad'
            New-Item -ItemType Directory -Force -Path $dstDir | Out-Null
            $dst = Join-Path $dstDir $caFile
            if (Test-Path -LiteralPath $dst) {
                $kept += "certs\ipad\$caFile"
            } else {
                Copy-Item -LiteralPath $src -Destination $dst
                $created += "certs\ipad\$caFile"
            }
        } else {
            $skipped += "certs\ipad\$caFile (not present in SourceDir - iPad root CA distribution skipped)"
        }
    }

    # secrets\.env: seed ONCE from the tracked template (no real secrets in
    # git, D-05). Never overwrite - the user fills real VOLCENGINE_TTS_* here.
    $envDst = Join-Path $InstallDir 'secrets\.env'
    if (Test-Path -LiteralPath $envDst) {
        $kept += 'secrets\.env'
        Write-Host "[install] keeping existing secrets\.env (never overwritten)" -ForegroundColor Yellow
    } else {
        Copy-Item -LiteralPath (Join-Path $SourceDir 'third_party\python\.env.example') -Destination $envDst
        $created += 'secrets\.env'
        Write-Host "[install] seeded secrets\.env from the template - FILL IN the real VOLCENGINE_TTS_* values (values are never echoed by this installer)" -ForegroundColor Yellow
    }

    # ---------------------------------------------------------------------------
    # 5. Production config generation (plan context B)
    # ---------------------------------------------------------------------------

    $configDst = Join-Path $InstallDir 'config\config.yaml'
    if (Test-Path -LiteralPath $configDst) {
        $kept += 'config\config.yaml'
        Write-Host "[install] keeping existing config\config.yaml (delete that file and re-run to regenerate)" -ForegroundColor Yellow
    } else {
        # @() re-arrays the pipeline-unrolled return value (a List[string]
        # comes back as string[]/string, which has no .ToArray()).
        $lines = @(ConvertTo-ProductionConfig -ExamplePath (Join-Path $SourceDir 'config.example.yaml') `
            -InstallDirPath $InstallDir -DataDirPath $DataDir -PubPort $PublicPort -HthPort $HealthPort -Lip $LanIp)
        Test-GeneratedConfig -Lines $lines -InstallDirPath $InstallDir -DataDirPath $DataDir -PubPort $PublicPort -HthPort $HealthPort -Lip $LanIp
        Set-Content -LiteralPath $configDst -Value $lines -Encoding Ascii
        $created += 'config\config.yaml'
    }

    # ---------------------------------------------------------------------------
    # 6. venv via uv (plan context C; Makefile py-bootstrap L81-89, Windows layout)
    # ---------------------------------------------------------------------------

    if (Test-Path -LiteralPath $venvPython) {
        $kept += 'venv'
        Write-Host "[install] keeping existing venv (delete venv\ and re-run to rebuild)" -ForegroundColor Yellow
    } else {
        Write-Host "==> Creating venv at $InstallDir\venv via uv (Windows layout; uv may download Python 3.12 on first run - networked)" -ForegroundColor Cyan
        & uv venv --python 3.12 (Join-Path $InstallDir 'venv')
        if ($LASTEXITCODE -ne 0) { Fail "uv venv failed (exit $LASTEXITCODE)" }
        $env:UV_INDEX_URL = $PyIndex
        & uv pip install --python $venvPython -r (Join-Path $InstallDir 'python\requirements.txt')
        if ($LASTEXITCODE -ne 0) { Fail "uv pip install failed (exit $LASTEXITCODE)" }
        # Clear the env var without Remove-Item (this installer is deletion-free).
        $env:UV_INDEX_URL = ''
        if (-not (Test-Path -LiteralPath $venvPython)) {
            Fail "venv step finished but $venvPython is missing"
        }
        $created += 'venv'
    }

    # ---------------------------------------------------------------------------
    # 6b. Vocab schema bootstrap (issue #78) - best effort, NEVER an install
    # failure (matches the firewall/cert-expiry degrade pattern).
    #
    # The Python side owns cards/textbooks/review_logs/card_assignments; Go's
    # Migrate deliberately does not create them. On a fresh install the service
    # reads SQLite directly (GET /api/v1/units -> CountByUnit) without ever
    # having spawned a Python process, so those tables do not exist yet and the
    # endpoint 500s with "no such table: cards". Run vocab_manager.py's explicit
    # `init` subcommand once here (idempotent CREATE TABLE IF NOT EXISTS) to
    # guarantee the schema exists before first start. A broken venv python
    # degrades to a printed manual command, never an install failure.
    # ---------------------------------------------------------------------------
    $vocabDB = Join-Path $DataDir 'vocab.db'
    $vocabScript = Join-Path $InstallDir 'python\vocab_manager.py'
    Write-Host "==> Initializing vocab schema at $vocabDB" -ForegroundColor Cyan
    try {
        & $venvPython $vocabScript --db $vocabDB init
        if ($LASTEXITCODE -ne 0) { throw "vocab_manager init exited $LASTEXITCODE" }
        Write-Host "[install] vocab schema ready (cards/textbooks/etc.)" -ForegroundColor Green
    } catch {
        Write-Host "WARNING: could not initialize the vocab schema ($($_.Exception.Message)) - run once manually:" -ForegroundColor Yellow
        Write-Host "  & `"$venvPython`" `"$vocabScript`" --db `"$vocabDB`" init" -ForegroundColor Yellow
    }

    # ---------------------------------------------------------------------------
    # 7. Firewall (plan context E) - best effort, NEVER an install failure
    # ---------------------------------------------------------------------------

    $fwState = 'skipped'   # skipped | applied | already | degraded
    $fwDetail = ''
    if ($SkipFirewall) {
        Write-Host "[install] -SkipFirewall given - no firewall changes" -ForegroundColor Yellow
        $fwDetail = '-SkipFirewall was set; no firewall rule was touched.'
    } else {
        $existingRule = Get-NetFirewallRule -DisplayName $script:FirewallRuleName -ErrorAction SilentlyContinue
        if ($existingRule) {
            $fwState = 'already'
            Write-Host "[install] firewall rule '$($script:FirewallRuleName)' already exists - nothing to do" -ForegroundColor Green
        } else {
            try {
                if (Test-IsAdmin) {
                    & netsh advfirewall firewall add rule name="$script:FirewallRuleName" dir=in action=allow protocol=TCP localport=$PublicPort | Out-Null
                    if ($LASTEXITCODE -ne 0) { throw "netsh exited $LASTEXITCODE" }
                    $fwState = 'applied'
                    Write-Host "[install] firewall rule '$($script:FirewallRuleName)' added for TCP $PublicPort" -ForegroundColor Green
                } else {
                    throw 'not running as Administrator'
                }
            } catch {
                $fwState = 'degraded'
                Write-Host "WARNING: could not add the firewall rule ($($_.Exception.Message)) - run this once in an ADMIN PowerShell:" -ForegroundColor Yellow
                Write-Host "  Start-Process powershell -Verb RunAs -ArgumentList '-NoProfile -Command $($script:FirewallCommand)'" -ForegroundColor Yellow
                $fwDetail = "not applied automatically ($($_.Exception.Message)); run the command below once as Administrator."
            }
        }
    }

    # ---------------------------------------------------------------------------
    # 8. Certificate expiry + INSTALL-NOTES.md (plan context D/G)
    # ---------------------------------------------------------------------------

    $certPem = Join-Path $InstallDir 'certs\hta-lan.pem'
    $certExpiry = ''
    $certExpiryNote = ''
    try {
        $certExpiry = Get-CertNotAfterDate -PemPath $certPem
    } catch {
        $certExpiryNote = 'unknown (parse failed). Check manually with: openssl x509 -enddate -noout -in "' + $certPem + '"'
        Write-Host "WARNING: could not parse the certificate expiry date ($($_.Exception.Message)) - install continues" -ForegroundColor Yellow
    }

    $hasIpadCa = (Test-Path -LiteralPath (Join-Path $InstallDir 'certs\ipad\rootCA.pem')) -or (Test-Path -LiteralPath (Join-Path $InstallDir 'certs\ipad\rootCA.crt'))
    $notesPath = Join-Path $InstallDir 'INSTALL-NOTES.md'
    $stamp = (Get-Date).ToString('yyyy-MM-dd HH:mm:ss')
    $expiryLine = if ($certExpiry) { "Leaf certificate (certs\hta-lan.pem) expires: $certExpiry" } else { "Leaf certificate (certs\hta-lan.pem) expiry: $certExpiryNote" }
    $fwLine = if ($fwState -eq 'applied' -or $fwState -eq 'already') {
        "Firewall: inbound rule '$($script:FirewallRuleName)' (TCP $PublicPort) is in place."
    } else {
        "Firewall: $fwDetail If iPads cannot reach the service, run this ONCE in an ADMIN PowerShell:`n`n    $($script:FirewallCommand)`n`n  one-liner:  Start-Process powershell -Verb RunAs -ArgumentList '-NoProfile -Command $($script:FirewallCommand)'"
    }
    $ipadLine = if ($hasIpadCa) {
        "New iPad: install the root CA first - open certs\ipad\rootCA.crt on the device (or serve it), then trust it (Settings > General > About > Certificate Trust Settings)."
    } else {
        "New iPad: no root CA was copied (certs\ipad\ not found in the source checkout) - export the mkcert root CA and install it on the device before trusting the HTTPS entry."
    }
    $notes = @"
# HTA Install Notes

Generated: $stamp (regenerated on every install run)

## Layout

``````
$InstallDir\
  bin\service.exe            service binary (refreshed from source on each run)
  web\static\                student/parent frontend (refreshed)
  python\                    vendored Python scripts + requirements.txt (refreshed)
  config\config.yaml         production config (NEVER overwritten; delete to regenerate)
  certs\hta-lan.pem          TLS certificate (fill-if-missing)
  certs\hta-lan-key.pem      TLS private key (fill-if-missing)
  certs\ipad\                mkcert root CA for new iPads (if available)
  secrets\.env               secrets - NEVER overwritten; fill VOLCENGINE_TTS_* once
  venv\Scripts\python.exe    uv-built venv, Python 3.12 (delete venv\ to rebuild)
  data\                      runtime data (vocab.db, cache) - installer never touches contents
  logs\                      service logs (written by hta.ps1)
  hta.ps1                    daily start/stop script (refreshed)
  INSTALL-NOTES.md           this file (regenerated on every run)
``````

## Network

- LAN IP: $LanIp
- Public port: $PublicPort (HTTPS) - dev-reserved ports 8080/8443/8081 are deliberately avoided because run.ps1 uses them for local development sessions
- Health port: $HealthPort (127.0.0.1 only; no firewall rule needed)
- Student entry (iPad): https://$LanIp`:$PublicPort/tutor/
- Parent page: https://$LanIp`:$PublicPort/parent.html

## TLS certificate

$expiryLine

$ipadLine

## Secrets

secrets\.env holds the VOLCENGINE_TTS_* keys. This installer only seeds the
template ONCE and never prints or overwrites its contents. Edit the file
directly to fill in the real values.

## Firewall

$fwLine

## Start / stop

    .\hta.ps1 start     # daily start (scripts\hta.ps1, batch item 261024-njo)

Created: $($created -join ', ')
Kept (idempotent, untouched): $($kept -join ', ')
Skipped: $($skipped -join ', ')
"@
    Set-Content -LiteralPath $notesPath -Value $notes -Encoding Ascii
    $created += 'INSTALL-NOTES.md (regenerated)'

    # ---------------------------------------------------------------------------
    # 9. Closing summary
    # ---------------------------------------------------------------------------

    Write-Host ""
    Write-Host "==> Install complete: $InstallDir" -ForegroundColor Green
    Write-Host "    created: $($created -join ', ')"
    if ($kept.Count -gt 0) { Write-Host "    kept (idempotent): $($kept -join ', ')" -ForegroundColor Yellow }
    if ($skipped.Count -gt 0) { Write-Host "    skipped: $($skipped -join ', ')" -ForegroundColor Yellow }
    Write-Host ""
    Write-Host "Next steps:" -ForegroundColor Cyan
    Write-Host "  1. Fill secrets\.env with the real VOLCENGINE_TTS_* values (edit the file directly)."
    if ($fwState -eq 'degraded' -or $fwState -eq 'skipped') {
        Write-Host "  2. If iPads cannot reach the service, run the firewall command in INSTALL-NOTES.md once as Administrator."
    } else {
        Write-Host "  2. Firewall is set up (rule '$($script:FirewallRuleName)')."
    }
    Write-Host "  3. Start the service:  .\hta.ps1 start   (from $InstallDir)"
    Write-Host "  4. Open on the iPad:   https://$LanIp`:$PublicPort/tutor/"
    Write-Host "  See INSTALL-NOTES.md for the full checklist."
    exit 0
} catch {
    Write-Host "ERROR: install failed: $($_.Exception.Message)" -ForegroundColor Red
    if ($_.Exception.Message -match 'transform failed|self-check') {
        Write-Host ($_.ScriptStackTrace -split "`r?`n" | Select-Object -First 3) -ForegroundColor DarkYellow
    }
    exit 1
}
