param(
    [Parameter(Mandatory = $true)][string]$ArtifactDir,
    [Parameter(Mandatory = $true)][string]$SourceDir
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $SourceDir 'dev\winvm\package-lifecycle-common.ps1')

$programFiles = Join-Path $env:ProgramFiles 'Almagest'
$programData = Join-Path $env:ProgramData 'Almagest'
$cli = Join-Path $programFiles 'almagest-cli.exe'
$base = Get-Content -Raw (Join-Path $SourceDir 'dev\winvm\package-manifest.json') | ConvertFrom-Json
$upgrade = Get-Content -Raw (Join-Path $SourceDir 'dev\winvm\package-upgrade-manifest.json') | ConvertFrom-Json
Assert-True (Test-Path (Join-Path $ArtifactDir $base.output)) 'Base lifecycle MSI is absent'
Assert-True (Test-Path (Join-Path $ArtifactDir $upgrade.output)) 'Upgrade lifecycle MSI is absent'

Invoke-PackageCli $cli @('snapshots', 'load', '001-hello-network') | Out-Null
Add-Content -LiteralPath (Join-Path $programData 'alm.hujson') -Value "`r`n// package-lifecycle-regular"
Add-Content -LiteralPath (Join-Path $programData 'protected_alm.hujson') -Value "`r`n// package-lifecycle-protected"
Invoke-PackageCli $cli @('kv', 'set', '--key', 'package-lifecycle', '--data', 'retained-kv-value') | Out-Null
Invoke-PackageCli $cli @('secrets', 'set', '--key', 'package-lifecycle', '--value', 'retained-secret-value') | Out-Null
Invoke-PackageCli $cli @('snapshots', 'create', '--name', 'Package lifecycle retained snapshot') | Out-Null
Start-Sleep -Seconds 2

$beforeRestart = Wait-AlmagestService
Stop-Service almagestd
$stopped = Get-Service almagestd
Assert-True ($stopped.Status -eq 'Stopped') 'SCM stop did not stop almagestd'
Start-Service almagestd
$afterRestart = Wait-AlmagestService -DifferentProcessId $beforeRestart.ProcessId
Assert-LifecycleState $cli $programData

$record = [ordered]@{
    schemaVersion = 1
    suite = 'windows-msi-package-lifecycle-prepare'
    outcome = 'passed'
    baseVersion = $base.version
    baseProductCode = $base.productCode
    upgradeVersion = $upgrade.version
    upgradeProductCode = $upgrade.productCode
    bootTimeUtcTicks = (Get-CimInstance Win32_OperatingSystem).LastBootUpTime.ToUniversalTime().Ticks
    serviceRestart = [ordered]@{
        stopped = $true
        processIdBefore = $beforeRestart.ProcessId
        processIdAfter = $afterRestart.ProcessId
    }
    configSHA256 = Get-FileSHA256 (Join-Path $programData 'alm.hujson')
    protectedConfigSHA256 = Get-FileSHA256 (Join-Path $programData 'protected_alm.hujson')
    stateCreated = [ordered]@{ kv = $true; secret = $true; snapshot = $true; graph = $true }
}
$record | ConvertTo-Json -Depth 6 | Set-Content `
    (Join-Path $ArtifactDir 'package-lifecycle-prepare.json') -Encoding utf8
