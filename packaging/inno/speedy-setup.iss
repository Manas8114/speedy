; Speedy 2.0 Multi-WAN Bonding — Inno Setup Script
; Generates a professional Windows Setup Wizard with Service and Firewall registration

#define MyAppName "Speedy 2.0"
#define MyAppVersion "2.0.0"
#define MyAppPublisher "Speedy Open Source Project"
#define MyAppURL "https://github.com/Manas8114/speedy"
#define MyAppExeName "speedy-ui.exe"
#define MyClientExeName "speedy-client.exe"

[Setup]
AppId={{9B78D82E-C44B-4E7B-9A3E-7D4F99839A2C}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher={#MyAppPublisher}
AppPublisherURL={#MyAppURL}
AppSupportURL={#MyAppURL}
AppUpdatesURL={#MyAppURL}
DefaultDirName={autopf}\Speedy
DefaultGroupName={#MyAppName}
AllowNoIcons=yes
OutputDir=..\..\dist
OutputBaseFilename=Speedy-2.0-Setup-x64
Compression=lzma2/ultra64
SolidCompression=yes
WizardStyle=modern
PrivilegesRequired=admin
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: unchecked
Name: "startservice"; Description: "Register and start Speedy Bonding background daemon service"; GroupDescription: "System Services:"

[Files]
Source: "..\..\bin\speedy-client.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\..\bin\speedy-ui.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\..\bin\speedy-relay.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\..\bin\wintun.dll"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\..\bin\wintun.dll"; DestDir: "{sys}"; Flags: onlyifdoesntexist uninsneveruninstall
Source: "..\..\ui\*"; DestDir: "{app}\ui"; Flags: ignoreversion recursesubdirs createallsubdirs
Source: "..\..\scripts\install-service.ps1"; DestDir: "{app}\scripts"; Flags: ignoreversion
Source: "..\..\scripts\uninstall-service.ps1"; DestDir: "{app}\scripts"; Flags: ignoreversion

[Icons]
Name: "{group}\Speedy Dashboard"; Filename: "{app}\{#MyAppExeName}"; Comment: "Launch Speedy 2.0 Web Dashboard"
Name: "{group}\Uninstall Speedy 2.0"; Filename: "{uninstallexe}"
Name: "{autodesktop}\Speedy 2.0"; Filename: "{app}\{#MyAppExeName}"; Tasks: desktopicon

[Run]
; Install Windows Service
Filename: "{app}\{#MyClientExeName}"; Parameters: "--service install"; Flags: runhidden; Tasks: startservice
; Configure Windows Firewall UDP Rule
Filename: "powershell.exe"; Parameters: "-NoProfile -ExecutionPolicy Bypass -Command ""New-NetFirewallRule -Name 'SpeedyBondingUDP' -DisplayName 'Speedy 2.0 WAN Bonding UDP Traffic' -Direction Inbound -Protocol UDP -LocalPort 51820 -Action Allow -Profile Any"""; Flags: runhidden; Tasks: startservice
; Start the service
Filename: "{app}\{#MyClientExeName}"; Parameters: "--service start"; Flags: runhidden; Tasks: startservice
; Launch Dashboard option on finish
Filename: "{app}\{#MyAppExeName}"; Description: "{cm:LaunchProgram,{#StringChange(MyAppName, '&', '&&')}}"; Flags: nowait postinstall skipifsilent

[UninstallRun]
; Stop and remove service
Filename: "{app}\{#MyClientExeName}"; Parameters: "--service stop"; Flags: runhidden
Filename: "{app}\{#MyClientExeName}"; Parameters: "--service uninstall"; Flags: runhidden
; Remove firewall rule
Filename: "powershell.exe"; Parameters: "-NoProfile -ExecutionPolicy Bypass -Command ""Remove-NetFirewallRule -Name 'SpeedyBondingUDP' -ErrorAction SilentlyContinue"""; Flags: runhidden
