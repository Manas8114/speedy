<#
.SYNOPSIS
    Uninstalls the Speedy 2.0 Multi-WAN Bonding Background Service on Windows.
#>

$ErrorActionPreference = "Stop"

Write-Host "==========================================================" -ForegroundColor Cyan
Write-Host "  Speedy 2.0 — Windows Service Uninstaller               " -ForegroundColor Cyan
Write-Host "==========================================================" -ForegroundColor Cyan

# 1. Administrator Check
$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $isAdmin) {
    Write-Warning "Administrator rights required to uninstall Windows Services."
    Start-Process powershell -ArgumentList "-NoProfile -ExecutionPolicy Bypass -File `"$PSCommandPath`"" -Verb RunAs
    exit
}

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$ProjectRoot = Split-Path -Parent $ScriptDir
$BinaryPath = Join-Path $ProjectRoot "bin\speedy-client.exe"

if (Test-Path $BinaryPath) {
    Write-Host "[1/2] Stopping and removing 'speedy-tunnel' service..." -ForegroundColor Cyan
    try {
        & $BinaryPath --service stop
    } catch {}
    try {
        & $BinaryPath --service uninstall
    } catch {
        Write-Warning "Uninstall command: $_"
    }
} else {
    Write-Host "Using sc.exe delete fallback..." -ForegroundColor Yellow
    sc.exe stop speedy-tunnel
    sc.exe delete speedy-tunnel
}

Write-Host "[2/2] Cleaning up firewall rules..." -ForegroundColor Cyan
Remove-NetFirewallRule -Name "SpeedyBondingUDP" -ErrorAction SilentlyContinue

Write-Host "✓ Speedy 2.0 Service Uninstalled Successfully." -ForegroundColor Green
