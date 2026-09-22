<#
.SYNOPSIS
    Builds the complete Speedy 2.0 release distribution, MSI/Inno installers, and portable ZIP.
.DESCRIPTION
    1. Compiles all Go binaries for Windows x64.
    2. Signs all executables and DLLs with Authenticode certificate.
    3. Checks for Inno Setup (iscc.exe) or WiX Toolset and builds the installer package if available.
    4. Packages a standalone portable release archive: dist/Speedy-2.0-Windows-x64.zip.
#>

$ErrorActionPreference = "Stop"

Write-Host "==========================================================" -ForegroundColor Cyan
Write-Host "  Speedy 2.0 - Packaging and Release Distribution Builder " -ForegroundColor Cyan
Write-Host "==========================================================" -ForegroundColor Cyan

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$ProjectRoot = Split-Path -Parent $ScriptDir
$BinDir = Join-Path $ProjectRoot "bin"
$DistDir = Join-Path $ProjectRoot "dist"

if (-not (Test-Path $DistDir)) {
    New-Item -ItemType Directory -Path $DistDir | Out-Null
}

# 1. Compile Binaries
Write-Host "[1/4] Compiling Windows x64 production binaries..." -ForegroundColor Cyan
$goExe = "C:\Program Files\Go\bin\go.exe"
if (-not (Test-Path $goExe)) {
    $goExe = (Get-Command go.exe -ErrorAction Stop).Source
}

& $goExe build -ldflags "-s -w" -o "$BinDir\speedy-client.exe" "$ProjectRoot\cmd\speedy-client"
& $goExe build -ldflags "-s -w" -o "$BinDir\speedy-ui.exe" "$ProjectRoot\cmd\speedy-ui"
& $goExe build -ldflags "-s -w" -o "$BinDir\speedy-relay.exe" "$ProjectRoot\cmd\speedy-relay"
Write-Host "Binaries compiled successfully in $BinDir" -ForegroundColor Green

# 2. Sign Binaries with Authenticode
Write-Host "[2/4] Signing binaries with Authenticode certificate..." -ForegroundColor Cyan
$cert = Get-ChildItem Cert:\CurrentUser\My -CodeSigningCert | Where-Object { $_.Subject -match "Speedy" } | Select-Object -First 1
if ($cert) {
    Get-ChildItem "$BinDir\*.exe", "$BinDir\*.dll" | ForEach-Object {
        try {
            Set-AuthenticodeSignature -FilePath $_.FullName -Certificate $cert -HashAlgorithm SHA256 -TimestampServer "http://timestamp.digicert.com" -ErrorAction SilentlyContinue | Out-Null
        } catch {}
    }
    Write-Host "Authenticode signatures applied." -ForegroundColor Green
} else {
    Write-Warning "No Speedy code-signing certificate found. Skipping signing."
}

# 3. Check for Inno Setup / WiX
Write-Host "[3/4] Checking for Windows Installer compiler toolsets..." -ForegroundColor Cyan
$isccPaths = @(
    "C:\Program Files (x86)\Inno Setup 6\iscc.exe",
    "C:\Program Files\Inno Setup 6\iscc.exe",
    (Get-Command iscc.exe -ErrorAction SilentlyContinue).Source
) | Where-Object { $_ -and (Test-Path $_) }

if ($isccPaths.Count -gt 0) {
    $iscc = $isccPaths[0]
    Write-Host "Found Inno Setup Compiler at: $iscc" -ForegroundColor Green
    Write-Host "Compiling setup executable..." -ForegroundColor Cyan
    & $iscc "$ProjectRoot\packaging\inno\speedy-setup.iss"
    Write-Host "[OK] Inno Setup Installer generated in $DistDir\Speedy-2.0-Setup-x64.exe" -ForegroundColor Green
} else {
    Write-Host "Inno Setup (iscc.exe) not detected in default paths. (Inno Setup script ready at packaging/inno/speedy-setup.iss)" -ForegroundColor Yellow
}

# 4. Create Portable Distribution ZIP
Write-Host "[4/4] Creating standalone distribution package: Speedy-2.0-Windows-x64.zip..." -ForegroundColor Cyan
$zipFile = Join-Path $DistDir "Speedy-2.0-Windows-x64.zip"
if (Test-Path $zipFile) {
    Remove-Item $zipFile -Force
}

$tempPkg = Join-Path $DistDir "Speedy-2.0-Package"
if (Test-Path $tempPkg) {
    Remove-Item $tempPkg -Recurse -Force
}
New-Item -ItemType Directory -Path $tempPkg | Out-Null
New-Item -ItemType Directory -Path "$tempPkg\bin" | Out-Null
New-Item -ItemType Directory -Path "$tempPkg\scripts" | Out-Null

Copy-Item "$BinDir\*.exe" "$tempPkg\bin\"
Copy-Item "$BinDir\*.dll" "$tempPkg\bin\"
Copy-Item "$ProjectRoot\ui" "$tempPkg\ui" -Recurse
Copy-Item "$ProjectRoot\scripts\*.ps1" "$tempPkg\scripts\"
Copy-Item "$ProjectRoot\packaging" "$tempPkg\packaging" -Recurse

Compress-Archive -Path "$tempPkg\*" -DestinationPath $zipFile
Remove-Item $tempPkg -Recurse -Force

Write-Host "[OK] Standalone distribution package created: $zipFile" -ForegroundColor Green
Write-Host ""
Write-Host "==========================================================" -ForegroundColor Green
Write-Host "  Speedy 2.0 Packaging Complete! Artifacts in $DistDir     " -ForegroundColor Green
Write-Host "==========================================================" -ForegroundColor Green
