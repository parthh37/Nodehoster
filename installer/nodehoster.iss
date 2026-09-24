; NodeHoster installer (Inno Setup 6).
;
; Build:  ISCC.exe /DAppVersion=1.2.3 /DWinVersion=1.2.3.0 /DSourceDir=..\dist installer\nodehoster.iss
;
; Installs nodehoster.exe to Program Files, registers and starts the Windows
; service, and opens the firewall for the program (all ports it binds, so
; bindings added later need no new rules). Also installs NodeHoster Manager
; (nodehoster-manager.exe, the desktop console) and starts its status icon
; in the notification area at every sign-in unless that task is unchecked,
; and the NodeHoster PowerShell module (Get-NHSite, Publish-NHSite...) for
; every user, in {commonpf64}\WindowsPowerShell\Modules.
; Upgrades stop the service first and start it again afterwards; installing
; an older version over a newer one asks first. Uninstalling removes the
; service, the firewall rule, the PATH entry, the status icon and the
; PowerShell module; data in
; %ProgramData%\NodeHoster is kept unless the user chooses to remove it.
;
; Unattended install (for scripts and remote management):
;   NodeHoster-1.2.3-setup.exe /VERYSILENT /SUPPRESSMSGBOXES /NORESTART /TASKS="addtopath,firewall"
; Exit codes: 0 installed and the service is running; 1 installed, but the
; service did not start; 7 not installed, because a newer version is
; installed (add /ALLOWDOWNGRADE to install it anyway). Other codes are Inno
; Setup's own. Setup writes a log to %TEMP%\Setup Log *.txt.
; Unattended uninstall (keeps the data):
;   "C:\Program Files\NodeHoster\unins000.exe" /VERYSILENT /SUPPRESSMSGBOXES /NORESTART
;
; installer\test.ps1 installs, upgrades, downgrades and uninstalls the built
; setup and checks the result; CI runs it on every build.

#ifndef AppVersion
  #define AppVersion "0.0.0-dev"
#endif
; The four-part file version of setup.exe (X.Y.Z.0 for releases).
#ifndef WinVersion
  #define WinVersion "0.0.0.0"
#endif
#ifndef SourceDir
  #define SourceDir "..\dist"
#endif
#define AppGuid "{6F1B3C2A-9D4E-4E7B-A1C5-2B7D9E0F4A11}"
; The PowerShell module's version (X.Y.Z of WinVersion): its folder is
; named after it, and PowerShell requires the manifest to say the same.
#define ModuleVersion Copy(WinVersion, 1, RPos(".", WinVersion) - 1)
#define ModuleDir "{commonpf64}\WindowsPowerShell\Modules\NodeHoster"

[Setup]
AppId={#StringChange(AppGuid, "{", "{{")}
AppName=NodeHoster
AppVersion={#AppVersion}
AppVerName=NodeHoster {#AppVersion}
AppPublisher=NodeHoster
AppPublisherURL=https://github.com/parthh37/Nodehoster
AppSupportURL=https://github.com/parthh37/Nodehoster/issues
AppUpdatesURL=https://github.com/parthh37/Nodehoster/releases
VersionInfoVersion={#WinVersion}
VersionInfoProductVersion={#WinVersion}
VersionInfoProductTextVersion={#AppVersion}
VersionInfoDescription=NodeHoster Setup
VersionInfoCompany=NodeHoster
VersionInfoProductName=NodeHoster
SetupIconFile=nodehoster.ico
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
; The module sits in a machine-wide module folder so that every session
; finds it (Import-Module NodeHoster); it runs {app}\nodehoster.exe.
Source: "NodeHoster\NodeHoster.psm1"; DestDir: "{#ModuleDir}\{#ModuleVersion}"; Flags: ignoreversion
Source: "NodeHoster\NodeHoster.psd1"; DestDir: "{#ModuleDir}\{#ModuleVersion}"; Flags: ignoreversion; AfterInstall: StampModuleVersion

[InstallDelete]
; An upgrade replaces the previous version's module folder instead of
; leaving it next to the new one.
Type: filesandordirs; Name: "{#ModuleDir}"

[UninstallDelete]
Type: dirifempty; Name: "{#ModuleDir}"

[Icons]
Name: "{autoprograms}\NodeHoster Manager"; Filename: "{app}\nodehoster-manager.exe"; Comment: "Manage NodeHoster sites and the service"
Name: "{autoprograms}\NodeHoster Status"; Filename: "{app}\nodehoster-manager.exe"; Parameters: "--tray"; Comment: "Show the NodeHoster status icon in the notification area"
Name: "{autoprograms}\NodeHoster Console"; Filename: "https://localhost:8484/"

[Run]
; Always run: on upgrades it re-applies the start type and the automatic
; restart-on-failure settings, keeping the service's data folder.
Filename: "{app}\nodehoster.exe"; Parameters: "service install"; StatusMsg: "Registering the NodeHoster service..."; Flags: runhidden waituntilterminated
; The old rule goes even when the task is now unchecked.
Filename: "{sys}\netsh.exe"; Parameters: "advfirewall firewall delete rule name=""NodeHoster"""; Flags: runhidden waituntilterminated
Filename: "{sys}\netsh.exe"; Parameters: "advfirewall firewall add rule name=""NodeHoster"" dir=in action=allow program=""{app}\nodehoster.exe"" enable=yes profile=any"; StatusMsg: "Configuring Windows Firewall..."; Flags: runhidden waituntilterminated; Tasks: firewall
Filename: "{app}\nodehoster.exe"; Parameters: "service start"; StatusMsg: "Starting NodeHoster..."; Flags: runhidden waituntilterminated; AfterInstall: VerifyServiceRunning
; The status icon runs unelevated, as the user who started setup. Upgrades
; close every user's icon (the program file must be replaced); other users
; get theirs back at their next sign-in.
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
const
  EnvironmentKey = 'SYSTEM\CurrentControlSet\Control\Session Manager\Environment';
  RunKey = 'SOFTWARE\Microsoft\Windows\CurrentVersion\Run';
  UninstallKey = 'Software\Microsoft\Windows\CurrentVersion\Uninstall\{#AppGuid}_is1';

var
  // The version installed before this setup ran ('' on a fresh install).
  InstalledVersion: string;
  Downgrade: Boolean;
  ServiceStartFailed: Boolean;

function ServiceExists: Boolean;
var
  ResultCode: Integer;
begin
  Result := Exec(ExpandConstant('{sys}\sc.exe'), 'query NodeHoster', '', SW_HIDE, ewWaitUntilTerminated, ResultCode) and (ResultCode = 0);
end;

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

// The module manifest in the repository says 0.0.0; PowerShell only loads a
// module from a version folder whose manifest has the same version.
procedure StampModuleVersion;
var
  Lines: TArrayOfString;
  I: Integer;
  FileName: string;
begin
  FileName := ExpandConstant(CurrentFileName);
  if not LoadStringsFromFile(FileName, Lines) then
    exit;
  for I := 0 to GetArrayLength(Lines) - 1 do
    if Pos('ModuleVersion', TrimLeft(Lines[I])) = 1 then
      Lines[I] := '  ModuleVersion = ''{#ModuleVersion}''';
  if not SaveStringsToFile(FileName, Lines, False) then
    Log('Could not set the version of ' + FileName);
end;

// --- Versions -------------------------------------------------------------

// NextNumber reads the decimal number at the start of S and removes it and
// one following '.'; it returns -1 when S does not start with a digit.
function NextNumber(var S: string): Integer;
var
  I: Integer;
begin
  // No S[I] in a condition that also tests I <= Length(S): it would be
  // out of range if the evaluation did not stop early.
  I := 1;
  while I <= Length(S) do
  begin
    if (S[I] < '0') or (S[I] > '9') then
      break;
    I := I + 1;
  end;
  if I = 1 then
  begin
    Result := -1;
    exit;
  end;
  Result := StrToIntDef(Copy(S, 1, I - 1), -1);
  if I <= Length(S) then
    if S[I] = '.' then
      I := I + 1;
  Delete(S, 1, I - 1);
end;

// CompareVersions compares the X.Y.Z at the start of two versions: <0, 0 or
// >0. Suffixes (0.0.0-dev.12, 1.2.0-rc1) are ignored, so development builds
// and release candidates never count as a downgrade from their release.
function CompareVersions(A, B: string): Integer;
var
  I, X, Y: Integer;
begin
  Result := 0;
  for I := 1 to 3 do
  begin
    X := NextNumber(A);
    Y := NextNumber(B);
    if X <> Y then
    begin
      Result := X - Y;
      exit;
    end;
  end;
end;

function HasSwitch(Name: string): Boolean;
var
  I: Integer;
begin
  Result := False;
  for I := 1 to ParamCount do
    if CompareText(ParamStr(I), Name) = 0 then
      Result := True;
end;

// An older version may not read the data a newer one has migrated, so
// installing over a newer version needs a decision: interactive setups ask
// (defaulting to No), unattended ones refuse unless /ALLOWDOWNGRADE is given.
function InitializeSetup: Boolean;
begin
  Result := True;
  if not RegQueryStringValue(HKEY_LOCAL_MACHINE, UninstallKey, 'DisplayVersion', InstalledVersion) then
    InstalledVersion := '';
  Downgrade := (InstalledVersion <> '') and (CompareVersions(InstalledVersion, '{#AppVersion}') > 0);
  if Downgrade and not WizardSilent and not HasSwitch('/ALLOWDOWNGRADE') then
    Result := MsgBox('NodeHoster ' + InstalledVersion + ' is installed, which is newer than this version ({#AppVersion}).' + #13#10#13#10 +
                     'An older version may not be able to read the configuration and data of a newer one. ' +
                     'Back up the configuration first (NodeHoster Manager: Server > Back up).' + #13#10#13#10 +
                     'Install NodeHoster {#AppVersion} anyway?', mbConfirmation, MB_YESNO or MB_DEFBUTTON2) = IDYES;
end;

// --- PATH -----------------------------------------------------------------

function SamePath(A, B: string): Boolean;
begin
  Result := CompareText(RemoveBackslashUnlessRoot(Trim(A)), RemoveBackslashUnlessRoot(Trim(B))) = 0;
end;

function NeedsAddPath(Dir: string): Boolean;
var
  Paths, Entry: string;
  P: Integer;
begin
  Result := True;
  if not RegQueryStringValue(HKEY_LOCAL_MACHINE, EnvironmentKey, 'Path', Paths) then
    exit;
  Paths := Paths + ';';
  while Paths <> '' do
  begin
    P := Pos(';', Paths);
    Entry := Copy(Paths, 1, P - 1);
    Delete(Paths, 1, P);
    if SamePath(Entry, Dir) then
    begin
      Result := False;
      exit;
    end;
  end;
end;

// RemovePath takes Dir out of the system PATH, keeping every other entry
// (and their order and %VARIABLES%) as they were.
procedure RemovePath(Dir: string);
var
  Paths, Entry, Kept: string;
  P: Integer;
  Changed: Boolean;
begin
  if not RegQueryStringValue(HKEY_LOCAL_MACHINE, EnvironmentKey, 'Path', Paths) then
    exit;
  Paths := Paths + ';';
  Kept := '';
  Changed := False;
  while Paths <> '' do
  begin
    P := Pos(';', Paths);
    Entry := Copy(Paths, 1, P - 1);
    Delete(Paths, 1, P);
    if SamePath(Entry, Dir) then
      Changed := True
    else if Entry <> '' then
    begin
      if Kept <> '' then
        Kept := Kept + ';';
      Kept := Kept + Entry;
    end;
  end;
  if Changed then
  begin
    if RegWriteExpandStringValue(HKEY_LOCAL_MACHINE, EnvironmentKey, 'Path', Kept) then
      Log('Removed ' + Dir + ' from the system PATH.')
    else
      Log('Could not remove ' + Dir + ' from the system PATH.');
  end;
end;

// --- Install --------------------------------------------------------------

// Stop the running service before files are replaced on upgrade. Stopping
// drains connections and shuts down hosted applications gracefully. Status
// icons and open managers (in every session) hold nodehoster-manager.exe
// open, so they are closed too; setup starts the icon again afterwards.
// An unattended downgrade is refused here, before anything is stopped
// (exit code 7).
function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  ResultCode: Integer;
begin
  Result := '';
  if Downgrade and WizardSilent and not HasSwitch('/ALLOWDOWNGRADE') then
  begin
    Result := 'NodeHoster ' + InstalledVersion + ' is installed, which is newer than {#AppVersion}. ' +
              'Run setup with /ALLOWDOWNGRADE to install the older version anyway.';
    Log(Result);
    exit;
  end;
  if FileExists(ExpandConstant('{app}\nodehoster-manager.exe')) then
    Exec(ExpandConstant('{sys}\taskkill.exe'), '/F /IM nodehoster-manager.exe', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  if ServiceExists and FileExists(ExpandConstant('{app}\nodehoster.exe')) then
    Exec(ExpandConstant('{app}\nodehoster.exe'), 'service stop', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  PwFile: string;
  I: Integer;
begin
  // [Registry] only writes values for selected tasks; one unchecked on an
  // upgrade must also undo what an earlier install did.
  if CurStep = ssPostInstall then
  begin
    if not WizardIsTaskSelected('statusicon') then
      RegDeleteValue(HKEY_LOCAL_MACHINE, RunKey, 'NodeHosterStatus');
    if not WizardIsTaskSelected('addtopath') then
      RemovePath(ExpandConstant('{app}'));
  end;

  if CurStep <> ssDone then
    exit;
  if ServiceStartFailed then
  begin
    SuppressibleMsgBox('NodeHoster was installed, but the NodeHoster service did not start.' + #13#10#13#10 +
      'See ' + ExpandConstant('{commonappdata}\NodeHoster\logs\nodehoster.log') + ' and the Windows Event Viewer (System log), ' +
      'fix the problem, then run: nodehoster service start', mbError, MB_OK, IDOK);
    exit;
  end;
  // Only a first installation has a new administrator password to show; on
  // an upgrade the file, if still there, is stale.
  if WizardSilent or (InstalledVersion <> '') then
    exit;
  // The service reports running before it has created the administrator.
  PwFile := ExpandConstant('{commonappdata}\NodeHoster\initial-admin-password.txt');
  for I := 1 to 20 do
  begin
    if FileExists(PwFile) then
      break;
    Sleep(500);
  end;
  if FileExists(PwFile) then
    MsgBox('NodeHoster is running.' + #13#10#13#10 +
           'Manage it from NodeHoster Manager (Start menu), or the web console.' + #13#10 +
           'Console: https://localhost:8484' + #13#10 +
           'The initial administrator password is in:' + #13#10 + PwFile + #13#10#13#10 +
           'You will be asked to change it at first sign-in.', mbInformation, MB_OK);
end;

// --- Uninstall ------------------------------------------------------------

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  DataDir: string;
begin
  // The PATH entry is removed whether or not this installation added it:
  // the folder it names is about to be deleted.
  if CurUninstallStep = usUninstall then
    RemovePath(ExpandConstant('{app}'));

  if CurUninstallStep = usPostUninstall then
  begin
    DataDir := ExpandConstant('{commonappdata}\NodeHoster');
    if DirExists(DataDir) and not UninstallSilent then
      if MsgBox('Remove all NodeHoster data (sites, certificates, logs and deployed applications) in ' + DataDir + '?',
                mbConfirmation, MB_YESNO or MB_DEFBUTTON2) = IDYES then
        DelTree(DataDir, True, True, True);
  end;
end;
