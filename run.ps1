<#
.SYNOPSIS
    Starts the Home Tutor Agent Go service from the repository root.

.DESCRIPTION
    Phase 8 gap-closure fix (G-08-2): the service loads config.yaml via a
    relative path (-config config.yaml). LoadConfig silently skips a missing
    file and falls back to DefaultConfig, whose StaticDir is empty — that
    DISABLES static file serving so the student interface never loads and
    the service serves the debug test-interface.html instead.

    This script guarantees the process CWD is the repository root (where
    config.yaml lives) so the relative path resolves. It also bootstraps
    config.yaml from config.example.yaml on first run.

.NOTES
    The student interface (pkg/web/static/) requires server.staticDir to be
    set in config.yaml. The example template ships with staticDir pre-enabled;
    if you have an older config.yaml, add:

      server:
        staticDir: ./pkg/web/static
        staticRoute: /
#>

# Resolve repo root from the script location so the service starts from the
# correct directory regardless of where PowerShell was invoked.
$RepoRoot = Split-Path -Parent $MyInvocation.MyCommand.Definition
Set-Location $RepoRoot

Write-Host "==> Working directory: $(Get-Location)" -ForegroundColor Cyan

# Bootstrap config.yaml from the example on first run.
if (-not (Test-Path "config.yaml")) {
    if (Test-Path "config.example.yaml") {
        Write-Host "==> config.yaml not found — copying from config.example.yaml" -ForegroundColor Yellow
        Copy-Item "config.example.yaml" "config.yaml"
        Write-Host "==> Created config.yaml. Edit python.interpreter to point at your venv." -ForegroundColor Yellow
    } else {
        Write-Host "ERROR: Neither config.yaml nor config.example.yaml found in $RepoRoot" -ForegroundColor Red
        exit 1
    }
}

# Verify the static directory exists (fail-fast with a clear message).
$staticDir = "./pkg/web/static"
if (-not (Test-Path $staticDir)) {
    Write-Host "ERROR: Static directory $staticDir not found. Student interface will not load." -ForegroundColor Red
    exit 1
}

Write-Host "==> Starting service (config.yaml, CWD=$(Get-Location))" -ForegroundColor Green
go run ./cmd/service -config config.yaml
