# run-full-regression.ps1 — Windows entry for the full local regression gate.
# Browser E2E is the default door (see docs/E2E-COVERAGE-MATRIX.md).
#
#   npm run regress
#   powershell -NoProfile -ExecutionPolicy Bypass -File scripts/run-full-regression.ps1 [--skip-real] [--require-real] [--skip-student]
param(
    [switch]$SkipReal,
    [switch]$RequireReal,
    [switch]$SkipStudent
)

$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
Set-Location $root

$nodeArgs = @('scripts/run-full-regression.mjs')
if ($SkipReal) { $nodeArgs += '--skip-real' }
if ($RequireReal) { $nodeArgs += '--require-real' }
if ($SkipStudent) { $nodeArgs += '--skip-student' }

Write-Host "[regress.ps1] node $($nodeArgs -join ' ')" -ForegroundColor Cyan
& node @nodeArgs
exit $LASTEXITCODE
