# NodeHoster PowerShell module: the nodehoster.exe management commands as
# PowerShell commands that return objects. Each one runs
# `nodehoster.exe --json ...`, which talks to the NodeHoster service on
# this computer over its local admin pipe (\\.\pipe\NodeHoster.Admin); the
# pipe only admits elevated Administrators, so run these from an elevated
# session, as you would IIS's WebAdministration module.
#
# Windows PowerShell 5.1 compatible (and PowerShell 7).

$script:UninstallKey = 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\{6F1B3C2A-9D4E-4E7B-A1C5-2B7D9E0F4A11}_is1'

# nodehoster.exe: $env:NODEHOSTER_EXE, else where setup installed it, else
# the PATH.
function Get-NHExecutable {
  if ($env:NODEHOSTER_EXE -and (Test-Path -LiteralPath $env:NODEHOSTER_EXE)) { return $env:NODEHOSTER_EXE }
  $key = Get-ItemProperty -LiteralPath $script:UninstallKey -ErrorAction SilentlyContinue
  if ($key -and $key.PSObject.Properties['InstallLocation']) {
    $exe = Join-Path $key.InstallLocation 'nodehoster.exe'
    if (Test-Path -LiteralPath $exe) { return $exe }
  }
  $cmd = Get-Command nodehoster.exe -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
  if ($cmd) { return $cmd.Path }
  throw 'nodehoster.exe was not found. Install NodeHoster, or set $env:NODEHOSTER_EXE to its path.'
}

# Quotes arguments for a Windows command line the way programs (and Go's
# os.Args) split it back: backslashes before a quote are doubled.
function ConvertTo-NHCommandLine([string[]] $Arguments) {
  $quoted = foreach ($a in $Arguments) {
    if ($a -eq '') { '""' }
    elseif ($a -notmatch '[\s"]') { $a }
    else {
      $s = $a -replace '(\\*)"', '$1$1\"'
      $s = $s -replace '(\\+)$', '$1$1'
      '"' + $s + '"'
    }
  }
  $quoted -join ' '
}

# Runs nodehoster.exe --json with the arguments and returns the parsed
# JSON, a JSON array as its items (Windows PowerShell 5.1's ConvertFrom-Json
# would return it as one object). A failure (exit code 1 or 2) throws the
# command's error message.
function Invoke-NHCli([string[]] $Arguments) {
  $psi = New-Object System.Diagnostics.ProcessStartInfo
  $psi.FileName = Get-NHExecutable
  $psi.Arguments = ConvertTo-NHCommandLine (@('--json') + $Arguments)
  $psi.UseShellExecute = $false
  $psi.CreateNoWindow = $true
  $psi.RedirectStandardOutput = $true
  $psi.RedirectStandardError = $true
  $psi.StandardOutputEncoding = [System.Text.Encoding]::UTF8
  $psi.StandardErrorEncoding = [System.Text.Encoding]::UTF8
  $p = [System.Diagnostics.Process]::Start($psi)
  $stderr = $p.StandardError.ReadToEndAsync()
  $out = $p.StandardOutput.ReadToEnd()
  $p.WaitForExit()
  if ($p.ExitCode -ne 0) {
    $msg = (($stderr.Result -split "`r?`n") | Where-Object { $_ } | Select-Object -First 1) -replace '^error:\s*', ''
    if (-not $msg) { $msg = "nodehoster.exe exited with code $($p.ExitCode)" }
    throw $msg
  }
  if ($out.Trim()) {
    $value = ConvertFrom-Json -InputObject $out
    $value
  }
}

# Runs nodehoster.exe --json and passes its output lines on as they come,
# for the commands that follow something (a deployment, a log). What it
# writes to stderr (the deployment log, errors) goes straight to the host.
function Invoke-NHCliLive([string[]] $Arguments) {
  $exe = Get-NHExecutable
  $previous = $null
  try { $previous = [Console]::OutputEncoding; [Console]::OutputEncoding = [System.Text.Encoding]::UTF8 } catch { }
  try {
    & $exe --json @Arguments
    if ($LASTEXITCODE -ne 0) { throw "nodehoster $($Arguments[0]) failed (exit code $LASTEXITCODE); see the messages above." }
  } finally {
    if ($previous) { try { [Console]::OutputEncoding = $previous } catch { } }
  }
}

function Add-NHType($Object, [string] $TypeName) {
  foreach ($o in $Object) {
    if ($null -ne $o) {
      $o.PSObject.TypeNames.Insert(0, $TypeName)
      $o
    }
  }
}

function ConvertTo-NHSite($Site) {
  foreach ($s in $Site) {
    Add-Member -InputObject $s -NotePropertyName State -NotePropertyValue $s.status.state -Force
    Add-NHType $s 'NodeHoster.Site'
  }
}

function ConvertTo-NHLogLine($Line, [string] $Site) {
  foreach ($l in $Line) {
    [pscustomobject]@{
      PSTypeName = 'NodeHoster.LogLine'
      Site       = $Site
      Time       = [datetime] $l.t
      Stream     = $l.s
      Instance   = $l.i
      Text       = $l.m
    }
  }
}

Update-TypeData -TypeName NodeHoster.Site -DefaultDisplayPropertySet Name, Type, State, AutoStart, Id -Force
Update-TypeData -TypeName NodeHoster.Deployment -DefaultDisplayPropertySet Id, Source, Status, StartedAt, User, Message -Force
Update-TypeData -TypeName NodeHoster.Event -DefaultDisplayPropertySet Time, Level, Type, SiteId, Message -Force
Update-TypeData -TypeName NodeHoster.Certificate -DefaultDisplayPropertySet Name, Domains, Status, NotAfter, AutoRenew, Id -Force

<#
.SYNOPSIS
Gets NodeHoster sites and their state.
.DESCRIPTION
Without -Name, lists every site. With names (or IDs), gets those sites,
with their configuration and live status (status.instances, status.traffic).
.PARAMETER Name
Site names (case-insensitive) or IDs. Accepts pipeline input.
.EXAMPLE
Get-NHSite
.EXAMPLE
Get-NHSite shop | Select-Object -ExpandProperty bindings
.EXAMPLE
Get-NHSite | Where-Object State -eq 'failed' | Start-NHSite
#>
function Get-NHSite {
  [CmdletBinding()]
  param(
    [Parameter(Position = 0, ValueFromPipeline, ValueFromPipelineByPropertyName)]
    [Alias('SiteName')]
    [string[]] $Name
  )
  process {
    if (-not $Name) {
      ConvertTo-NHSite (Invoke-NHCli @('site', 'list'))
      return
    }
    foreach ($n in $Name) {
      try { ConvertTo-NHSite (Invoke-NHCli @('site', 'show', $n)) }
      catch { $PSCmdlet.WriteError($_) }
    }
  }
}

function Invoke-NHSiteAction([string] $Action, [string[]] $Name, [bool] $PassThru, $Cmdlet) {
  foreach ($n in $Name) {
    if (-not $Cmdlet.ShouldProcess($n, "$Action site")) { continue }
    try {
      $status = Invoke-NHCli @('site', $Action, $n)
      if ($PassThru) { Add-NHType $status 'NodeHoster.SiteStatus' }
    } catch { $Cmdlet.WriteError($_) }
  }
}

<#
.SYNOPSIS
Starts NodeHoster sites.
.PARAMETER Name
Site names or IDs. Accepts pipeline input, including sites from Get-NHSite.
.PARAMETER PassThru
Returns the site's status afterwards.
.EXAMPLE
Start-NHSite shop
.EXAMPLE
Get-NHSite | Where-Object State -eq 'stopped' | Start-NHSite -PassThru
#>
function Start-NHSite {
  [CmdletBinding(SupportsShouldProcess)]
  param(
    [Parameter(Mandatory, Position = 0, ValueFromPipeline, ValueFromPipelineByPropertyName)]
    [Alias('SiteName')]
    [string[]] $Name,
    [switch] $PassThru
  )
  process { Invoke-NHSiteAction 'start' $Name $PassThru.IsPresent $PSCmdlet }
}

<#
.SYNOPSIS
Stops NodeHoster sites.
.DESCRIPTION
Stops the site's Node.js processes (gracefully, then forcefully) and its
bindings stop answering.
.PARAMETER Name
Site names or IDs. Accepts pipeline input.
.PARAMETER PassThru
Returns the site's status afterwards.
.EXAMPLE
Stop-NHSite shop -Confirm:$false
#>
function Stop-NHSite {
  [CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'Medium')]
  param(
    [Parameter(Mandatory, Position = 0, ValueFromPipeline, ValueFromPipelineByPropertyName)]
    [Alias('SiteName')]
    [string[]] $Name,
    [switch] $PassThru
  )
  process { Invoke-NHSiteAction 'stop' $Name $PassThru.IsPresent $PSCmdlet }
}

<#
.SYNOPSIS
Restarts NodeHoster sites, or recycles them without downtime.
.DESCRIPTION
Restart stops the site's processes, then starts them. With -Recycle, new
instances start first and take over the traffic before the old ones drain
and stop, like recycling an IIS application pool.
.PARAMETER Name
Site names or IDs. Accepts pipeline input.
.PARAMETER Recycle
Recycles without downtime instead of stopping first.
.PARAMETER PassThru
Returns the site's status afterwards.
.EXAMPLE
Restart-NHSite shop -Recycle
#>
function Restart-NHSite {
  [CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'Medium')]
  param(
    [Parameter(Mandatory, Position = 0, ValueFromPipeline, ValueFromPipelineByPropertyName)]
    [Alias('SiteName')]
    [string[]] $Name,
    [switch] $Recycle,
    [switch] $PassThru
  )
  process {
    $action = 'restart'
    if ($Recycle) { $action = 'recycle' }
    Invoke-NHSiteAction $action $Name $PassThru.IsPresent $PSCmdlet
  }
}

<#
.SYNOPSIS
Recycles NodeHoster sites without downtime (Restart-NHSite -Recycle).
.PARAMETER Name
Site names or IDs. Accepts pipeline input.
.PARAMETER PassThru
Returns the site's status afterwards.
.EXAMPLE
Get-NHSite | Invoke-NHRecycle
#>
function Invoke-NHRecycle {
  [CmdletBinding(SupportsShouldProcess)]
  param(
    [Parameter(Mandatory, Position = 0, ValueFromPipeline, ValueFromPipelineByPropertyName)]
    [Alias('SiteName')]
    [string[]] $Name,
    [switch] $PassThru
  )
  process { Invoke-NHSiteAction 'recycle' $Name $PassThru.IsPresent $PSCmdlet }
}

<#
.SYNOPSIS
Deploys a release to a NodeHoster site from a .zip file or its git repository.
.DESCRIPTION
Uploads the archive (or has the service fetch the site's git repository),
shows the deployment log as it runs, and returns the finished deployment.
A failed deployment is an error; the site keeps its active release.
.PARAMETER Name
The site's name or ID.
.PARAMETER ZipPath
The .zip archive to deploy.
.PARAMETER Git
Deploys from the git repository configured on the site.
.PARAMETER Branch
The git branch (default: the site's).
.EXAMPLE
Publish-NHSite shop -ZipPath .\build\shop.zip
.EXAMPLE
Publish-NHSite shop -Git -Branch release
#>
function Publish-NHSite {
  [CmdletBinding(SupportsShouldProcess, DefaultParameterSetName = 'Zip')]
  param(
    [Parameter(Mandatory, Position = 0, ValueFromPipelineByPropertyName)]
    [Alias('SiteName')]
    [string] $Name,
    [Parameter(Mandatory, Position = 1, ParameterSetName = 'Zip')]
    [Alias('Path')]
    [string] $ZipPath,
    [Parameter(Mandatory, ParameterSetName = 'Git')]
    [switch] $Git,
    [Parameter(ParameterSetName = 'Git')]
    [string] $Branch
  )
  process {
    $cliArgs = @('deploy', $Name)
    if ($PSCmdlet.ParameterSetName -eq 'Zip') {
      $zip = (Resolve-Path -LiteralPath $ZipPath -ErrorAction Stop).ProviderPath
      $cliArgs += @('--zip', $zip)
      $what = "deploy $zip"
    } else {
      $cliArgs += '--git'
      if ($Branch) { $cliArgs += @('--branch', $Branch) }
      $what = 'deploy from git'
    }
    if (-not $PSCmdlet.ShouldProcess($Name, $what)) { return }
    $lines = @(Invoke-NHCliLive $cliArgs)
    Add-NHType (ConvertFrom-Json -InputObject ($lines -join "`n")) 'NodeHoster.Deployment'
  }
}

<#
.SYNOPSIS
Gets a NodeHoster site's deployments (releases), newest first.
.PARAMETER Name
The site's name or ID. Accepts pipeline input.
.EXAMPLE
Get-NHRelease shop | Where-Object Status -eq 'failed'
#>
function Get-NHRelease {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory, Position = 0, ValueFromPipeline, ValueFromPipelineByPropertyName)]
    [Alias('SiteName')]
    [string[]] $Name
  )
  process {
    foreach ($n in $Name) {
      try { Add-NHType (Invoke-NHCli @('releases', $n)) 'NodeHoster.Deployment' }
      catch { $PSCmdlet.WriteError($_) }
    }
  }
}

<#
.SYNOPSIS
Rolls a NodeHoster site back to an earlier release.
.DESCRIPTION
Activates the release before the active one that deployed successfully, or
the release given with -ReleaseId. The switch is a zero-downtime recycle.
.PARAMETER Name
The site's name or ID.
.PARAMETER ReleaseId
The release (deployment ID) to activate; see Get-NHRelease.
.EXAMPLE
Undo-NHDeployment shop
.EXAMPLE
Undo-NHDeployment shop -ReleaseId 20260101-120000-a1b2c3
#>
function Undo-NHDeployment {
  [CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'Medium')]
  param(
    [Parameter(Mandatory, Position = 0, ValueFromPipelineByPropertyName)]
    [Alias('SiteName')]
    [string] $Name,
    [Parameter(Position = 1)]
    [string] $ReleaseId
  )
  process {
    $cliArgs = @('rollback', $Name)
    if ($ReleaseId) { $cliArgs += $ReleaseId }
    if (-not $PSCmdlet.ShouldProcess($Name, 'roll back')) { return }
    Add-NHType (Invoke-NHCli $cliArgs) 'NodeHoster.Deployment'
  }
}

<#
.SYNOPSIS
Gets a NodeHoster site's recent output, or follows it.
.DESCRIPTION
Returns the latest lines the site's instances wrote to stdout and stderr
(or its access log with -Access). With -Follow, keeps returning new lines
until you press Ctrl+C.
.PARAMETER Name
The site's name or ID.
.PARAMETER Tail
How many recent lines (default 100).
.PARAMETER Follow
Keeps following new lines.
.PARAMETER Access
The access log instead of the application's output.
.EXAMPLE
Get-NHLog shop -Tail 20
.EXAMPLE
Get-NHLog shop -Follow | Where-Object Stream -eq 'stderr'
#>
function Get-NHLog {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory, Position = 0, ValueFromPipelineByPropertyName)]
    [Alias('SiteName')]
    [string] $Name,
    [ValidateRange(0, 5000)]
    [int] $Tail = 100,
    [switch] $Follow,
    [switch] $Access
  )
  process {
    $cliArgs = @('logs', $Name, '-n', "$Tail")
    if ($Access) { $cliArgs += '--access' }
    if (-not $Follow) {
      ConvertTo-NHLogLine (Invoke-NHCli $cliArgs) $Name
      return
    }
    # One JSON object per line while following.
    Invoke-NHCliLive ($cliArgs + '-f') | ForEach-Object {
      if ($_) { ConvertTo-NHLogLine (ConvertFrom-Json -InputObject $_) $Name }
    }
  }
}

<#
.SYNOPSIS
Gets recent NodeHoster events: crashes, restarts, deployments, certificates.
.PARAMETER Count
How many events (default 50), newest first.
.PARAMETER Site
Only this site's events (name or ID).
.EXAMPLE
Get-NHEvent -Count 20 | Where-Object Level -ne 'info'
.EXAMPLE
Get-NHEvent -Site shop
#>
function Get-NHEvent {
  [CmdletBinding()]
  param(
    [ValidateRange(1, 1000)]
    [int] $Count = 50,
    [Parameter(ValueFromPipelineByPropertyName)]
    [Alias('SiteName', 'Name')]
    [string] $Site
  )
  process {
    $cliArgs = @('events', '-n', "$Count")
    if ($Site) { $cliArgs += @('--site', $Site) }
    foreach ($e in @(Invoke-NHCli $cliArgs)) {
      if ($null -ne $e) {
        Add-Member -InputObject $e -NotePropertyName Time -NotePropertyValue ([datetime] $e.time) -Force
        Add-NHType $e 'NodeHoster.Event'
      }
    }
  }
}

<#
.SYNOPSIS
Gets the certificates in the NodeHoster certificate store.
.PARAMETER Name
A certificate name or domain; wildcards are allowed. Default: all.
.EXAMPLE
Get-NHCertificate | Where-Object { $_.notAfter -and [datetime]$_.notAfter -lt (Get-Date).AddDays(14) }
.EXAMPLE
Get-NHCertificate *.example.com
#>
function Get-NHCertificate {
  [CmdletBinding()]
  param(
    [Parameter(Position = 0)]
    [SupportsWildcards()]
    [string] $Name = '*'
  )
  foreach ($c in @(Invoke-NHCli @('cert', 'list'))) {
    if ($null -eq $c) { continue }
    $match = ($c.name -like $Name) -or ($c.id -eq $Name) -or (@($c.domains | Where-Object { $_ -like $Name }).Count -gt 0)
    if ($match) { Add-NHType $c 'NodeHoster.Certificate' }
  }
}

Export-ModuleMember -Function Get-NHSite, Start-NHSite, Stop-NHSite, Restart-NHSite, Invoke-NHRecycle,
  Publish-NHSite, Get-NHRelease, Undo-NHDeployment, Get-NHLog, Get-NHEvent, Get-NHCertificate
