# UAT 本地启动脚本 — Home Tutor Agent
# 用法: pwsh scripts/run-uat.ps1
# 作用: 加载 .env 密钥 → 停旧实例 → 构建 → 启动 → 健康检查 → 报告 iPad 访问地址
# 背景: 2026-09-13 UAT 发现服务未带 .env 启动导致 MIMO_API_KEY 缺失（语音命令/OCR/Mimo TTS 全部失效）。

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

if (-not (Test-Path .env)) { throw "未找到 .env（应有 MIMO_API_KEY / VOLCENGINE_TTS_* 等）" }

# 1. 加载 .env 到进程环境（Start-Process 的子进程会继承）
$loaded = 0
foreach ($line in Get-Content .env) {
    $t = $line.Trim()
    if ($t -and -not $t.StartsWith('#') -and $t.Contains('=')) {
        $k, $v = $t -split '=', 2
        Set-Item -Path "Env:$k" -Value $v
        $loaded++
    }
}
Write-Host "[1/5] .env 已加载 $loaded 个变量"

# 2. 停止占用 8080 的旧实例
$oldConns = Get-NetTCPConnection -LocalPort 8080 -State Listen -ErrorAction SilentlyContinue
if ($oldConns) {
    $oldPids = $oldConns | Select-Object -ExpandProperty OwningProcess -Unique
    foreach ($opid in $oldPids) {
        Write-Host "[2/5] 停止旧实例 PID=$opid"
        Stop-Process -Id $opid -Force -ErrorAction SilentlyContinue
    }
    Start-Sleep -Milliseconds 800
} else {
    Write-Host "[2/5] 无旧实例"
}

# 3. 构建
Write-Host "[3/5] 构建 service.exe ..."
go build -o service.exe ./cmd/service/
if ($LASTEXITCODE -ne 0) { throw "构建失败" }

# 4. 启动（继承含 .env 的环境）
New-Item -ItemType Directory -Force -Path .hta-data | Out-Null
$p = Start-Process -FilePath ".\service.exe" -WorkingDirectory $root `
    -RedirectStandardOutput ".hta-data\uat.out.log" -RedirectStandardError ".hta-data\uat.err.log" `
    -PassThru -WindowStyle Hidden
Write-Host "[4/5] 已启动 PID=$($p.Id)"

# 5. 健康检查（最多 15s）
$ok = $false
for ($i = 0; $i -lt 15; $i++) {
    Start-Sleep -Milliseconds 1000
    try {
        $h = Invoke-RestMethod -Uri "http://127.0.0.1:8081/health" -TimeoutSec 2
        if ($h.status -eq 'ok') { $ok = $true; break }
    } catch { }
}
if (-not $ok) {
    Get-Content ".hta-data\uat.err.log" -Tail 20
    throw "健康检查失败 — 见上方日志 / .hta-data/uat.err.log"
}

# MIMO 状态核对（启动告警会写进 err.log）
Start-Sleep -Milliseconds 500
$mimoWarn = Select-String -Path ".hta-data\uat.err.log" -Pattern 'MIMO_API_KEY not set' -Quiet
if ($mimoWarn) {
    Write-Warning "[5/5] MIMO_API_KEY 未生效：语音命令/拍照 OCR/Mimo TTS 禁用（Volcengine 兜底）"
} else {
    Write-Host "[5/5] MIMO_API_KEY 已生效：语音命令 + 拍照 OCR + Mimo TTS（茉莉教师音色）启用"
}

$ip = (Get-NetIPAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue |
    Where-Object { $_.InterfaceAlias -eq 'WLAN' -and $_.IPAddress -notlike '169.254.*' } |
    Select-Object -First 1).IPAddress
if (-not $ip) { $ip = '<WLAN-IP>' }
Write-Host ""
Write-Host "================================================"
Write-Host " 健康检查通过。iPad (同一 WiFi) Safari 打开:"
Write-Host "   http://${ip}:8080"
Write-Host " 日志: .hta-data/uat.err.log (实时: Get-Content -Wait)"
Write-Host "================================================"
