; NodeHoster installer (Inno Setup 6).
;
; Build:  ISCC.exe /DAppVersion=1.2.3 /DSourceDir=..\dist installer\nodehoster.iss
;
; Installs nodehoster.exe to Program Files, registers and starts the Windows
; service, and opens the firewall for the program (all ports it binds, so
; bindings added later need no new rules). Also installs NodeHoster Manager
; (nodehoster-manager.exe, the desktop console) and starts its status icon
; in the notification area at every sign-in unless that task is unchecked. Upgrades stop the service first
; and start it again afterwards. Data in %ProgramData%\NodeHoster is kept on
; uninstall unless the user chooses to remove it.
;
; Unattended install (for scripts and remote management):
;   NodeHoster-1.2.3-setup.exe /VERYSILENT /SUPPRESSMSGBOXES /NORESTART /TASKS="addtopath,firewall"
; Exit code 0 means installed and the service is running; 1 means installed
; but the service did not start (other codes are Inno Setup's own). Setup
; writes a log to %TEMP%\Setup Log *.txt.

#ifndef AppVersion
  #define AppVersion "0.0.0-dev"
#endif
#ifndef SourceDir
  #define SourceDir "..\dist"
#endif

[Setup]
AppId={{6F1B3C2A-9D4E-4E7B-A1C5-2B7D9E0F4A11}
AppName=NodeHoster
AppVersion={#AppVersion}
AppVerName=NodeHoster {#AppVersion}
AppPublisher=NodeHoster
AppPublisherURL=https://github.com/parthh37/Nodehoster
DefaultDirName={autopf}\NodeHoster
DisableProgramGroupPage=yes
OutputBaseFilename=NodeHoster-{#AppVersion}-setup
OutputDir={#SourceDir}
Compression=lzma2/ultra64
SolidCompression=yes
PrivilegesRequired=admin
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
WizardStyle=modern
ChangesEnvironment=yes
UninstallDisplayIcon={app}\nodehoster.exe
MinVersion=10.0.17763
CloseApplications=no
SetupLogging=yes

[Tasks]
Name: "addtopath"; Description: "Add nodehoster to the system PATH"; Flags: checkedonce
Name: "firewall"; Description: "Allow NodeHoster through Windows Firewall"; Flags: checkedonce
Name: "statusicon"; Description: "Show the NodeHoster status icon in the notification area at sign-in (all users)"; Flags: checkedonce

[Files]
Source: "{#SourceDir}\nodehoster.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\nodehoster-manager.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\README.md"; DestDir: "{app}"; Flags: ignoreversion isreadme

[Icons]
Name: "{autoprograms}\NodeHoster Manager"; Filename: "{app}\nodehoster-manager.exe"; Comment: "Manage NodeHoster sites and the service"
Name: "{autoprograms}\NodeHoster Status"; Filename: "{app}\nodehoster-manager.exe"; Parameters: "--tray"; Comment: "Show the NodeHoster status icon in the notification area"
Name: "{autoprograms}\NodeHoster Console"; Filename: "https://localhost:8484/"

[Run]
; Always run: on upgrades it re-applies the start type and the automatic
; restart-on-failure settings, keeping the service's data folder.
Filename: "{app}\nodehoster.exe"; Parameters: "service install"; StatusMsg: "Registering the NodeHoster service..."; Flags: runhidden waituntilterminated
Filename: "{sys}\netsh.exe"; Parameters: "advfirewall firewall delete rule name=""NodeHoster"""; Flags: runhidden waituntilterminated; Tasks: firewall
Filename: "{sys}\netsh.exe"; Parameters: "advfirewall firewall add rule name=""NodeHoster"" dir=in action=allow program=""{app}\nodehoster.exe"" enable=yes profile=any"; StatusMsg: "Configuring Windows Firewall..."; Flags: runhidden waituntilterminated; Tasks: firewall
Filename: "{app}\nodehoster.exe"; Parameters: "service start"; StatusMsg: "Starting NodeHoster..."; Flags: runhidden waituntilterminated; AfterInstall: VerifyServiceRunning
; The status icon runs unelevated, as the user who started setup.
Filename: "{app}\nodehoster-manager.exe"; Parameters: "--tray"; Flags: nowait runasoriginaluser skipifsilent; Tasks: statusicon
Filename: "{app}\nodehoster-manager.exe"; Description: "Open NodeHoster Manager"; Flags: postinstall nowait skipifsilent unchecked
Filename: "https://localhost:8484/"; Description: "Open the NodeHoster web console"; Flags: postinstall shellexec nowait skipifsilent unchecked

[UninstallRun]
Filename: "{sys}\taskkill.exe"; Parameters: "/F /IM nodehoster-manager.exe"; Flags: runhidden waituntilterminated; RunOnceId: "CloseManager"
Filename: "{app}\nodehoster.exe"; Parameters: "service uninstall"; Flags: runhidden waituntilterminated; RunOnceId: "RemoveService"
Filename: "{sys}\netsh.exe"; Parameters: "advfirewall firewall delete rule name=""NodeHoster"""; Flags: runhidden waituntilterminated; RunOnceId: "RemoveFirewall"

[Registry]
; --autostart lets each user switch the icon off for themselves (from its
; menu) without affecting other users.
Root: HKLM; Subkey: "SOFTWARE\Microsoft\Windows\CurrentVersion\Run"; ValueType: string; ValueName: "NodeHosterStatus"; ValueData: """{app}\nodehoster-manager.exe"" --tray --autostart"; Flags: uninsdeletevalue; Tasks: statusicon
Root: HKLM; Subkey: "SYSTEM\CurrentControlSet\Control\Session Manager\Environment"; ValueType: expandsz; ValueName: "Path"; ValueData: "{olddata};{app}"; Tasks: addtopath; Check: NeedsAddPath(ExpandConstant('{app}'))

[Code]
function ServiceExists: Boolean;
var
  ResultCode: Integer;
begin
  Result := Exec(ExpandConstant('{sys}\sc.exe'), 'query NodeHoster', '', SW_HIDE, ewWaitUntilTerminated, ResultCode) and (ResultCode = 0);
end;

var
  ServiceStartFailed: Boolean;

function ServiceRunning: Boolean;
var
  ResultCode: Integer;
begin
  // find exits with 0 only when the state line says RUNNING.
  Result := Exec(ExpandConstant('{cmd}'), '/C sc.exe query NodeHoster | find "RUNNING"', '', SW_HIDE, ewWaitUntilTerminated, ResultCode) and (ResultCode = 0);
end;

// A service that installs but does not run is a failed installation: say so
// (and fail silent installs with an exit code) instead of finishing quietly.
procedure VerifyServiceRunning;
var
  I: Integer;
begin
  for I := 1 to 10 do
  begin
    if ServiceRunning then
    begin
      Log('NodeHoster service is running.');
      exit;
    end;
    Sleep(1000);
  end;
  ServiceStartFailed := True;
  Log('NodeHoster service did not start.');
end;

// Exit code for unattended installs: 1 = installed, but the service is not running.
function GetCustomSetupExitCode: Integer;
begin
  if ServiceStartFailed then
    Result := 1
  else
    Result := 0;
end;

function NeedsAddPath(Dir: string): Boolean;
var
  Paths: string;
begin
  if not RegQueryStringValue(HKEY_LOCAL_MACHINE, 'SYSTEM\CurrentControlSet\Control\Session Manager\Environment', 'Path', Paths) then
  begin
    Result := True;
    exit;
  end;
  Result := Pos(';' + Uppercase(Dir) + ';', ';' + Uppercase(Paths) + ';') = 0;
end;

// Stop the running service before files are replaced on upgrade. Stopping
// drains connections and shuts down hosted applications gracefully. Status
// icons and open managers (in every session) hold nodehoster-manager.exe
// open, so they are closed too; setup starts the icon again afterwards.
function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  ResultCode: Integer;
begin
  Result := '';
  if FileExists(ExpandConstant('{app}\nodehoster-manager.exe')) then
    Exec(ExpandConstant('{sys}\taskkill.exe'), '/F /IM nodehoster-manager.exe', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  if ServiceExists and FileExists(ExpandConstant('{app}\nodehoster.exe')) then
    Exec(ExpandConstant('{app}\nodehoster.exe'), 'service stop', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  DataDir: string;
begin
  if CurUninstallStep = usPostUninstall then
  begin
    DataDir := ExpandConstant('{commonappdata}\NodeHoster');
    if DirExists(DataDir) and not UninstallSilent then
      if MsgBox('Remove all NodeHoster data (sites, certificates, logs and deployed applications) in ' + DataDir + '?',
                mbConfirmation, MB_YESNO or MB_DEFBUTTON2) = IDYES then
        DelTree(DataDir, True, True, True);
  end;
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  PwFile: string;
begin
  if (CurStep = ssDone) and ServiceStartFailed then
  begin
    SuppressibleMsgBox('NodeHoster was installed, but the NodeHoster service did not start.' + #13#10#13#10 +
      'See ' + ExpandConstant('{commonappdata}\NodeHoster\logs\nodehoster.log') + ' and the Windows Event Viewer (System log), ' +
      'fix the problem, then run: nodehoster service start', mbError, MB_OK, IDOK);
    exit;
  end;
  if (CurStep = ssDone) and not WizardSilent then
  begin
    PwFile := ExpandConstant('{commonappdata}\NodeHoster\initial-admin-password.txt');
    Sleep(1500);
    if FileExists(PwFile) then
      MsgBox('NodeHoster is running.' + #13#10#13#10 +
             'Manage it from NodeHoster Manager (Start menu), or the web console.' + #13#10 +
             'Console: https://localhost:8484' + #13#10 +
             'The initial administrator password is in:' + #13#10 + PwFile + #13#10#13#10 +
             'You will be asked to change it at first sign-in.', mbInformation, MB_OK);
  end;
end;
