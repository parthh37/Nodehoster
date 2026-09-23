# Installs, upgrades, downgrades and uninstalls a built NodeHoster setup and
# checks the machine after each step: the service, its pipes and web console,
# the firewall rule, the PATH, the status icon's Run entry, the PowerShell
# module and the data.
#
#   powershell -File installer\test.ps1 -Setup dist\NodeHoster-1.2.3-setup.exe -Version 1.2.3
#
# It changes the machine (Program Files, the NodeHoster service, the system
# PATH, %ProgramData%\NodeHoster), so run it on a disposable Windows machine
# with no NodeHoster installed, from an elevated prompt. CI runs it on every
# build. Windows PowerShell 5.1 compatible.

param(
  [Parameter(Mandatory)] [string] $Setup,
  [Parameter(Mandatory)] [string] $Version
)

$ErrorActionPreference = "Stop"
$Setup = (Resolve-Path $Setup).Path
$App = Join-Path $env:ProgramFiles "NodeHoster"
$Data = Join-Path $env:ProgramData "NodeHoster"
$UninstallKey = "HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\{6F1B3C2A-9D4E-4E7B-A1C5-2B7D9E0F4A11}_is1"
$EnvKey = "HKLM:\SYSTEM\CurrentControlSet\Control\Session Manager\Environment"
$RunKey = "HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Run"
$ModuleDir = Join-Path $env:ProgramFiles "WindowsPowerShell\Modules\NodeHoster"

if (Get-Service NodeHoster -ErrorAction SilentlyContinue) { throw "NodeHoster is already installed on this machine" }

function Check($ok, $what) {
  if (-not $ok) { throw "FAILED: $what" }
  Write-Host "  ok  $what"
}

# Runs setup (or the uninstaller) unattended and returns its exit code,
# printing its log when the code is not the expected one.
function Invoke-Setup($exe, [string[]] $extra, [int] $expect) {
  $log = Join-Path $env:TEMP ("nodehoster-setup-" + [guid]::NewGuid().ToString("N") + ".log")
  $argList = @("/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/LOG=`"$log`"") + $extra
  $p = Start-Process -FilePath $exe -ArgumentList $argList -Wait -PassThru
  if ($p.ExitCode -ne $expect) {
    Get-Content $log -ErrorAction SilentlyContinue | Select-Object -Last 60
    throw "$(Split-Path $exe -Leaf) $($extra -join ' ') exited with $($p.ExitCode), expected $expect"
  }
}

function Invoke-Pipe($name, $path) {
  $pipe = New-Object System.IO.Pipes.NamedPipeClientStream(".", $name, [System.IO.Pipes.PipeDirection]::InOut)
  $pipe.Connect(10000)
  try {
    $w = New-Object System.IO.StreamWriter($pipe)
    $w.Write("GET $path HTTP/1.1`r`nHost: nodehoster`r`nConnection: close`r`n`r`n")
    $w.Flush()
    return (New-Object System.IO.StreamReader($pipe)).ReadToEnd()
  } finally { $pipe.Dispose() }
}

function SystemPath { (Get-ItemProperty $EnvKey).Path }
function PathEntries { @((SystemPath) -split ";" | Where-Object { $_.TrimEnd("\") -ieq $App }) }
function FirewallRule { netsh advfirewall firewall show rule name=NodeHoster | Out-Null; $LASTEXITCODE -eq 0 }
function RunValue { (Get-ItemProperty $RunKey -ErrorAction SilentlyContinue).NodeHosterStatus }
function ServicePid { (Get-CimInstance Win32_Service -Filter "Name='NodeHoster'").ProcessId }

# The module imports from the machine-wide module path, and its commands
# reach the service through nodehoster.exe and the admin pipe.
function Check-Module($what) {
  Check (@(Get-ChildItem $ModuleDir -Directory).Count -eq 1) "${what}: one version of the PowerShell module is installed"
  Import-Module NodeHoster -Force -ErrorAction Stop
  $m = Get-Module NodeHoster
  Check ($m.Version.ToString() -eq (Split-Path $m.ModuleBase -Leaf)) "${what}: the module's version ($($m.Version)) matches its folder"
  Check (@(Get-Command -Module NodeHoster).Count -eq 11) "${what}: the module exports its commands"
  $sites = @(Get-NHSite -ErrorAction Stop)
  Check ($sites.Count -eq 0) "${what}: Get-NHSite answers (no sites)"
  Check (@(Get-NHEvent -Count 5 -ErrorAction Stop).Count -ge 1) "${what}: Get-NHEvent answers"
  Remove-Module NodeHoster
}

# The service reports running before its listeners are up.
function Wait-Console {
  for ($i = 0; $i -lt 30; $i++) {
    $code = & curl.exe -sk -o NUL -w "%{http_code}" https://localhost:8484/api/auth/me
    if ($code -eq "401") { return $true }
    Start-Sleep -Seconds 1
  }
  return $false
}

function Check-Running($what) {
  $svc = Get-Service NodeHoster
  Check ($svc.Status -eq "Running") "${what}: the service is running"
  Check ($svc.StartType -eq "Automatic") "${what}: the service starts automatically"
  Check (Wait-Console) "${what}: the web console answers on https://localhost:8484"
  $who = Invoke-Pipe "NodeHoster.Admin" "/api/local/whoami"
  Check ($who -match "200 OK") "${what}: the admin pipe answers"
  $sum = Invoke-Pipe "NodeHoster.Status" "/status"
  Check ($sum -match '"sites":\[') "${what}: the status pipe answers"
  Check ((Get-ItemProperty $UninstallKey).DisplayVersion -eq $Version) "${what}: Programs and Features lists $Version"
}

Write-Host "Install"
Invoke-Setup $Setup @("/TASKS=`"addtopath,firewall,statusicon`"") 0
Check (Test-Path "$App\nodehoster.exe") "nodehoster.exe is installed"
Check (Test-Path "$App\nodehoster-manager.exe") "nodehoster-manager.exe is installed"
Check-Running "install"
Check (Test-Path "$Data\initial-admin-password.txt") "the initial administrator password was written"
Check ((PathEntries).Count -eq 1) "the program folder is on the system PATH"
Check (FirewallRule) "the firewall rule exists"
Check ((RunValue) -like "*nodehoster-manager.exe*--tray*") "the status icon starts at sign-in"
$ver = (Get-Item "$App\nodehoster.exe").VersionInfo
Check ($ver.ProductName -eq "NodeHoster") "nodehoster.exe has version information ($($ver.FileVersion))"
Check-Module "install"
& "$App\nodehoster.exe" site list | Out-Null
Check ($LASTEXITCODE -eq 0) "nodehoster site list talks to the service"
$password = Get-Content "$Data\initial-admin-password.txt" -Raw
$pid1 = ServicePid

Write-Host "Upgrade (reinstall over the top, without the status icon)"
Invoke-Setup $Setup @("/TASKS=`"addtopath,firewall`"") 0
Check-Running "upgrade"
Check ((ServicePid) -ne $pid1) "the service was restarted"
Check ((Get-Content "$Data\initial-admin-password.txt" -Raw) -eq $password) "the data was kept"
Check ((PathEntries).Count -eq 1) "the PATH entry was not duplicated"
Check (-not (RunValue)) "the unchecked status icon no longer starts at sign-in"
Check-Module "upgrade"

Write-Host "Downgrade"
# Pretend a newer version is installed.
Set-ItemProperty $UninstallKey DisplayVersion "999.0.0"
$pid2 = ServicePid
Invoke-Setup $Setup @() 7
Check ((Get-Service NodeHoster).Status -eq "Running" -and (ServicePid) -eq $pid2) "a refused downgrade leaves the service alone"
Invoke-Setup $Setup @("/ALLOWDOWNGRADE") 0
Check-Running "downgrade with /ALLOWDOWNGRADE"

Write-Host "Uninstall"
$uninstaller = Join-Path $App "unins000.exe"
Invoke-Setup $uninstaller @() 0
# The uninstaller runs from a temporary copy; wait for it to finish.
for ($i = 0; $i -lt 120 -and ((Test-Path $UninstallKey) -or (Test-Path "$App\nodehoster.exe")); $i++) { Start-Sleep -Seconds 1 }
Check (-not (Test-Path $UninstallKey)) "Programs and Features no longer lists NodeHoster"
Check (-not (Test-Path "$App\nodehoster.exe")) "the program files were removed"
Check (-not (Get-Service NodeHoster -ErrorAction SilentlyContinue)) "the service was removed"
Check (-not (FirewallRule)) "the firewall rule was removed"
Check ((PathEntries).Count -eq 0) "the program folder was removed from the PATH"
Check (-not (RunValue)) "the status icon no longer starts at sign-in"
Check (-not (Test-Path $ModuleDir)) "the PowerShell module was removed"
Check (Test-Path "$Data\nodehoster.db") "an unattended uninstall keeps the data"

Remove-Item -Recurse -Force $Data
Write-Host "Installer test passed"
# The last native command (netsh, finding no rule) left a failing exit code.
exit 0
