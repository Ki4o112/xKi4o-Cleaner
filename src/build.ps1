# Builds xKi4oCleaner.exe next to this script (in src\).
# Needs Go 1.24+ (https://go.dev/dl/). Run:  .\build.ps1
# Add -UI to regenerate ui\index.html from ui\app.html first (needs Python 3).
param([switch]$UI)

$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Error "Go is not installed. Download it from https://go.dev/dl/ and open a new terminal."
}

if ($UI) {
    python ui\build_ui.py
    if ($LASTEXITCODE -ne 0) { Write-Error "Could not build ui\index.html" }
}

go build -trimpath -ldflags "-s -w -H windowsgui" -o xKi4oCleaner.exe .
if ($LASTEXITCODE -ne 0) { Write-Error "Build failed" }

Write-Host "Built xKi4oCleaner.exe" -ForegroundColor Green
