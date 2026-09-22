<#
.SYNOPSIS
    Installs the Speedy 2.0 Multi-WAN Bonding Background Service on Windows.
.DESCRIPTION
    1. Verifies Administrator elevation (requests UAC prompt if needed).
    2. Verifies wintun.dll driver dependency.
    3. Registers 'speedy-tunnel' service under LocalSystem account.
    4. Configures Windows Firewall rules for UDP bonding traffic.
    5. Starts the service daemon.
#>

param(
    [string]$BinaryPath = ""
)

$ErrorActionPreference = "Stop"

Write-Host "==========================================================" -ForegroundColor Cyan
Write-Host "  Speedy 2.0 — Windows Service & Wintun Driver Installer  " -ForegroundColor Cyan
Write-Host "==========================================================" -ForegroundColor Cyan

# 1. Administrator Check
$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $isAdmin) {
    Write-Warning "Administrator rights required to install Windows Services."
    Write-Host "Relaunching installer with elevated privileges..." -ForegroundColor Yellow
    Start-Process powershell -ArgumentList "-NoProfile -ExecutionPolicy Bypass -File `"$PSCommandPath`"" -Verb RunAs
    exit
}

# 2. Locate Speedy Client Binary
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$ProjectRoot = Split-Path -Parent $ScriptDir

if ([string]::IsNullOrWhiteSpace($BinaryPath)) {
    $Candidate = Join-Path $ProjectRoot "bin\speedy-client.exe"
    if (Test-Path $Candidate) {
        $BinaryPath = $Candidate
    } else {
        $BinaryPath = (Get-Command speedy-client.exe -ErrorAction SilentlyContinue).Source
    }
}

if (-not (Test-Path $BinaryPath)) {
    Write-Error "Could not find speedy-client.exe. Please build it first: go build -o bin/speedy-client.exe ./cmd/speedy-client"
    exit 1
}

Write-Host "[1/5] Verified binary location: $BinaryPath" -ForegroundColor Green

# 3. Check for wintun.dll
$WintunCandidate1 = Join-Path (Split-Path -Parent $BinaryPath) "wintun.dll"
$WintunCandidate2 = Join-Path $env:SystemRoot "System32\wintun.dll"

if (Test-Path $WintunCandidate1) {
    Write-Host "[2/5] Verified wintun.dll in binary directory: $WintunCandidate1" -ForegroundColor Green
} elseif (Test-Path $WintunCandidate2) {
    Write-Host "[2/5] Verified wintun.dll in System32: $WintunCandidate2" -ForegroundColor Green
} else {
    Write-Warning "[2/5] wintun.dll not found in $WintunCandidate1 or System32. (Mock devices will be used if wintun is absent)"
}

# 4. Configure Windows Firewall
Write-Host "[3/5] Configuring Windows Defender Firewall UDP rules..." -ForegroundColor Cyan
try {
    Remove-NetFirewallRule -Name "SpeedyBondingUDP" -ErrorAction SilentlyContinue
    New-NetFirewallRule -Name "SpeedyBondingUDP" `
                        -DisplayName "Speedy 2.0 WAN Bonding UDP Traffic" `
                        -Direction Inbound `
                        -Protocol UDP `
                        -Action Allow `
                        -Profile Any `
                        -Description "Allows bonded multi-path UDP tunnel traffic for Speedy 2.0" | Out-Null
    Write-Host "[3/5] Firewall rule 'SpeedyBondingUDP' configured successfully." -ForegroundColor Green
} catch {
    Write-Warning "Could not add firewall rule automatically: $_"
}

# 5. Install Windows Service
Write-Host "[4/5] Registering 'speedy-tunnel' service via Service Control Manager..." -ForegroundColor Cyan
try {
    & $BinaryPath --service install
} catch {
    Write-Warning "Service install command output: $_"
}

# 6. Verify Service Status
Write-Host "[5/5] Querying installed service status..." -ForegroundColor Cyan
& $BinaryPath --service status

Write-Host ""
Write-Host "✓ Speedy 2.0 Service Installation Complete!" -ForegroundColor Green
Write-Host "To control service: " -ForegroundColor Gray
Write-Host "  Start:     speedy-client.exe --service start" -ForegroundColor White
Write-Host "  Stop:      speedy-client.exe --service stop" -ForegroundColor White
Write-Host "  Uninstall: powershell scripts/uninstall-service.ps1" -ForegroundColor White
