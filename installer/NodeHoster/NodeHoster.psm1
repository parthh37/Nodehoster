# NodeHoster PowerShell module: the nodehoster.exe management commands as
# PowerShell commands that return objects. Each one runs
# `nodehoster.exe --json ...`, which talks to the NodeHoster service on
# this computer over its local admin pipe (\\.\pipe\NodeHoster.Admin); the
# pipe only admits elevated Administrators, so run these from an elevated
# session, as you would IIS's WebAdministration module.
#
# After Connect-NHServer, the commands target another server's web console
# over HTTPS instead (`nodehoster.exe --server`), with an API token created
# there, until Disconnect-NHServer.
#
# Windows PowerShell 5.1 compatible (and PowerShell 7).

$script:UninstallKey = 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\{6F1B3C2A-9D4E-4E7B-A1C5-2B7D9E0F4A11}_is1'

# The server Connect-NHServer chose (a saved connection's name or a URL)
# and its token when one was given; $null for this computer.
$script:NHServer = $null
$script:NHToken = $null

# The global arguments that select the server.
function Get-NHTargetArguments {
  if ($script:NHServer) { return @('--server', $script:NHServer) }
  return @()
}

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
  $psi.Arguments = ConvertTo-NHCommandLine (@('--json') + (Get-NHTargetArguments) + $Arguments)
  # The token travels in the environment, not on the command line.
  if ($script:NHToken) { $psi.EnvironmentVariables['NODEHOSTER_TOKEN'] = $script:NHToken }
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
  $target = Get-NHTargetArguments
  $savedToken = $env:NODEHOSTER_TOKEN
  try { $previous = [Console]::OutputEncoding; [Console]::OutputEncoding = [System.Text.Encoding]::UTF8 } catch { }
  try {
    if ($script:NHToken) { $env:NODEHOSTER_TOKEN = $script:NHToken }
    & $exe --json @target @Arguments
    if ($LASTEXITCODE -ne 0) { throw "nodehoster $($Arguments[0]) failed (exit code $LASTEXITCODE); see the messages above." }
  } finally {
    if ($script:NHToken) { $env:NODEHOSTER_TOKEN = $savedToken }
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
    # node and worker sites: node (Node.js), bun, deno, python, dotnet or custom.
    $rt = $null
    if ($s.PSObject.Properties['node'] -and $s.node) { $rt = if ($s.node.runtime) { $s.node.runtime } else { 'node' } }
    Add-Member -InputObject $s -NotePropertyName Runtime -NotePropertyValue $rt -Force
    Add-NHType $s 'NodeHoster.Site'
  }
}

function ConvertTo-NHTaskRun($Run) {
  foreach ($r in $Run) {
    if ($null -eq $r) { continue }
    Add-Member -InputObject $r -NotePropertyName StartedAt -NotePropertyValue ([datetime] $r.startedAt) -Force
    Add-NHType $r 'NodeHoster.TaskRun'
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
      Slot       = $l.slot # a deployment slot's line; empty for production
    }
  }
}

Update-TypeData -TypeName NodeHoster.Site -DefaultDisplayPropertySet Name, Type, Runtime, State, AutoStart, Id -Force
Update-TypeData -TypeName NodeHoster.Deployment -DefaultDisplayPropertySet Id, Source, Status, StartedAt, User, Message -Force
Update-TypeData -TypeName NodeHoster.Event -DefaultDisplayPropertySet Time, Level, Type, SiteId, Message -Force
Update-TypeData -TypeName NodeHoster.Certificate -DefaultDisplayPropertySet Name, Domains, Status, NotAfter, AutoRenew, Id -Force
Update-TypeData -TypeName NodeHoster.Task -DefaultDisplayPropertySet Site, Name, Schedule, Enabled, NextRunAt, LastStatus -Force
Update-TypeData -TypeName NodeHoster.TaskRun -DefaultDisplayPropertySet Id, TaskName, Status, StartedAt, Trigger, ExitCode, Error -Force
Update-TypeData -TypeName NodeHoster.BackupRun -DefaultDisplayPropertySet StartedAt, Trigger, Status, File, Size, Error -Force
Update-TypeData -TypeName NodeHoster.TlsSetting -DefaultDisplayPropertySet MinVersion, Http2, Http3, Http3Listeners -Force
Update-TypeData -TypeName NodeHoster.Preview -DefaultDisplayPropertySet Site, PullRequest, Branch, State, Url, Id -Force
Update-TypeData -TypeName NodeHoster.Runtime -DefaultDisplayPropertySet Runtime, Version, Status, IsDefault, Path -Force

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
.PARAMETER Slot
Deploys to this deployment slot (e.g. staging) instead of production;
Switch-NHSlot then swaps it into production.
.EXAMPLE
Publish-NHSite shop -ZipPath .\build\shop.zip
.EXAMPLE
Publish-NHSite shop -Git -Branch release
.EXAMPLE
Publish-NHSite shop .\build\shop.zip -Slot staging
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
    [string] $Branch,
    [string] $Slot
  )
  process {
    $cliArgs = @('deploy', $Name)
    if ($Slot) { $cliArgs += @('--slot', $Slot) }
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
.PARAMETER Slot
Only the deployments made to this slot ("production" for the site's own).
.EXAMPLE
Get-NHRelease shop | Where-Object Status -eq 'failed'
.EXAMPLE
Get-NHRelease shop -Slot staging
#>
function Get-NHRelease {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory, Position = 0, ValueFromPipeline, ValueFromPipelineByPropertyName)]
    [Alias('SiteName')]
    [string[]] $Name,
    [string] $Slot
  )
  process {
    foreach ($n in $Name) {
      $cliArgs = @('releases', $n)
      if ($Slot) { $cliArgs += @('--slot', $Slot) }
      try { Add-NHType (Invoke-NHCli $cliArgs) 'NodeHoster.Deployment' }
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
.PARAMETER Slot
Rolls this deployment slot back instead of production. (To roll
production back after a swap, swap again: Switch-NHSlot.)
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
    [string] $ReleaseId,
    [string] $Slot
  )
  process {
    $cliArgs = @('rollback', $Name)
    if ($ReleaseId) { $cliArgs += $ReleaseId }
    if ($Slot) { $cliArgs += @('--slot', $Slot) }
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
.PARAMETER Slot
Only this deployment slot's lines ("production" for the site's own).
.EXAMPLE
Get-NHLog shop -Tail 20
.EXAMPLE
Get-NHLog shop -Follow | Where-Object Stream -eq 'stderr'
.EXAMPLE
Get-NHLog shop -Slot staging -Access
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
    [switch] $Access,
    [string] $Slot
  )
  process {
    $cliArgs = @('logs', $Name, '-n', "$Tail")
    if ($Access) { $cliArgs += '--access' }
    if ($Slot) { $cliArgs += @('--slot', $Slot) }
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

<#
.SYNOPSIS
Gets a NodeHoster site's scheduled tasks, with their next run and last result.
.PARAMETER Name
The site's name or ID. Accepts pipeline input.
.PARAMETER Task
A task name; wildcards are allowed. Default: all.
.EXAMPLE
Get-NHTask shop
.EXAMPLE
Get-NHSite | Get-NHTask | Where-Object LastStatus -eq 'failed'
#>
function Get-NHTask {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory, Position = 0, ValueFromPipeline, ValueFromPipelineByPropertyName)]
    [Alias('SiteName')]
    [string[]] $Name,
    [Parameter(Position = 1)]
    [SupportsWildcards()]
    [string] $Task = '*'
  )
  process {
    foreach ($n in $Name) {
      try {
        foreach ($t in @(Invoke-NHCli @('task', 'list', $n))) {
          if ($null -eq $t -or -not (($t.name -like $Task) -or ($t.id -eq $Task))) { continue }
          $next = $null
          if ($t.PSObject.Properties['nextRunAt'] -and $t.nextRunAt) { $next = [datetime] $t.nextRunAt }
          $last = $null
          if ($t.PSObject.Properties['lastRun'] -and $t.lastRun) { $last = $t.lastRun.status }
          Add-Member -InputObject $t -NotePropertyName Site -NotePropertyValue $n -Force
          Add-Member -InputObject $t -NotePropertyName NextRunAt -NotePropertyValue $next -Force
          Add-Member -InputObject $t -NotePropertyName LastStatus -NotePropertyValue $last -Force
          Add-NHType $t 'NodeHoster.Task'
        }
      } catch { $PSCmdlet.WriteError($_) }
    }
  }
}

<#
.SYNOPSIS
Runs a NodeHoster scheduled task now.
.DESCRIPTION
Starts a run of the task, shows its output as it runs, and returns the
finished run. A run that does not succeed (failed, timed out, cancelled) is
an error. With -NoWait, returns the run as soon as it has started. When the
task's overlap policy is "queue" and a run is in progress, the new run waits
for it: you get a warning and no run.
.PARAMETER Name
The site's name or ID. Accepts sites from Get-NHSite on the pipeline.
.PARAMETER Task
The task's name or ID.
.PARAMETER NoWait
Returns once the run has started instead of waiting for it to end.
.EXAMPLE
Start-NHTask shop nightly-report
.EXAMPLE
Start-NHTask shop cleanup -NoWait
#>
function Start-NHTask {
  [CmdletBinding(SupportsShouldProcess)]
  param(
    [Parameter(Mandatory, Position = 0, ValueFromPipelineByPropertyName)]
    [Alias('SiteName')]
    [string] $Name,
    [Parameter(Mandatory, Position = 1)]
    [string] $Task,
    [switch] $NoWait
  )
  process {
    if (-not $PSCmdlet.ShouldProcess("$Name/$Task", 'run task')) { return }
    $cliArgs = @('task', 'run', $Name, $Task)
    if ($NoWait) {
      $res = Invoke-NHCli ($cliArgs + '--no-wait')
    } else {
      $lines = @(Invoke-NHCliLive $cliArgs)
      $res = ConvertFrom-Json -InputObject ($lines -join "`n")
    }
    # {run, queued} when the run did not start right away (or with -NoWait).
    if ($res.PSObject.Properties['queued']) {
      if (-not $res.run) {
        Write-Warning "Task $Task of $Name is queued: it runs when the current run ends."
        return
      }
      $res = $res.run
    }
    ConvertTo-NHTaskRun $res
  }
}

<#
.SYNOPSIS
Gets recent runs of a NodeHoster site's scheduled tasks, newest first.
.PARAMETER Name
The site's name or ID. Accepts pipeline input.
.PARAMETER Task
Only this task's runs (name or ID).
.PARAMETER Count
How many runs (default 20).
.EXAMPLE
Get-NHTaskRun shop -Task nightly-report -Count 5
.EXAMPLE
Get-NHTaskRun shop | Where-Object Status -ne 'succeeded'
#>
function Get-NHTaskRun {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory, Position = 0, ValueFromPipeline, ValueFromPipelineByPropertyName)]
    [Alias('SiteName')]
    [string[]] $Name,
    [Parameter(Position = 1)]
    [string] $Task,
    [ValidateRange(1, 500)]
    [int] $Count = 20
  )
  process {
    foreach ($n in $Name) {
      $cliArgs = @('task', 'runs', $n)
      if ($Task) { $cliArgs += $Task }
      try { ConvertTo-NHTaskRun @(Invoke-NHCli ($cliArgs + @('-n', "$Count"))) }
      catch { $PSCmdlet.WriteError($_) }
    }
  }
}

<#
.SYNOPSIS
Runs a NodeHoster backup to the configured destinations now.
.DESCRIPTION
Makes a backup archive as the backup settings say (contents, passphrase),
copies it to every enabled destination, waits for it to finish and returns
the result, with one entry per destination in its destinations property.
A backup that did not reach every destination is an error.
.EXAMPLE
Start-NHBackup
.EXAMPLE
(Start-NHBackup).destinations | Format-Table name, ok, error
#>
function Start-NHBackup {
  [CmdletBinding(SupportsShouldProcess)]
  param()
  if (-not $PSCmdlet.ShouldProcess('backup destinations', 'run a backup')) { return }
  $run = Invoke-NHCli @('backup', 'run')
  Add-Member -InputObject $run -NotePropertyName StartedAt -NotePropertyValue ([datetime] $run.startedAt) -Force
  Add-NHType $run 'NodeHoster.BackupRun'
}

<#
.SYNOPSIS
Asks the OCSP responder of NodeHoster certificates now and returns them.
.DESCRIPTION
NodeHoster staples OCSP responses by itself, refreshing them halfway to
their expiry; this checks at once (after the CA fixed its responder, say).
The ocsp property tells the result: state none (no responder, as for
Let's Encrypt), good, revoked, unknown or error, and whether a response is
stapled. Accepts certificates from Get-NHCertificate on the pipeline.
.PARAMETER Id
A certificate's ID, name or domain (from the pipeline: its id).
.EXAMPLE
Update-NHCertificateOcsp shop.example.com | Select-Object name, @{ n = 'ocsp'; e = { $_.ocsp.state } }
.EXAMPLE
Get-NHCertificate | Where-Object { $_.ocsp.state -eq 'error' } | Update-NHCertificateOcsp
#>
function Update-NHCertificateOcsp {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory, Position = 0, ValueFromPipelineByPropertyName)]
    [Alias('Name')]
    [string[]] $Id
  )
  process {
    foreach ($n in $Id) {
      try { Add-NHType (Invoke-NHCli @('cert', 'ocsp', $n)) 'NodeHoster.Certificate' }
      catch { $PSCmdlet.WriteError($_) }
    }
  }
}

<#
.SYNOPSIS
Gets NodeHoster's TLS settings: minimum TLS version, HTTP/2, HTTP/3.
.DESCRIPTION
http3Listeners lists the UDP (QUIC) listeners HTTP/3 opened next to the
HTTPS listeners, and the ones that could not be opened.
.EXAMPLE
Get-NHTlsSetting
#>
function Get-NHTlsSetting {
  [CmdletBinding()]
  param()
  Add-NHType (Invoke-NHCli @('tls')) 'NodeHoster.TlsSetting'
}

<#
.SYNOPSIS
Changes NodeHoster's TLS settings for every HTTPS binding.
.DESCRIPTION
Only the settings given change; listeners follow at once. HTTP/3 opens a
UDP listener on each HTTPS port (setup's Windows Firewall rule allows the
program, UDP included; open UDP on firewalls in front of the server too).
.EXAMPLE
Set-NHTlsSetting -Http3 $true
.EXAMPLE
Set-NHTlsSetting -MinVersion 1.3
#>
function Set-NHTlsSetting {
  [CmdletBinding(SupportsShouldProcess)]
  param(
    [Nullable[bool]] $Http3,
    [Nullable[bool]] $Http2,
    [ValidateSet('1.2', '1.3')]
    [string] $MinVersion
  )
  $cliArgs = @('tls', 'set')
  if ($null -ne $Http3) { $cliArgs += @('--http3', $(if ($Http3) { 'on' } else { 'off' })) }
  if ($null -ne $Http2) { $cliArgs += @('--http2', $(if ($Http2) { 'on' } else { 'off' })) }
  if ($MinVersion) { $cliArgs += @('--min-version', $MinVersion) }
  if ($cliArgs.Count -eq 2) { throw 'Give at least one of -Http3, -Http2 or -MinVersion.' }
  if (-not $PSCmdlet.ShouldProcess('TLS settings', 'change')) { return }
  Add-NHType (Invoke-NHCli $cliArgs) 'NodeHoster.TlsSetting'
}

<#
.SYNOPSIS
Gets the preview deployments of a NodeHoster site.
.DESCRIPTION
A site with previews enabled gets a temporary site per pull request (or
previewed branch), created, redeployed and deleted by its push webhook.
This lists them with their address, branch, pull request, commit and state
(pending, deploying, ready, failed, deleting).
.PARAMETER Name
The parent site's name or ID. Accepts pipeline input.
.EXAMPLE
Get-NHPreview shop
.EXAMPLE
Get-NHPreview shop | Where-Object State -eq 'failed' | Publish-NHPreview
#>
function Get-NHPreview {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory, Position = 0, ValueFromPipeline, ValueFromPipelineByPropertyName)]
    [Alias('SiteName')]
    [string[]] $Name
  )
  process {
    foreach ($n in $Name) {
      try {
        foreach ($p in @(Invoke-NHCli @('preview', 'list', $n))) {
          if ($null -eq $p) { continue }
          $pr = $null
          if ($p.preview.kind -eq 'pr') { $pr = $p.preview.number }
          Add-Member -InputObject $p -NotePropertyName Site -NotePropertyValue $n -Force
          Add-Member -InputObject $p -NotePropertyName PullRequest -NotePropertyValue $pr -Force
          Add-Member -InputObject $p -NotePropertyName Branch -NotePropertyValue $p.preview.branch -Force
          Add-Member -InputObject $p -NotePropertyName Url -NotePropertyValue $p.preview.url -Force
          Add-Member -InputObject $p -NotePropertyName Commit -NotePropertyValue $p.preview.commit -Force
          Add-Member -InputObject $p -NotePropertyName LastPush -NotePropertyValue ([datetime] $p.preview.lastPush) -Force
          Add-NHType $p 'NodeHoster.Preview'
        }
      } catch { $PSCmdlet.WriteError($_) }
    }
  }
}

<#
.SYNOPSIS
Deploys a branch as a preview of a NodeHoster site, or a preview again.
.DESCRIPTION
With -Branch, deploys that branch as a preview (created if it does not
exist; the production branch is refused). With -Preview (or previews from
Get-NHPreview on the pipeline), deploys a preview's head again. The
deployment runs in the background: Get-NHPreview shows its state.
.PARAMETER Name
The parent site's name or ID.
.PARAMETER Branch
A branch to deploy as a preview.
.PARAMETER Preview
A preview: its ID, pull request number, host name or branch.
.EXAMPLE
Publish-NHPreview shop -Branch feature/checkout
.EXAMPLE
Publish-NHPreview shop -Preview 42
#>
function Publish-NHPreview {
  [CmdletBinding(SupportsShouldProcess, DefaultParameterSetName = 'Preview')]
  param(
    [Parameter(Mandatory, Position = 0, ValueFromPipelineByPropertyName)]
    [Alias('SiteName', 'Site')]
    [string] $Name,
    [Parameter(Mandatory, ParameterSetName = 'Branch')]
    [string] $Branch,
    [Parameter(Mandatory, Position = 1, ParameterSetName = 'Preview', ValueFromPipelineByPropertyName)]
    [Alias('Id')]
    [string] $Preview
  )
  process {
    if ($PSCmdlet.ParameterSetName -eq 'Branch') {
      if (-not $PSCmdlet.ShouldProcess("$Name/$Branch", 'deploy preview')) { return }
      Invoke-NHCli @('preview', 'deploy', $Name, $Branch)
      return
    }
    if (-not $PSCmdlet.ShouldProcess("$Name/$Preview", 'redeploy preview')) { return }
    Add-NHType (Invoke-NHCli @('preview', 'redeploy', $Name, $Preview)) 'NodeHoster.Preview'
  }
}

<#
.SYNOPSIS
Deletes a preview deployment of a NodeHoster site.
.DESCRIPTION
Deletes the preview's site with its releases, logs and automatic
certificate, after any deployment of it in progress. Closing or merging a
pull request and deleting a branch delete their previews on their own;
this is for the others.
.PARAMETER Name
The parent site's name or ID.
.PARAMETER Preview
The preview: its ID, pull request number, host name or branch. Accepts
previews from Get-NHPreview on the pipeline.
.EXAMPLE
Remove-NHPreview shop 42
.EXAMPLE
Get-NHPreview shop | Where-Object LastPush -lt (Get-Date).AddDays(-3) | Remove-NHPreview -Confirm:$false
#>
function Remove-NHPreview {
  [CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'High')]
  param(
    [Parameter(Mandatory, Position = 0, ValueFromPipelineByPropertyName)]
    [Alias('SiteName', 'Site')]
    [string] $Name,
    [Parameter(Mandatory, Position = 1, ValueFromPipelineByPropertyName)]
    [Alias('Id')]
    [string] $Preview
  )
  process {
    if (-not $PSCmdlet.ShouldProcess("$Name/$Preview", 'delete preview')) { return }
    try { Invoke-NHCli @('preview', 'delete', $Name, $Preview, '--yes') | Out-Null }
    catch { $PSCmdlet.WriteError($_) }
  }
}

<#
.SYNOPSIS
Makes the NodeHoster commands target another server.
.DESCRIPTION
Until Disconnect-NHServer, the commands of this module manage another
NodeHoster server through its web console's HTTPS API instead of the
service on this computer, as `nodehoster.exe --server` does. -Server is a
connection saved with `nodehoster server add` (or NodeHoster Manager's
"Connect to a server..."), whose token is protected for your Windows
account, or the console's URL with -Token. What the token's role allows on
that server is what the commands can do.
.PARAMETER Server
A saved connection's name, or the web console's URL (https://web02:8484).
.PARAMETER Token
An API token created on that server; overrides a saved connection's.
.EXAMPLE
Connect-NHServer web02; Get-NHSite; Disconnect-NHServer
.EXAMPLE
Connect-NHServer https://web02.example.com:8484 -Token $env:WEB02_TOKEN
#>
function Connect-NHServer {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory, Position = 0)]
    [string] $Server,
    [string] $Token
  )
  $previous = $script:NHServer, $script:NHToken
  $script:NHServer, $script:NHToken = $Server, $Token
  try {
    # Reach it now, so that a mistake shows here rather than later.
    $null = Invoke-NHCli @('site', 'list')
  } catch {
    $script:NHServer, $script:NHToken = $previous
    throw
  }
}

<#
.SYNOPSIS
Makes the NodeHoster commands target this computer's service again.
#>
function Disconnect-NHServer {
  [CmdletBinding()]
  param()
  $script:NHServer, $script:NHToken = $null, $null
}

<#
.SYNOPSIS
Lists the connections to other servers saved for your Windows account.
.DESCRIPTION
Save one with `nodehoster server add <name> <url>` (it asks for the token
and shows the certificate to trust), then use it with Connect-NHServer.
.EXAMPLE
Get-NHServer
#>
function Get-NHServer {
  [CmdletBinding()]
  param()
  $server, $token = $script:NHServer, $script:NHToken
  $script:NHServer, $script:NHToken = $null, $null # saved connections are this computer's
  try {
    foreach ($s in @(Invoke-NHCli @('server', 'list'))) {
      Add-Member -InputObject $s -NotePropertyName Connected -NotePropertyValue ($server -and ($server -eq $s.name -or $server -eq $s.url)) -Force
      Add-NHType $s 'NodeHoster.Server'
    }
  } finally {
    $script:NHServer, $script:NHToken = $server, $token
  }
}

Update-TypeData -TypeName NodeHoster.Server -DefaultDisplayPropertySet name, url, fingerprint, tokenSaved, Connected -Force

function ConvertTo-NHAlert($Alert) {
  foreach ($a in $Alert) {
    if ($null -eq $a) { continue }
    $site = 'server'
    if ($a.PSObject.Properties['siteName'] -and $a.siteName) { $site = $a.siteName }
    $fired = $null
    if ($a.PSObject.Properties['firedAt'] -and $a.firedAt) { $fired = [datetime] $a.firedAt }
    $resolved = $null
    if ($a.PSObject.Properties['resolvedAt'] -and $a.resolvedAt) { $resolved = [datetime] $a.resolvedAt }
    $silenced = [bool] ($a.PSObject.Properties['silence'] -and $a.silence)
    Add-Member -InputObject $a -NotePropertyName Site -NotePropertyValue $site -Force
    Add-Member -InputObject $a -NotePropertyName FiredAt -NotePropertyValue $fired -Force
    Add-Member -InputObject $a -NotePropertyName ResolvedAt -NotePropertyValue $resolved -Force
    Add-Member -InputObject $a -NotePropertyName Silenced -NotePropertyValue $silenced -Force
    Add-NHType $a 'NodeHoster.Alert'
  }
}

Update-TypeData -TypeName NodeHoster.Alert -DefaultDisplayPropertySet Site, Severity, State, Message, FiredAt, Silenced, Id -Force

<#
.SYNOPSIS
Gets NodeHoster resource alerts: those firing, or the recent ones.
.DESCRIPTION
Without -History, returns the alerts firing now (with -Pending, also the
conditions past their limit that have not lasted long enough to fire).
With -History, returns the alerts that fired, newest first, resolved ones
included.
.PARAMETER Site
Only this site's alerts (name or ID). Accepts pipeline input.
.PARAMETER Pending
Also return pending alerts.
.PARAMETER History
Return recent alerts instead of the ones in progress.
.PARAMETER Count
With -History, how many (default 50).
.EXAMPLE
Get-NHAlert | Where-Object Severity -eq 'critical'
.EXAMPLE
Get-NHSite shop | Get-NHAlert -History -Count 10
#>
function Get-NHAlert {
  [CmdletBinding()]
  param(
    [Parameter(Position = 0, ValueFromPipelineByPropertyName)]
    [Alias('SiteName', 'Name')]
    [string] $Site,
    [switch] $Pending,
    [switch] $History,
    [ValidateRange(1, 1000)]
    [int] $Count = 50
  )
  process {
    if ($History) {
      $cliArgs = @('alert', 'history', '-n', "$Count")
      if ($Site) { $cliArgs += @('--site', $Site) }
      ConvertTo-NHAlert @(Invoke-NHCli $cliArgs)
      return
    }
    $cliArgs = @('alert', 'list')
    if ($Site) { $cliArgs += @('--site', $Site) }
    $list = Invoke-NHCli $cliArgs
    if (-not $list.enabled) { Write-Warning 'Alerts are off (Settings > Alerts in the web console).' }
    ConvertTo-NHAlert @($list.firing)
    if ($Pending) { ConvertTo-NHAlert @($list.pending) }
  }
}

<#
.SYNOPSIS
Silences a NodeHoster alert, for a while or until it resolves.
.DESCRIPTION
A silenced alert sends no notification or reminder. A timed silence also
covers the rule's next alerts on the same site until it ends; without
-Minutes (or with 0), the alert is acknowledged: silent until it resolves.
.PARAMETER Id
The alert's ID (or its first characters). Accepts alerts from Get-NHAlert.
.PARAMETER Minutes
How long. 0 (the default) acknowledges the alert.
.PARAMETER Note
Why, shown with the alert and written to the audit log.
.EXAMPLE
Get-NHAlert | Where-Object Site -eq 'shop' | Set-NHAlertSilence -Minutes 60 -Note 'deploying a fix'
#>
function Set-NHAlertSilence {
  [CmdletBinding(SupportsShouldProcess)]
  param(
    [Parameter(Mandatory, Position = 0, ValueFromPipelineByPropertyName)]
    [string] $Id,
    [ValidateRange(0, 43200)]
    [int] $Minutes = 0,
    [string] $Note
  )
  process {
    if (-not $PSCmdlet.ShouldProcess($Id, 'silence alert')) { return }
    $cliArgs = @('alert', 'silence', $Id, '--minutes', "$Minutes")
    if ($Note) { $cliArgs += @('--note', $Note) }
    ConvertTo-NHAlert (Invoke-NHCli $cliArgs)
  }
}

<#
.SYNOPSIS
Lifts a NodeHoster alert's silence: it notifies again.
.PARAMETER Id
The alert's ID (or its first characters). Accepts alerts from Get-NHAlert.
.EXAMPLE
Get-NHAlert | Where-Object Silenced | Clear-NHAlertSilence
#>
function Clear-NHAlertSilence {
  [CmdletBinding(SupportsShouldProcess)]
  param(
    [Parameter(Mandatory, Position = 0, ValueFromPipelineByPropertyName)]
    [string] $Id
  )
  process {
    if (-not $PSCmdlet.ShouldProcess($Id, 'unsilence alert')) { return }
    ConvertTo-NHAlert (Invoke-NHCli @('alert', 'unsilence', $Id))
  }
}

<#
.SYNOPSIS
Gets the secret stores (HashiCorp Vault / OpenBao, Infisical, Bitwarden
Secrets Manager) and their state: references, values in memory, last read
and last error. Never any value.
.PARAMETER Name
A store name; wildcards are allowed. Default: all.
.PARAMETER Test
Also sign in to each store now; with -Reference, read that reference too.
Adds TestOK, TestDetail and TestError.
.PARAMETER Reference
With -Test: a reference to read, e.g. app/prod#DB_PASSWORD.
.EXAMPLE
Get-NHSecretStore
.EXAMPLE
Get-NHSecretStore vault -Test -Reference 'app/prod#DB_PASSWORD'
#>
function Get-NHSecretStore {
  [CmdletBinding()]
  param(
    [Parameter(Position = 0)]
    [SupportsWildcards()]
    [string] $Name = '*',
    [switch] $Test,
    [string] $Reference
  )
  foreach ($s in @(Invoke-NHCli @('secrets', 'list'))) {
    if ($null -eq $s -or -not ($s.name -like $Name)) { continue }
    if ($Test) {
      $cliArgs = @('secrets', 'test', $s.name)
      if ($Reference) { $cliArgs += @('--ref', $Reference) }
      try {
        $r = Invoke-NHCli $cliArgs
        Add-Member -InputObject $s -NotePropertyName TestOK -NotePropertyValue ([bool] $r.ok) -Force
        Add-Member -InputObject $s -NotePropertyName TestDetail -NotePropertyValue $r.detail -Force
        Add-Member -InputObject $s -NotePropertyName TestError -NotePropertyValue $r.error -Force
      } catch {
        Add-Member -InputObject $s -NotePropertyName TestOK -NotePropertyValue $false -Force
        Add-Member -InputObject $s -NotePropertyName TestError -NotePropertyValue $_.Exception.Message -Force
      }
    }
    Add-NHType $s 'NodeHoster.SecretStore'
  }
}

<#
.SYNOPSIS
Reads every secret store reference of a site now (its variables, its
tasks' variables and its git token) and returns one result per reference.
Values are never returned.
.PARAMETER Name
The site's name or ID. Accepts pipeline input.
.EXAMPLE
Test-NHSecretReference shop | Where-Object { -not $_.ok }
.EXAMPLE
Get-NHSite | Test-NHSecretReference
#>
function Test-NHSecretReference {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory, Position = 0, ValueFromPipeline, ValueFromPipelineByPropertyName)]
    [Alias('SiteName')]
    [string[]] $Name
  )
  process {
    foreach ($n in $Name) {
      # A reference that fails makes the command exit with 1 after writing
      # every result as JSON: read them all rather than stopping.
      $psi = New-Object System.Diagnostics.ProcessStartInfo
      $psi.FileName = Get-NHExecutable
      $psi.Arguments = ConvertTo-NHCommandLine @('--json', 'secrets', 'check', $n)
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
      if ($p.ExitCode -ne 0 -and -not $out.Trim()) {
        $msg = (($stderr.Result -split "`r?`n") | Where-Object { $_ } | Select-Object -First 1) -replace '^error:\s*', ''
        if (-not $msg) { $msg = "nodehoster.exe exited with code $($p.ExitCode)" }
        throw $msg
      }
      foreach ($r in @(ConvertFrom-Json -InputObject $out)) {
        foreach ($item in @($r)) {
          if ($null -eq $item) { continue }
          Add-Member -InputObject $item -NotePropertyName Site -NotePropertyValue $n -Force
          Add-NHType $item 'NodeHoster.SecretReferenceCheck'
        }
      }
    }
  }
}

Update-TypeData -TypeName NodeHoster.Slot -DefaultDisplayPropertySet Site, Name, State, Ready, Release, AutoSwap -Force
Update-TypeData -TypeName NodeHoster.SwapResult -DefaultDisplayPropertySet Slot, Succeeded, ProductionRelease, SlotRelease, Message -Force

<#
.SYNOPSIS
Gets a NodeHoster site's deployment slots: production and its staging slots.
.DESCRIPTION
One object per slot, production first, with its state, ready instances,
release and bindings (status and bindings properties).
.PARAMETER Name
The site's name or ID. Accepts pipeline input.
.EXAMPLE
Get-NHSlot shop
.EXAMPLE
Get-NHSite | Get-NHSlot | Where-Object Name -ne 'production'
#>
function Get-NHSlot {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory, Position = 0, ValueFromPipeline, ValueFromPipelineByPropertyName)]
    [Alias('SiteName')]
    [string[]] $Name
  )
  process {
    foreach ($n in $Name) {
      try {
        $view = Invoke-NHCli @('slot', 'list', $n)
        foreach ($s in @($view.slots)) {
          if ($null -eq $s) { continue }
          $ready = @($s.status.instances | Where-Object { $_.state -eq 'ready' }).Count
          Add-Member -InputObject $s -NotePropertyName Site -NotePropertyValue $n -Force
          Add-Member -InputObject $s -NotePropertyName State -NotePropertyValue $s.status.state -Force
          Add-Member -InputObject $s -NotePropertyName Ready -NotePropertyValue "$ready/$(@($s.status.instances).Count)" -Force
          if (-not $s.PSObject.Properties['autoSwap']) { Add-Member -InputObject $s -NotePropertyName autoSwap -NotePropertyValue $false -Force }
          if (-not $s.PSObject.Properties['release']) { Add-Member -InputObject $s -NotePropertyName release -NotePropertyValue $null -Force }
          Add-NHType $s 'NodeHoster.Slot'
        }
      } catch { $PSCmdlet.WriteError($_) }
    }
  }
}

<#
.SYNOPSIS
Swaps a NodeHoster deployment slot into production, like an Azure slot swap.
.DESCRIPTION
The slot's instances restart with production's settings, are warmed up
(its warm-up paths must answer an accepted status on every instance),
then take production's traffic at once; the old production release
becomes the slot. Swapping again is the rollback. Shows the swap's
progress and returns its result; a swap that fails (production then
stays as it was) is an error.
.PARAMETER Name
The site's name or ID.
.PARAMETER Slot
The slot to swap into production (default: the site's only slot).
.PARAMETER NoWait
Returns once the swap has started.
.EXAMPLE
Switch-NHSlot shop
.EXAMPLE
Publish-NHSite shop .\build\shop.zip -Slot staging; Switch-NHSlot shop -Slot staging -Confirm:$false
#>
function Switch-NHSlot {
  [CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'High')]
  param(
    [Parameter(Mandatory, Position = 0, ValueFromPipelineByPropertyName)]
    [Alias('SiteName')]
    [string] $Name,
    [Parameter(Position = 1)]
    [string] $Slot,
    [switch] $NoWait
  )
  process {
    $what = 'swap the deployment slot into production'
    if ($Slot) { $what = "swap slot $Slot into production" }
    if (-not $PSCmdlet.ShouldProcess($Name, $what)) { return }
    $cliArgs = @('slot', 'swap', $Name)
    if ($Slot) { $cliArgs += $Slot }
    $cliArgs += '--yes'
    if ($NoWait) {
      Invoke-NHCli ($cliArgs + '--no-wait')
      return
    }
    $lines = @(Invoke-NHCliLive $cliArgs)
    $res = ConvertFrom-Json -InputObject ($lines -join "`n")
    foreach ($p in 'productionRelease', 'slotRelease') {
      if (-not $res.PSObject.Properties[$p]) { Add-Member -InputObject $res -NotePropertyName $p -NotePropertyValue $null -Force }
    }
    Add-NHType $res 'NodeHoster.SwapResult'
  }
}

<#
.SYNOPSIS
Gets the runtimes besides Node.js that sites can run with.
.DESCRIPTION
One object per Bun or Deno version (installed by NodeHoster, or found on
PATH), Python interpreter and .NET runtime found on this computer.
IsDefault marks what sites that pin no version use.
.PARAMETER Runtime
bun, deno, python or dotnet; wildcards are allowed. Default: all.
.EXAMPLE
Get-NHRuntime
.EXAMPLE
Get-NHRuntime python | Where-Object IsDefault
#>
function Get-NHRuntime {
  [CmdletBinding()]
  param(
    [Parameter(Position = 0)]
    [SupportsWildcards()]
    [string] $Runtime = '*'
  )
  $r = Invoke-NHCli @('runtime', 'list')
  $out = New-Object System.Collections.Generic.List[object]
  foreach ($rt in 'bun', 'deno') {
    $m = $r.$rt
    foreach ($i in @($m.installed)) {
      if ($null -eq $i) { continue }
      $out.Add([pscustomobject]@{ PSTypeName = 'NodeHoster.Runtime'; Runtime = $rt; Version = $i.version; Status = $i.status; IsDefault = [bool] $i.isDefault; Path = $i.path; Error = $i.error })
    }
    if ($m.system) {
      $default = -not (@($m.installed | Where-Object { $_.isDefault }).Count -gt 0)
      $out.Add([pscustomobject]@{ PSTypeName = 'NodeHoster.Runtime'; Runtime = $rt; Version = $m.system.version; Status = 'on PATH'; IsDefault = $default; Path = $m.system.path; Error = $null })
    }
  }
  foreach ($p in @($r.python)) {
    if ($null -eq $p) { continue }
    $out.Add([pscustomobject]@{ PSTypeName = 'NodeHoster.Runtime'; Runtime = 'python'; Version = $p.version; Status = "found ($($p.source))"; IsDefault = [bool] $p.isDefault; Path = $p.path; Error = $null })
  }
  if ($r.dotnet) {
    foreach ($d in @($r.dotnet.runtimes)) {
      if ($null -eq $d) { continue }
      $out.Add([pscustomobject]@{ PSTypeName = 'NodeHoster.Runtime'; Runtime = 'dotnet'; Version = $d.version; Status = $d.name; IsDefault = $false; Path = $r.dotnet.host; Error = $null })
    }
  }
  $out | Where-Object { $_.Runtime -like $Runtime }
}

<#
.SYNOPSIS
Installs a Bun or Deno version for NodeHoster's sites.
.DESCRIPTION
Downloads the official release for this computer from GitHub, checks its
published SHA-256, unpacks it into NodeHoster's data folder and waits for
it. Python and .NET are not installed by NodeHoster: use their installers.
.PARAMETER Runtime
bun or deno.
.PARAMETER Version
The version, e.g. 1.1.30. Default: the newest release.
.PARAMETER Default
Makes it the server's default for sites that pin no version (it becomes
the default anyway when there is none and none on PATH).
.EXAMPLE
Install-NHRuntime bun
.EXAMPLE
Install-NHRuntime deno 2.1.4 -Default
#>
function Install-NHRuntime {
  [CmdletBinding(SupportsShouldProcess)]
  param(
    [Parameter(Mandatory, Position = 0)]
    [ValidateSet('bun', 'deno')]
    [string] $Runtime,
    [Parameter(Position = 1)]
    [string] $Version,
    [switch] $Default
  )
  $what = if ($Version) { "$Runtime $Version" } else { "the newest $Runtime" }
  if (-not $PSCmdlet.ShouldProcess($what, 'install')) { return }
  $cliArgs = @('runtime', 'install', $Runtime)
  if ($Version) { $cliArgs += $Version }
  if ($Default) { $cliArgs += '--default' }
  Invoke-NHCli $cliArgs
}

Update-TypeData -TypeName NodeHoster.WafEvent -DefaultDisplayPropertySet Time, Action, ClientIp, Method, Path, Rules, Score, Id -Force

<#
.SYNOPSIS
Gets requests the web application firewall blocked (or, in detect mode,
would have blocked), newest first.
.PARAMETER Name
Only this site's events (name or ID). Accepts pipeline input.
.PARAMETER Action
Blocked or Detected.
.PARAMETER ClientIP
Only this client address.
.PARAMETER Rule
Only events that matched this rule ID.
.PARAMETER RequestId
The event of this request ID, as shown on the block page.
.PARAMETER Count
How many events (default 50).
.EXAMPLE
Get-NHWafEvent shop -Action Blocked -Count 20
.EXAMPLE
Get-NHWafEvent -RequestId 9f2c4e1ab37d0c55 | Select-Object -ExpandProperty matches
.EXAMPLE
Get-NHWafEvent | Group-Object ClientIp | Sort-Object Count -Descending | Select-Object -First 10
#>
function Get-NHWafEvent {
  [CmdletBinding()]
  param(
    [Parameter(Position = 0, ValueFromPipeline, ValueFromPipelineByPropertyName)]
    [Alias('SiteName')]
    [string[]] $Name,
    [ValidateSet('Blocked', 'Detected')]
    [string] $Action,
    [string] $ClientIP,
    [int] $Rule,
    [string] $RequestId,
    [ValidateRange(1, 1000)]
    [int] $Count = 50
  )
  process {
    $filters = @('-n', "$Count")
    if ($Action) { $filters += @('--action', $Action.ToLowerInvariant()) }
    if ($ClientIP) { $filters += @('--ip', $ClientIP) }
    if ($Rule) { $filters += @('--rule', "$Rule") }
    if ($RequestId) { $filters += @('--id', $RequestId) }
    $targets = if ($Name) { $Name } else { @($null) }
    foreach ($n in $targets) {
      $cliArgs = @('waf', 'events')
      if ($n) { $cliArgs += $n }
      try {
        foreach ($e in @(Invoke-NHCli ($cliArgs + $filters))) {
          if ($null -eq $e) { continue }
          Add-Member -InputObject $e -NotePropertyName Time -NotePropertyValue ([datetime] $e.time) -Force
          Add-Member -InputObject $e -NotePropertyName Rules -NotePropertyValue ((@($e.matches) | ForEach-Object { $_.ruleId }) -join ', ') -Force
          Add-NHType $e 'NodeHoster.WafEvent'
        }
      } catch { $PSCmdlet.WriteError($_) }
    }
  }
}

<#
.SYNOPSIS
Sets a site's web application firewall mode: Off, Detect (log what would
be blocked) or Block.
.DESCRIPTION
Keeps the site's exclusions. Returns the site's firewall configuration and
counters.
.PARAMETER Name
The site's name or ID. Accepts sites from Get-NHSite on the pipeline.
.PARAMETER Mode
Off, Detect or Block.
.PARAMETER ParanoiaLevel
1 (standard) to 3 (paranoid); unchanged if not given.
.PARAMETER Threshold
The anomaly score that blocks; unchanged if not given.
.EXAMPLE
Set-NHWafMode shop Block
.EXAMPLE
Get-NHSite | Where-Object type -ne 'worker' | Set-NHWafMode -Mode Detect
#>
function Set-NHWafMode {
  [CmdletBinding(SupportsShouldProcess)]
  param(
    [Parameter(Mandatory, Position = 0, ValueFromPipelineByPropertyName)]
    [Alias('SiteName')]
    [string] $Name,
    [Parameter(Mandatory, Position = 1)]
    [ValidateSet('Off', 'Detect', 'Block')]
    [string] $Mode,
    [ValidateRange(1, 3)]
    [int] $ParanoiaLevel,
    [ValidateRange(1, 1000)]
    [int] $Threshold
  )
  process {
    if (-not $PSCmdlet.ShouldProcess($Name, "set the web application firewall to $Mode")) { return }
    $cliArgs = @('waf', 'mode', $Name, $Mode.ToLowerInvariant())
    if ($ParanoiaLevel) { $cliArgs += @('--paranoia', "$ParanoiaLevel") }
    if ($Threshold) { $cliArgs += @('--threshold', "$Threshold") }
    try { Invoke-NHCli $cliArgs } catch { $PSCmdlet.WriteError($_) }
  }
}

<#
.SYNOPSIS
Adds a web application firewall exclusion to a site, for a false positive.
.DESCRIPTION
Turns rules (or categories) off under a path, or stops them inspecting
named arguments, cookies or headers. With only -Path, the firewall is off
under that path.
.PARAMETER Name
The site's name or ID.
.PARAMETER Path
Path prefix; the whole site if not given.
.PARAMETER Rule
Rule IDs to turn off.
.PARAMETER Category
Categories to turn off: sqli, xss, lfi, rfi, rce, nodejs, php, java, scanner, protocol.
.PARAMETER Argument
Form fields, query string arguments or JSON keys (dotted: post.body) not to inspect. A trailing * matches a prefix.
.PARAMETER Cookie
Cookies not to inspect.
.PARAMETER Header
Headers not to inspect.
.PARAMETER Comment
Why the exclusion exists.
.EXAMPLE
Add-NHWafExclusion blog -Path /wp-admin/ -Argument content -Comment 'The editor posts HTML'
.EXAMPLE
Add-NHWafExclusion api -Path /webhooks/stripe -Rule 942100, 942190
#>
function Add-NHWafExclusion {
  [CmdletBinding(SupportsShouldProcess)]
  param(
    [Parameter(Mandatory, Position = 0, ValueFromPipelineByPropertyName)]
    [Alias('SiteName')]
    [string] $Name,
    [string] $Path,
    [int[]] $Rule,
    [ValidateSet('sqli', 'xss', 'lfi', 'rfi', 'rce', 'nodejs', 'php', 'java', 'scanner', 'protocol')]
    [string[]] $Category,
    [string[]] $Argument,
    [string[]] $Cookie,
    [string[]] $Header,
    [string] $Comment
  )
  process {
    if (-not $PSCmdlet.ShouldProcess($Name, 'add a web application firewall exclusion')) { return }
    $cliArgs = @('waf', 'exclude', $Name)
    if ($Path) { $cliArgs += @('--path', $Path) }
    foreach ($r in $Rule) { $cliArgs += @('--rule', "$r") }
    foreach ($c in $Category) { $cliArgs += @('--category', $c) }
    foreach ($a in $Argument) { $cliArgs += @('--arg', $a) }
    foreach ($c in $Cookie) { $cliArgs += @('--cookie', $c) }
    foreach ($h in $Header) { $cliArgs += @('--header', $h) }
    if ($Comment) { $cliArgs += @('--comment', $Comment) }
    try { Invoke-NHCli $cliArgs } catch { $PSCmdlet.WriteError($_) }
  }
}

Export-ModuleMember -Function Get-NHSite, Start-NHSite, Stop-NHSite, Restart-NHSite, Invoke-NHRecycle,
  Publish-NHSite, Get-NHRelease, Undo-NHDeployment, Get-NHLog, Get-NHEvent, Get-NHCertificate,
  Get-NHTask, Start-NHTask, Get-NHTaskRun, Start-NHBackup,
  Update-NHCertificateOcsp, Get-NHTlsSetting, Set-NHTlsSetting,
  Get-NHPreview, Publish-NHPreview, Remove-NHPreview,
  Connect-NHServer, Disconnect-NHServer, Get-NHServer,
  Get-NHAlert, Set-NHAlertSilence, Clear-NHAlertSilence,
  Get-NHSecretStore, Test-NHSecretReference,
  Get-NHSlot, Switch-NHSlot,
  Get-NHRuntime, Install-NHRuntime,
  Get-NHWafEvent, Set-NHWafMode, Add-NHWafExclusion
