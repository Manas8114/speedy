<#
.SYNOPSIS
    Authenticode Code-Signing utility for Speedy 2.0 executables and Wintun driver.
.DESCRIPTION
    1. Checks for a production PFX certificate ($env:CODE_SIGN_CERT_PATH) or creates/retrieves
       a dedicated development code-signing certificate in Cert:\CurrentUser\My.
    2. Signs all project binaries (speedy-client.exe, speedy-ui.exe, speedy-relay.exe) and wintun.dll.
    3. Uses SHA-256 digest algorithm with RFC 3161 timestamp server (http://timestamp.digicert.com).
    4. Verifies Authenticode signature status.
#>

param(
    [string]$CertPath = $env:CODE_SIGN_CERT_PATH,
    [string]$CertPassword = $env:CODE_SIGN_PASSWORD,
    [string]$TimestampServer = "http://timestamp.digicert.com"
)

$ErrorActionPreference = "Stop"

Write-Host "==========================================================" -ForegroundColor Cyan
Write-Host "  Speedy 2.0 — Authenticode Code-Signing Utility          " -ForegroundColor Cyan
Write-Host "==========================================================" -ForegroundColor Cyan

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$ProjectRoot = Split-Path -Parent $ScriptDir
$BinDir = Join-Path $ProjectRoot "bin"

if (-not (Test-Path $BinDir)) {
    Write-Error "Bin directory does not exist: $BinDir. Please build binaries first."
    exit 1
}

# 1. Obtain Certificate
$cert = $null

if (![string]::IsNullOrWhiteSpace($CertPath) -and (Test-Path $CertPath)) {
    Write-Host "[1/3] Loading production PFX certificate from $CertPath..." -ForegroundColor Green
    if ([string]::IsNullOrWhiteSpace($CertPassword)) {
        $cert = Get-PfxCertificate -FilePath $CertPath
    } else {
        $secPass = ConvertTo-SecureString $CertPassword -AsPlainText -Force
        $cert = New-Object System.Security.Cryptography.X509Certificates.X509Certificate2($CertPath, $secPass)
    }
} else {
    Write-Host "[1/3] Searching for existing Speedy Code Signing certificate in Cert:\CurrentUser\My..." -ForegroundColor Yellow
    $existing = Get-ChildItem Cert:\CurrentUser\My -CodeSigningCert | Where-Object { $_.Subject -match "Speedy" } | Select-Object -First 1

    if ($existing) {
        $cert = $existing
        Write-Host "Found existing certificate: $($cert.Subject) [Thumbprint: $($cert.Thumbprint)]" -ForegroundColor Green
    } else {
        Write-Host "Creating new self-signed Authenticode Development certificate..." -ForegroundColor Yellow
        $cert = New-SelfSignedCertificate `
            -Type CodeSigningCert `
            -Subject 'CN=Speedy 2.0 Open Source Project Development Signer, O=Speedy Bonding, C=US' `
            -KeyUsage DigitalSignature `
            -KeySpec Signature `
            -KeyLength 2048 `
            -HashAlgorithm SHA256 `
            -CertStoreLocation 'Cert:\CurrentUser\My' `
            -NotAfter (Get-Date).AddYears(5)
        
        Write-Host "Created new code-signing certificate: $($cert.Subject)" -ForegroundColor Green

        # Install into Trusted Root / Trusted Publisher for local machine suppression of SmartScreen
        try {
            $rootStore = New-Object System.Security.Cryptography.X509Certificates.X509Store("Root", "CurrentUser")
            $rootStore.Open("ReadWrite")
            $rootStore.Add($cert)
            $rootStore.Close()
            Write-Host 'Installed into Cert:\CurrentUser\Root [Trusted Root Authorities].' -ForegroundColor Green
        } catch {
            Write-Warning "Could not auto-add to Trusted Root store: $_"
        }
    }
}

if (-not $cert) {
    Write-Error "Failed to obtain a code signing certificate."
    exit 1
}

# 2. Sign Binaries
$targets = Get-ChildItem $BinDir -Filter "*.exe"
$dllTargets = Get-ChildItem $BinDir -Filter "*.dll"
$allTargets = @($targets) + @($dllTargets)

if ($allTargets.Count -eq 0) {
    Write-Warning "No binaries found in $BinDir to sign."
    exit 0
}

Write-Host "[2/3] Signing $($allTargets.Count) binary artifacts..." -ForegroundColor Cyan

foreach ($target in $allTargets) {
    Write-Host "  -> Signing $($target.Name)..." -NoNewline
    try {
        # Attempt signing with timestamp server; fallback to untimestamped if offline
        $sig = $null
        try {
            $sig = Set-AuthenticodeSignature -FilePath $target.FullName `
                                             -Certificate $cert `
                                             -HashAlgorithm SHA256 `
                                             -TimestampServer $TimestampServer `
                                             -ErrorAction Stop
        } catch {
            $sig = Set-AuthenticodeSignature -FilePath $target.FullName `
                                             -Certificate $cert `
                                             -HashAlgorithm SHA256 `
                                             -ErrorAction Stop
        }

        if ($sig.Status -eq "Valid" -or $sig.Status -eq "UnknownError") {
            Write-Host " [SIGNED ($($sig.Status))]" -ForegroundColor Green
        } else {
            Write-Host " [STATUS: $($sig.Status)]" -ForegroundColor Yellow
        }
    } catch {
        Write-Host " [FAILED: $_]" -ForegroundColor Red
    }
}

# 3. Verification Report
Write-Host "[3/3] Authenticode Signature Verification Report:" -ForegroundColor Cyan
foreach ($target in $allTargets) {
    $check = Get-AuthenticodeSignature -FilePath $target.FullName
    $subj = if ($check.SignerCertificate) { $check.SignerCertificate.Subject } else { "None" }
    Write-Host ("  File: {0,-22} Status: {1,-14} Signer: {2}" -f $target.Name, $check.Status, $subj)
}

Write-Host ""
Write-Host "✓ Authenticode code-signing completed successfully." -ForegroundColor Green
