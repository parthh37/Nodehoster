#
# Module manifest for NodeHoster: PowerShell commands for the NodeHoster
# service on this computer. They wrap `nodehoster.exe --json`, which talks
# to the service over its local admin pipe, so they need an elevated
# session (Run as administrator), like NodeHoster Manager.
#
@{
  RootModule = 'NodeHoster.psm1'
  # Set by setup to the installed version (the folder name must match).
  ModuleVersion = '0.0.0'
  GUID = '5a98ca11-a72d-43c6-8330-540558b31ec7'
  Author = 'NodeHoster'
  CompanyName = 'NodeHoster'
  Copyright = '(c) NodeHoster contributors'
  Description = 'Manage NodeHoster sites, deployments, preview deployments, deployment slots, logs, events, certificates, scheduled tasks, backups, alerts and runtimes on this computer.'
  PowerShellVersion = '5.1'
  CompatiblePSEditions = @('Desktop', 'Core')
  FunctionsToExport = @(
    'Get-NHSite', 'Start-NHSite', 'Stop-NHSite', 'Restart-NHSite', 'Invoke-NHRecycle',
    'Publish-NHSite', 'Get-NHRelease', 'Undo-NHDeployment',
    'Get-NHLog', 'Get-NHEvent', 'Get-NHCertificate',
    'Get-NHTask', 'Start-NHTask', 'Get-NHTaskRun', 'Start-NHBackup',
    'Update-NHCertificateOcsp', 'Get-NHTlsSetting', 'Set-NHTlsSetting',
    'Get-NHPreview', 'Publish-NHPreview', 'Remove-NHPreview',
    'Connect-NHServer', 'Disconnect-NHServer', 'Get-NHServer',
    'Get-NHAlert', 'Set-NHAlertSilence', 'Clear-NHAlertSilence',
    'Get-NHSecretStore', 'Test-NHSecretReference',
    'Get-NHSlot', 'Switch-NHSlot',
    'Get-NHRuntime', 'Install-NHRuntime',
    'Get-NHWafEvent', 'Set-NHWafMode', 'Add-NHWafExclusion'
  )
  CmdletsToExport = @()
  VariablesToExport = @()
  AliasesToExport = @()
  PrivateData = @{
    PSData = @{
      Tags = @('NodeHoster', 'Node.js', 'IIS', 'Hosting')
      ProjectUri = 'https://github.com/parthh37/Nodehoster'
    }
  }
}
