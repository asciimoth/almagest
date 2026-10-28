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
$adminGroupName = 'Almagest Administrators'
$driverName = 'mullvad-split-tunnel'
$driverSystemPath = Join-Path $env:SystemRoot 'System32\drivers\mullvad-split-tunnel.sys'
$driverStore = Join-Path $env:SystemRoot 'System32\DriverStore\FileRepository'
$base = Get-Content -Raw (Join-Path $SourceDir 'dev\winvm\package-manifest.json') | ConvertFrom-Json
$upgrade = Get-Content -Raw (Join-Path $SourceDir 'dev\winvm\package-upgrade-manifest.json') | ConvertFrom-Json
$contract = Get-Content -Raw (Join-Path $SourceDir 'packaging\windows\contract.json') | ConvertFrom-Json
$prepare = Get-Content -Raw (Join-Path $ArtifactDir 'package-lifecycle-prepare.json') | ConvertFrom-Json
$baseMsi = Join-Path $ArtifactDir $base.output
$upgradeMsi = Join-Path $ArtifactDir $upgrade.output

$bootTicks = (Get-CimInstance Win32_OperatingSystem).LastBootUpTime.ToUniversalTime().Ticks
Assert-True ($bootTicks -ne $prepare.bootTimeUtcTicks) 'The lifecycle reboot did not change the Windows boot identity'
$afterBoot = Wait-AlmagestService
Assert-True ((Get-ItemProperty 'HKLM:\SYSTEM\CurrentControlSet\Services\almagestd').DelayedAutoStart -eq 0) `
    'Immediate automatic start was not retained after reboot'
Assert-LifecycleState $cli $programData

$recoveryPid = $afterBoot.ProcessId
Stop-Process -Id $recoveryPid -Force
$afterRecovery = Wait-AlmagestService -TimeoutSeconds 120 -DifferentProcessId $recoveryPid
Assert-LifecycleState $cli $programData

$repairLog = Join-Path $ArtifactDir 'msiexec-repair.log'
$repairExit = Invoke-MSI @(
    '/fvomus', ('"{0}"' -f $baseMsi), '/qn', '/norestart', '/l*v', ('"{0}"' -f $repairLog)
) 'Same-version repair'
$afterRepair = Wait-AlmagestService
Assert-LifecycleState $cli $programData
Assert-True ((Get-FileSHA256 (Join-Path $programData 'alm.hujson')) -eq $prepare.configSHA256) `
    'Repair changed the regular configuration'
Assert-True ((Get-FileSHA256 (Join-Path $programData 'protected_alm.hujson')) -eq $prepare.protectedConfigSHA256) `
    'Repair changed the protected configuration'

$beforeUpgradePid = $afterRepair.ProcessId
$upgradeLog = Join-Path $ArtifactDir 'msiexec-upgrade.log'
$upgradeExit = Invoke-MSI @(
    '/i', ('"{0}"' -f $upgradeMsi), '/qn', '/norestart', '/l*v', ('"{0}"' -f $upgradeLog)
) 'Major upgrade'
$afterUpgrade = Wait-AlmagestService -DifferentProcessId $beforeUpgradePid
$uninstallKey = 'HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall'
$upgradeRegistration = Get-ItemProperty (Join-Path $uninstallKey "{$($upgrade.productCode)}") -ErrorAction SilentlyContinue
Assert-True ($upgradeRegistration.DisplayVersion -eq $upgrade.version) 'Upgrade product registration is absent'
Assert-True (-not (Test-Path (Join-Path $uninstallKey "{$($base.productCode)}"))) 'Base product registration remains after upgrade'
$upgradeLogText = Get-Content -Raw $upgradeLog
foreach ($action in @('StopServices', 'RemoveExistingProducts', 'StartServices')) {
    Assert-True ($upgradeLogText -match $action) "Upgrade log does not contain $action"
}
Assert-LifecycleState $cli $programData
Assert-True ((Get-FileSHA256 (Join-Path $programData 'alm.hujson')) -eq $prepare.configSHA256) `
    'Upgrade changed the regular configuration'
Assert-True ((Get-FileSHA256 (Join-Path $programData 'protected_alm.hujson')) -eq $prepare.protectedConfigSHA256) `
    'Upgrade changed the protected configuration'

$downgradeLog = Join-Path $ArtifactDir 'msiexec-downgrade.log'
$downgrade = Start-Process msiexec.exe -ArgumentList @(
    '/i', ('"{0}"' -f $baseMsi), '/qn', '/norestart', '/l*v', ('"{0}"' -f $downgradeLog)
) -Wait -PassThru
Assert-True ($downgrade.ExitCode -notin @(0, 3010)) 'Downgrade was accepted'
$registrationAfterDowngrade = Get-ItemProperty (Join-Path $uninstallKey "{$($upgrade.productCode)}") -ErrorAction SilentlyContinue
Assert-True ($registrationAfterDowngrade.DisplayVersion -eq $upgrade.version) 'Downgrade damaged product registration'
Assert-LifecycleState $cli $programData

$featureChangeLog = Join-Path $ArtifactDir 'msiexec-feature-change.log'
$featureChangeExit = Invoke-MSI @(
    '/i', ('"{0}"' -f $upgradeMsi),
    'ADDLOCAL=AlmagestFeature,BackendFeature,DesktopAppFeature',
    'REMOVE=DesktopAppAutoStartFeature',
    '/qn', '/norestart', '/l*v', ('"{0}"' -f $featureChangeLog)
) 'Disable desktop app autostart'
Assert-True (Test-Path -LiteralPath (Join-Path $programFiles 'almagest-app.exe')) `
    'Disabling autostart removed the desktop app'
Assert-True (-not (Get-ItemProperty 'HKLM:\Software\Microsoft\Windows\CurrentVersion\Run' `
    -Name Almagest -ErrorAction SilentlyContinue)) 'Desktop app autostart remains after feature removal'

$uninstallLog = Join-Path $ArtifactDir 'msiexec-uninstall.log'
$uninstallExit = Invoke-MSI @(
    '/x', ('"{0}"' -f $upgradeMsi), '/qn', '/norestart', '/l*v', ('"{0}"' -f $uninstallLog)
) 'Uninstall'
Assert-True (-not (Get-Service almagestd -ErrorAction SilentlyContinue)) 'Daemon service remains after uninstall'
Assert-True (-not (Test-Path -LiteralPath $programFiles)) 'Program Files payload remains after uninstall'
$pathAfterUninstall = (Get-ItemProperty `
    'HKLM:\SYSTEM\CurrentControlSet\Control\Session Manager\Environment').Path
$pathEntriesAfterUninstall = @($pathAfterUninstall -split ';' | ForEach-Object { $_.TrimEnd('\') })
Assert-True ($pathEntriesAfterUninstall -notcontains $programFiles) `
    'Uninstall retained the Almagest machine PATH entry'
Assert-True (-not (Get-Service $driverName -ErrorAction SilentlyContinue)) 'Driver service remains after uninstall'
Assert-True (-not (Test-Path -LiteralPath $driverSystemPath)) 'System driver remains after uninstall'
$driverHash = $contract.drivers.mullvadSplitTunnel.files.'mullvad-split-tunnel.sys'.sha256
$retainedDriver = @(Get-ChildItem $driverStore -Filter 'mullvad-split-tunnel.sys' -File -Recurse -ErrorAction SilentlyContinue |
    Where-Object { (Get-FileSHA256 $_.FullName) -eq $driverHash })
Assert-True ($retainedDriver.Count -eq 0) 'Mullvad driver package remains in the driver store after uninstall'
Assert-True (Test-Path -LiteralPath $programData) 'ProgramData was removed during uninstall'
Assert-True ((Get-FileSHA256 (Join-Path $programData 'alm.hujson')) -eq $prepare.configSHA256) `
    'Uninstall changed the regular configuration'
Assert-True ((Get-FileSHA256 (Join-Path $programData 'protected_alm.hujson')) -eq $prepare.protectedConfigSHA256) `
    'Uninstall changed the protected configuration'
$retainedAdministrators = @(Get-LocalGroupMember -Group $adminGroupName)
Assert-True ($retainedAdministrators.Name -contains $prepare.administrationGroupMember) `
    'Uninstall removed the Almagest administration group or its delegated member'

$reinstallLog = Join-Path $ArtifactDir 'msiexec-reinstall.log'
$reinstallExit = Invoke-MSI @(
    '/i', ('"{0}"' -f $upgradeMsi), 'ADDLOCAL=AlmagestFeature,BackendFeature',
    '/qn', '/norestart', '/l*v', ('"{0}"' -f $reinstallLog)
) 'Service-only reinstall'
$afterReinstall = Wait-AlmagestService
Assert-LifecycleState $cli $programData
$pathAfterReinstall = (Get-ItemProperty `
    'HKLM:\SYSTEM\CurrentControlSet\Control\Session Manager\Environment').Path
$pathEntriesAfterReinstall = @($pathAfterReinstall -split ';' | ForEach-Object { $_.TrimEnd('\') })
Assert-True ($pathEntriesAfterReinstall -contains $programFiles) `
    'Service-only reinstall did not restore the Almagest machine PATH entry'
Assert-True ([bool](Get-Service $driverName -ErrorAction SilentlyContinue)) 'Driver service is absent after reinstall'
Assert-True (Test-Path -LiteralPath $driverSystemPath) 'System driver is absent after reinstall'
Assert-True (-not (Test-Path -LiteralPath (Join-Path $programFiles 'almagest-app.exe'))) `
    'Service-only reinstall installed the desktop app'
Assert-True (-not (Test-Path -LiteralPath (Join-Path $programFiles 'WebView2Loader.dll'))) `
    'Service-only reinstall installed the WebView2 loader'
Assert-True (-not (Test-Path -LiteralPath `
    (Join-Path $env:ProgramData 'Microsoft\Windows\Start Menu\Programs\Almagest.lnk'))) `
    'Service-only reinstall created a Start menu shortcut'
Assert-True (-not (Test-Path -LiteralPath (Join-Path $env:Public 'Desktop\Almagest.lnk'))) `
    'Service-only reinstall created a desktop shortcut'
Assert-True (-not (Get-ItemProperty 'HKLM:\Software\Microsoft\Windows\CurrentVersion\Run' `
    -Name Almagest -ErrorAction SilentlyContinue)) 'Service-only reinstall created desktop app autostart'

Get-WinEvent -FilterHashtable @{LogName='System'; ProviderName='Service Control Manager'; StartTime=(Get-Date).AddMinutes(-30)} `
    -ErrorAction SilentlyContinue | Format-List TimeCreated, Id, LevelDisplayName, Message |
    Out-File (Join-Path $ArtifactDir 'package-lifecycle-events.txt') -Encoding utf8
Copy-Item (Join-Path $programData 'logs\almagestd.log') `
    (Join-Path $ArtifactDir 'package-lifecycle-daemon.log') -Force
[ordered]@{
    schemaVersion = 1
    suite = 'windows-msi-package-lifecycle'
    outcome = 'passed'
    baseVersion = $base.version
    upgradeVersion = $upgrade.version
    reboot = [ordered]@{ passed = $true; bootTimeUtcTicks = $bootTicks; serviceProcessId = $afterBoot.ProcessId }
    failureRecovery = [ordered]@{ passed = $true; processIdBefore = $recoveryPid; processIdAfter = $afterRecovery.ProcessId }
    repair = [ordered]@{ passed = $true; exitCode = $repairExit }
    upgrade = [ordered]@{ passed = $true; exitCode = $upgradeExit; processIdBefore = $beforeUpgradePid; processIdAfter = $afterUpgrade.ProcessId }
    downgrade = [ordered]@{ rejected = $true; exitCode = $downgrade.ExitCode }
    featureSelection = [ordered]@{
        passed = $true; exitCode = $featureChangeExit; appRetained = $true; autoStartRemoved = $true
    }
    uninstall = [ordered]@{
        passed = $true; exitCode = $uninstallExit; programDataRetained = $true
        driverRemoved = $true; administrationGroupRetained = $true
    }
    reinstall = [ordered]@{
        passed = $true; exitCode = $reinstallExit; retainedStateUsable = $true
        serviceProcessId = $afterReinstall.ProcessId; serviceOnly = $true
    }
    retained = [ordered]@{ config = $true; protectedConfig = $true; kv = $true; secret = $true; snapshot = $true; graph = $true }
} | ConvertTo-Json -Depth 8 | Set-Content `
    (Join-Path $ArtifactDir 'package-lifecycle.json') -Encoding utf8
