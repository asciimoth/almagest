param(
    [Parameter(Mandatory = $true)][string]$ArtifactDir,
    [Parameter(Mandatory = $true)][string]$SourceDir
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Assert-True([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw $Message }
}
function Get-FileSHA256([string]$Path) {
    (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash.ToLowerInvariant()
}
function Get-PEMachine([string]$Path) {
    $bytes = [IO.File]::ReadAllBytes($Path)
    Assert-True ($bytes.Length -ge 64) "PE file is too short: $Path"
    $offset = [BitConverter]::ToInt32($bytes, 60)
    Assert-True ($offset -ge 0 -and $offset + 6 -le $bytes.Length) "Invalid PE header: $Path"
    Assert-True ([BitConverter]::ToUInt32($bytes, $offset) -eq 0x00004550) "Invalid PE signature: $Path"
    '0x{0:x4}' -f [BitConverter]::ToUInt16($bytes, $offset + 4)
}
function Get-SignatureRecord([string]$Path, [string]$SubjectPattern = '') {
    $signature = Get-AuthenticodeSignature -LiteralPath $Path
    Assert-True ($signature.Status -eq 'Valid') "Invalid Authenticode signature: $Path"
    if ($SubjectPattern) {
        Assert-True ($signature.SignerCertificate.Subject -like $SubjectPattern) "Unexpected signer: $Path"
    }
    [ordered]@{
        path = $Path
        status = $signature.Status.ToString()
        subject = $signature.SignerCertificate.Subject
        thumbprint = $signature.SignerCertificate.Thumbprint
        sha256 = Get-FileSHA256 $Path
    }
}

New-Item -ItemType Directory -Force $ArtifactDir | Out-Null
$manifest = Get-Content -Raw (Join-Path $SourceDir 'dev\winvm\package-manifest.json') | ConvertFrom-Json
$contract = Get-Content -Raw (Join-Path $SourceDir 'packaging\windows\contract.json') | ConvertFrom-Json
$image = Get-Content -Raw 'C:\winvm\manifest.json' | ConvertFrom-Json
Assert-True ($image.imageVariant -eq 'package-clean') 'Package smoke test requires the package-clean image'
Assert-True (-not $image.runtimeDependenciesInstalled) 'Package-clean image has provisioned runtime dependencies'

$programFiles = Join-Path $env:ProgramFiles 'Almagest'
$programData = Join-Path $env:ProgramData 'Almagest'
$driverName = 'mullvad-split-tunnel'
$adminGroupName = 'Almagest Administrators'
$delegatedAdministrator = "$env:COMPUTERNAME\winvm"
$delegatedSid = ([System.Security.Principal.NTAccount]$delegatedAdministrator).Translate(
    [System.Security.Principal.SecurityIdentifier]).Value
$driverSystemPath = Join-Path $env:SystemRoot 'System32\drivers\mullvad-split-tunnel.sys'
$pipePath = '\\.\pipe\almagest-admin'
$preconditions = [ordered]@{
    programFilesAbsent = -not (Test-Path -LiteralPath $programFiles)
    programDataAbsent = -not (Test-Path -LiteralPath $programData)
    daemonServiceAbsent = -not [bool](Get-Service almagestd -ErrorAction SilentlyContinue)
    driverServiceAbsent = -not [bool](Get-Service $driverName -ErrorAction SilentlyContinue)
    systemDriverAbsent = -not (Test-Path -LiteralPath $driverSystemPath)
    adminPipeAbsent = -not (Test-Path -LiteralPath $pipePath)
    administrationGroupAbsent = -not [bool](Get-LocalGroup -Name $adminGroupName -ErrorAction SilentlyContinue)
}
foreach ($entry in $preconditions.GetEnumerator()) {
    Assert-True $entry.Value "Clean-image precondition failed: $($entry.Key)"
}
$driverStoreBefore = @(Get-ChildItem (Join-Path $env:SystemRoot 'System32\DriverStore\FileRepository') `
    -Filter 'mullvad-split-tunnel.sys' -File -Recurse -ErrorAction SilentlyContinue)
Assert-True ($driverStoreBefore.Count -eq 0) 'Target driver is already present in the driver store'

$msi = Join-Path $ArtifactDir $manifest.output
Assert-True (Test-Path -LiteralPath $msi -PathType Leaf) 'Built MSI is absent'
$installLog = Join-Path $ArtifactDir 'msiexec-install.log'
$installer = Start-Process msiexec.exe -ArgumentList @(
    '/i', ('"{0}"' -f $msi), "ALMAGEST_ACCESS_SID=$delegatedSid",
    '/qn', '/norestart', '/l*v', ('"{0}"' -f $installLog)
) -Wait -PassThru
Assert-True ($installer.ExitCode -in @(0, 3010)) "MSI install failed: $($installer.ExitCode)"

$deadline = [DateTime]::UtcNow.AddMinutes(2)
do {
    $service = Get-CimInstance Win32_Service -Filter "Name='almagestd'" -ErrorAction SilentlyContinue
    if ($service -and $service.State -eq 'Running') { break }
    Start-Sleep -Seconds 1
} while ([DateTime]::UtcNow -lt $deadline)
Assert-True ($service -and $service.State -eq 'Running') 'almagestd did not reach Running'
$adminGroup = Get-LocalGroup -Name $adminGroupName
Assert-True ([bool]$adminGroup) 'Almagest administration group is absent'
$adminGroupMembers = @(Get-LocalGroupMember -Group $adminGroupName)
Assert-True ($adminGroupMembers.Count -eq 1) `
    'Default installer option did not add exactly one administration group member'
Assert-True ($adminGroupMembers[0].SID.Value -eq $delegatedSid) `
    'Default installer option did not add the selected current user'
$pipeAcl = $null
$pipeAclDeadline = [DateTime]::UtcNow.AddMinutes(2)
do {
    $pipeClient = $null
    try {
        $pipeClient = New-Object System.IO.Pipes.NamedPipeClientStream(
            '.', 'almagest-admin', [System.IO.Pipes.PipeDirection]::InOut,
            [System.IO.Pipes.PipeOptions]::None)
        $pipeClient.Connect(1000)
        $pipeAcl = $pipeClient.GetAccessControl()
    } catch {
        Start-Sleep -Milliseconds 100
    } finally {
        if ($pipeClient) { $pipeClient.Dispose() }
    }
} while (-not $pipeAcl -and [DateTime]::UtcNow -lt $pipeAclDeadline)
Assert-True ([bool]$pipeAcl) 'Machine administration pipe ACL is unavailable'
Assert-True ($pipeAcl.Sddl -match [regex]::Escape($adminGroup.SID.Value)) `
    'Machine administration pipe does not grant the Almagest administration group'

$inputHashes = @{}
foreach ($inputFile in $manifest.inputs.files) { $inputHashes[$inputFile.path] = $inputFile.sha256 }
$expected = [ordered]@{
    'almagestd.exe' = $inputHashes['dev/winvm/almagestd.exe']
    'almagest-cli.exe' = $inputHashes['dev/winvm/almagest-cli.exe']
    'almagest-app.exe' = $inputHashes['dev/winvm/almagest-app.exe']
    'WebView2Loader.dll' = $inputHashes['dev/winvm/WebView2Loader.dll']
    'wintun.dll' = $inputHashes['dev/winvm/package-inputs/wintun/amd64/wintun.dll']
    'LICENSE.txt' = $inputHashes['LICENSE']
    'WINTUN-LICENSE.txt' = $inputHashes['dev/winvm/package-inputs/wintun/licenses/wintun-LICENSE.txt']
    'MULLVAD-LICENSE-MPL.txt' = $inputHashes['dev/winvm/package-inputs/split-driver/licenses/mullvad-LICENSE-MPL.txt']
    'drivers\mullvad-split-tunnel\mullvad-split-tunnel.inf' = $inputHashes['dev/winvm/package-inputs/split-driver/amd64/mullvad-split-tunnel.inf']
    'drivers\mullvad-split-tunnel\mullvad-split-tunnel.sys' = $inputHashes['dev/winvm/package-inputs/split-driver/amd64/mullvad-split-tunnel.sys']
    'drivers\mullvad-split-tunnel\mullvad-split-tunnel.cat' = $inputHashes['dev/winvm/package-inputs/split-driver/amd64/mullvad-split-tunnel.cat']
}
foreach ($inputFile in $manifest.inputs.files) {
    if ($inputFile.path -match '^(locales|examples)/') {
        $expected[$inputFile.path.Replace('/', '\')] = $inputFile.sha256
    }
}
Assert-True (-not (Test-Path -LiteralPath (Join-Path $programFiles 'PACKAGE-README.md'))) `
    'Package build guide is installed'
Assert-True (-not (Test-Path -LiteralPath (Join-Path $programFiles 'locales\README.md'))) `
    'Locale source guide is installed'
$installedFiles = @()
foreach ($entry in $expected.GetEnumerator()) {
    Assert-True ([bool]$entry.Value) "No package input hash for $($entry.Key)"
    $path = Join-Path $programFiles $entry.Key
    Assert-True (Test-Path -LiteralPath $path -PathType Leaf) "Installed file is absent: $path"
    $digest = Get-FileSHA256 $path
    Assert-True ($digest -eq $entry.Value) "Installed file digest differs: $path"
    $versionInfo = (Get-Item -LiteralPath $path).VersionInfo
    $installedFiles += [ordered]@{
        path = $entry.Key.Replace('\', '/')
        sha256 = $digest
        fileVersion = $versionInfo.FileVersion
        productVersion = $versionInfo.ProductVersion
    }
}
$actualFiles = @(Get-ChildItem -LiteralPath $programFiles -File -Recurse | ForEach-Object {
    $_.FullName.Substring($programFiles.Length + 1)
})
Assert-True ($actualFiles.Count -eq $expected.Count) 'Program Files contains an unexpected file count'
foreach ($path in $actualFiles) { Assert-True $expected.Contains($path) "Unexpected installed file: $path" }

$appPath = Join-Path $programFiles 'almagest-app.exe'
Assert-True ((Get-PEMachine $appPath) -eq '0x8664') 'Desktop app is not an amd64 PE executable'
$webViewLoader = Join-Path $programFiles 'WebView2Loader.dll'
Assert-True ((Get-PEMachine $webViewLoader) -eq '0x8664') 'WebView2 loader is not an amd64 PE executable'
$appHelp = Start-Process $appPath -ArgumentList '--help' -Wait -PassThru
Assert-True ($appHelp.ExitCode -eq 0) `
    'Desktop app cannot start with the packaged WebView2 loader'
$autoStart = (Get-ItemProperty 'HKLM:\Software\Microsoft\Windows\CurrentVersion\Run').Almagest
$expectedAutoStart = '"{0}" --background' -f $appPath
Assert-True ($autoStart -eq $expectedAutoStart) "Unexpected desktop app autostart command: $autoStart"
$startMenuShortcut = Join-Path $env:ProgramData 'Microsoft\Windows\Start Menu\Programs\Almagest.lnk'
$desktopShortcut = Join-Path $env:Public 'Desktop\Almagest.lnk'
$shell = New-Object -ComObject WScript.Shell
foreach ($shortcutPath in @($startMenuShortcut, $desktopShortcut)) {
    Assert-True (Test-Path -LiteralPath $shortcutPath -PathType Leaf) "Shortcut is absent: $shortcutPath"
    $shortcut = $shell.CreateShortcut($shortcutPath)
    Assert-True ($shortcut.TargetPath -eq $appPath) "Shortcut target differs: $shortcutPath"
}
$machinePath = (Get-ItemProperty `
    'HKLM:\SYSTEM\CurrentControlSet\Control\Session Manager\Environment').Path
$machinePathEntries = @($machinePath -split ';' | ForEach-Object { $_.TrimEnd('\') })
Assert-True ($machinePathEntries -contains $programFiles) `
    'Almagest install directory is absent from the machine PATH'

$wintunPath = Join-Path $programFiles 'wintun.dll'
Assert-True ((Get-PEMachine $wintunPath) -eq $contract.drivers.wintun.peMachine) 'Wintun architecture differs'
Assert-True ((Get-Item $wintunPath).VersionInfo.FileVersion -like "$($contract.drivers.wintun.version)*") 'Wintun version differs'
$signatures = @(
    Get-SignatureRecord $wintunPath '*CN=WireGuard LLC*'
    Get-SignatureRecord (Join-Path $programFiles 'drivers\mullvad-split-tunnel\mullvad-split-tunnel.sys')
    Get-SignatureRecord (Join-Path $programFiles 'drivers\mullvad-split-tunnel\mullvad-split-tunnel.cat')
)
$driverService = Get-CimInstance Win32_SystemDriver -Filter "Name='$driverName'"
Assert-True ($driverService -and $driverService.StartMode -eq 'Manual') 'Split driver is not installed as demand-start'
Assert-True (Test-Path -LiteralPath $driverSystemPath -PathType Leaf) 'Installed split driver is absent'
Assert-True ((Get-FileSHA256 $driverSystemPath) -eq $contract.drivers.mullvadSplitTunnel.files.'mullvad-split-tunnel.sys'.sha256) 'System driver digest differs'
Assert-True ((Get-Item $driverSystemPath).VersionInfo.FileVersion -like "$($contract.drivers.mullvadSplitTunnel.version)*") 'System driver version differs'
$signatures += Get-SignatureRecord $driverSystemPath
$driverStoreFiles = @(Get-ChildItem (Join-Path $env:SystemRoot 'System32\DriverStore\FileRepository') `
    -File -Recurse -ErrorAction SilentlyContinue | Where-Object Name -in @(
        'mullvad-split-tunnel.sys', 'mullvad-split-tunnel.inf', 'mullvad-split-tunnel.cat'))
foreach ($name in @('mullvad-split-tunnel.sys', 'mullvad-split-tunnel.inf', 'mullvad-split-tunnel.cat')) {
    $matching = @($driverStoreFiles | Where-Object Name -eq $name | Where-Object {
        (Get-FileSHA256 $_.FullName) -eq $contract.drivers.mullvadSplitTunnel.files.$name.sha256
    })
    Assert-True ($matching.Count -ge 1) "Matching $name is absent from the driver store"
}

$expectedPath = '"{0}" --isolation=off' -f (Join-Path $programFiles 'almagestd.exe')
Assert-True ($service.PathName -eq $expectedPath) "Unexpected service command: $($service.PathName)"
Assert-True ($service.StartName -eq 'LocalSystem') 'Service account is not LocalSystem'
Assert-True ($service.StartMode -eq 'Auto') 'Service startup type is not automatic'
Assert-True ($service.DisplayName -eq 'Almagest Network Daemon') 'Service display name differs'
Assert-True ($service.Description -eq 'Almagest network daemon') 'Service description differs'
$serviceRegistry = 'HKLM:\SYSTEM\CurrentControlSet\Services\almagestd'
Assert-True ((Get-ItemProperty $serviceRegistry).DelayedAutoStart -eq 0) 'Immediate automatic start is not enabled'
Assert-True ((Get-ItemProperty $serviceRegistry).FailureActionsOnNonCrashFailures -eq 1) 'Recovery does not include error exits'
$failureActions = (& sc.exe qfailure almagestd | Out-String)
Assert-True ($LASTEXITCODE -eq 0) 'Cannot query service recovery actions'
foreach ($delay in @(5000, 15000, 60000)) {
    Assert-True ($failureActions -match "(?s)RESTART.+?Delay\s*=\s*$delay") "Recovery delay is absent: $delay"
}
Assert-True ($failureActions -match 'RESET_PERIOD \(in seconds\)\s*:\s*86400') 'Recovery reset period differs'

$uninstall = Get-ItemProperty "HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall\{$($manifest.productCode)}" -ErrorAction SilentlyContinue
Assert-True ($uninstall.DisplayVersion -eq $manifest.version) 'Installed product version differs'

$requiredData = @('alm.hujson', 'protected_alm.hujson', 'store', 'logs')
foreach ($name in $requiredData) { Assert-True (Test-Path -LiteralPath (Join-Path $programData $name)) "ProgramData item is absent: $name" }
$aclRecords = @()
foreach ($path in @($programFiles, $programData, (Join-Path $programData 'alm.hujson'),
        (Join-Path $programData 'protected_alm.hujson'), (Join-Path $programData 'store'),
        (Join-Path $programData 'logs'))) {
    $acl = Get-Acl -LiteralPath $path
    $aclRecords += [ordered]@{ path = $path; sddl = $acl.Sddl; access = $acl.AccessToString }
    $unsafe = @($acl.Access | Where-Object {
        $_.AccessControlType -eq 'Allow' -and $_.IdentityReference.Value -match '(BUILTIN\\Users|Authenticated Users)' -and
        ($_.FileSystemRights.ToString() -match '(Write|Modify|FullControl|CreateFiles|Delete)')
    })
    Assert-True ($unsafe.Count -eq 0) "Ordinary users have write access: $path"
}

$cliOutput = & (Join-Path $programFiles 'almagest-cli.exe') --url pipe://machine admin ping --nonce package-smoke 2>&1 | Out-String
Assert-True ($LASTEXITCODE -eq 0 -and $cliOutput -match 'package-smoke') 'Elevated CLI cannot use pipe://machine'
$logPath = Join-Path $programData 'logs\almagestd.log'
Assert-True (Test-Path -LiteralPath $logPath -PathType Leaf) 'Durable service log is absent'
$logContent = Get-Content -Raw -LiteralPath $logPath
Assert-True ($logContent -match 'Starting Windows pipe admin listener') 'Normal startup is absent from durable log'

$serviceRecord = [ordered]@{
    name = $service.Name; displayName = $service.DisplayName; description = $service.Description
    state = $service.State; startMode = $service.StartMode; account = $service.StartName
    pathName = $service.PathName; delayedAutoStart = 0; failureActions = $failureActions.Trim()
}
$serviceRecord | ConvertTo-Json -Depth 4 | Set-Content (Join-Path $ArtifactDir 'service-after-install.json') -Encoding utf8
$installedFiles | ConvertTo-Json -Depth 4 | Set-Content (Join-Path $ArtifactDir 'installed-files.json') -Encoding utf8
$aclRecords | ForEach-Object { "PATH: $($_.path)`r`nSDDL: $($_.sddl)`r`n$($_.access)`r`n" } |
    Set-Content (Join-Path $ArtifactDir 'installed-acls.txt') -Encoding utf8
$signatures | ForEach-Object { "PATH: $($_.path)`r`nSTATUS: $($_.status)`r`nSUBJECT: $($_.subject)`r`nSHA256: $($_.sha256)`r`n" } |
    Set-Content (Join-Path $ArtifactDir 'driver-signatures.txt') -Encoding utf8
Copy-Item $logPath (Join-Path $ArtifactDir 'daemon.log') -Force
Get-WinEvent -FilterHashtable @{LogName='System'; ProviderName='Service Control Manager'; StartTime=(Get-Date).AddMinutes(-10)} -ErrorAction SilentlyContinue |
    Format-List TimeCreated, Id, LevelDisplayName, Message | Out-File (Join-Path $ArtifactDir 'event-log.txt') -Encoding utf8
$evidence = [ordered]@{
    schemaVersion = 1; suite = 'windows-msi-package-smoke'; outcome = 'privileged-passed'
    version = $manifest.version; package = $manifest.output; productCode = $manifest.productCode
    windowsBuild = [Environment]::OSVersion.Version.ToString(); architecture = $env:PROCESSOR_ARCHITECTURE
    imageVariant = $image.imageVariant; preconditions = $preconditions
    installerExitCode = $installer.ExitCode; installedFileCount = $installedFiles.Count
    service = $serviceRecord; machinePipe = 'pipe://machine'; elevatedCli = 'passed'
    desktopApp = [ordered]@{
        installed = $true; loaderPresent = $true; launchPassed = $true
        autoStart = $true; autoStartCommand = $autoStart
        startMenuShortcut = $startMenuShortcut; desktopShortcut = $desktopShortcut
    }
    machinePath = $machinePath
    administrationGroup = [ordered]@{
        name = $adminGroup.Name; sid = $adminGroup.SID.Value
        initialMemberCount = $adminGroupMembers.Count; delegatedAdministrator = $delegatedAdministrator
        pipeAccess = 'passed'
    }
    signatures = $signatures; acls = $aclRecords
}
$evidence | ConvertTo-Json -Depth 8 | Set-Content (Join-Path $ArtifactDir 'package-smoke.json') -Encoding utf8
