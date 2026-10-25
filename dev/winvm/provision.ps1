param([switch]$Install)
$ErrorActionPreference = 'Stop'; Set-StrictMode -Version Latest
$root = 'C:\winvm'; $runs = Join-Path $root 'runs'; $taskName = 'almagest-winvm-provision'
New-Item -ItemType Directory -Force -Path $runs | Out-Null
if (-not $Install) {
    $source = (Get-Volume -FileSystemLabel WINVM_PROVISION).DriveLetter + ':'
    $staging = Join-Path $root 'provision'; New-Item -ItemType Directory -Force -Path $staging | Out-Null
    Copy-Item (Join-Path $source '*') $staging -Recurse -Force
    @'
$ErrorActionPreference = 'Stop'
$root = 'C:\winvm'
$failure = Join-Path $root 'provision.failed'
Remove-Item $failure -Force -ErrorAction SilentlyContinue
try {
    & 'C:\winvm\provision\provision.ps1' -Install *> (Join-Path $root 'provision.log')
} catch {
    $_ | Format-List * -Force | Out-String | Add-Content (Join-Path $root 'provision.log')
    [IO.File]::WriteAllText($failure, $_.Exception.Message)
    exit 1
}
'@ | Set-Content (Join-Path $staging 'run-provision.ps1') -Encoding ascii
    $action = New-ScheduledTaskAction powershell.exe "-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File `"$staging\run-provision.ps1`""
    $trigger = New-ScheduledTaskTrigger -AtStartup; $trigger.Delay = 'PT1M'
    Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -User SYSTEM -RunLevel Highest -Force | Out-Null
    # The specialize command has WillReboot=Always. Do not start the task in
    # this boot because setup can terminate it at any point. The startup
    # trigger runs it once after the required reboot.
    exit 0
}
$media = Join-Path $root 'provision'
function Expand-WithTar([string]$Archive, [string]$Destination) {
    New-Item -ItemType Directory -Force -Path $Destination | Out-Null
    & tar.exe -xf $Archive -C $Destination
    if ($LASTEXITCODE -ne 0) { throw "Cannot extract $Archive" }
}
$virtioVolume = Get-Volume | Where-Object { $_.DriveLetter -and $_.DriveType -eq 'CD-ROM' -and $_.FileSystemLabel -like 'virtio-win*' } | Select-Object -First 1
if (-not $virtioVolume) { throw 'VirtIO installation media is absent' }
$qga = Get-ChildItem ($virtioVolume.DriveLetter + ':\') -Recurse -Filter qemu-ga-x86_64.msi -ErrorAction SilentlyContinue | Select-Object -First 1
if (-not $qga) { throw 'QEMU Guest Agent installer is absent' }
$virtioRoot = (Split-Path (Split-Path $qga.FullName -Parent) -Qualifier) + '\'
Get-ChildItem $virtioRoot -Recurse -Filter *.inf | Where-Object FullName -Match '\\2k22\\amd64\\' | ForEach-Object { pnputil.exe /add-driver $_.FullName /install | Out-Default }
for ($attempt = 1; $attempt -le 20; $attempt++) {
    $result = Start-Process msiexec.exe -ArgumentList @('/i', $qga.FullName, '/qn', '/norestart') -Wait -PassThru
    if ($result.ExitCode -in @(0,3010)) { break }
    if ($result.ExitCode -ne 1618 -or $attempt -eq 20) { throw "QGA install failed: $($result.ExitCode)" }
    Start-Sleep 15
}
Set-Service qemu-ga -StartupType Automatic; Start-Service qemu-ga
Expand-Archive (Join-Path $media '@@GO_FILE@@') C:\ -Force
$sshRoot = 'C:\Program Files\OpenSSH-Win64'; Expand-WithTar (Join-Path $media '@@OPENSSH_FILE@@') 'C:\Program Files'
$torInstallRoot = 'C:\Program Files\Tor'; Expand-WithTar (Join-Path $media '@@TOR_FILE@@') $torInstallRoot
$torRoot = Join-Path $torInstallRoot 'tor'
if (-not (Test-Path -LiteralPath (Join-Path $torRoot 'tor.exe') -PathType Leaf)) { throw 'Tor executable is absent after extraction' }
$dotnetRoot = 'C:\Program Files\dotnet'
Expand-Archive (Join-Path $media '@@DOTNET_FILE@@') $dotnetRoot -Force
$wixPackage = Join-Path $root 'wix-package'
$wixUtilPackage = Join-Path $root 'wix-util-package'
$wixDifxPackage = Join-Path $root 'wix-difxapp-package'
$wixDtfPackage = Join-Path $root 'wix-dtf-package'
Expand-WithTar (Join-Path $media '@@WIX_FILE@@') $wixPackage
Expand-WithTar (Join-Path $media '@@WIX_UTIL_FILE@@') $wixUtilPackage
Expand-WithTar (Join-Path $media '@@WIX_DIFXAPP_FILE@@') $wixDifxPackage
Expand-WithTar (Join-Path $media '@@WIX_DTF_FILE@@') $wixDtfPackage
$wixRoot = Join-Path $root 'wix'
$wixExtensionRoot = Join-Path $root 'wixext'
New-Item -ItemType Directory -Force -Path $wixRoot, $wixExtensionRoot | Out-Null
Copy-Item (Join-Path $wixPackage 'tools\net6.0\any\*') $wixRoot -Recurse -Force
Copy-Item (Join-Path $wixUtilPackage 'wixext4\WixToolset.Util.wixext.dll') $wixExtensionRoot
Copy-Item (Join-Path $wixDifxPackage 'wixext4\WixToolset.DifxApp.wixext.dll') $wixExtensionRoot
Copy-Item (Join-Path $wixDifxPackage 'wixext4\difxapp_x64.wixlib') $wixExtensionRoot
Copy-Item (Join-Path $wixDtfPackage 'lib\netstandard2.0\WixToolset.Dtf.WindowsInstaller.dll') $wixRoot
Remove-Item $wixPackage, $wixUtilPackage, $wixDifxPackage, $wixDtfPackage -Recurse -Force
$machinePath = [Environment]::GetEnvironmentVariable('Path','Machine')
[Environment]::SetEnvironmentVariable('DOTNET_ROOT', $dotnetRoot, 'Machine')
$env:DOTNET_ROOT = $dotnetRoot
[Environment]::SetEnvironmentVariable('Path', "C:\go\bin;$sshRoot;$torRoot;$dotnetRoot;$wixRoot;$machinePath", 'Machine'); $env:Path = "C:\go\bin;$sshRoot;$torRoot;$dotnetRoot;$wixRoot;$env:Path"
& "$sshRoot\install-sshd.ps1"; & "$sshRoot\ssh-keygen.exe" -A
@'
HostKey C:/ProgramData/ssh/ssh_host_ed25519_key
PubkeyAuthentication yes
PasswordAuthentication no
KbdInteractiveAuthentication no
AllowUsers winvm
AuthorizedKeysFile C:/ProgramData/ssh/winvm_authorized_keys
Subsystem sftp sftp-server.exe
'@ | Set-Content C:\ProgramData\ssh\sshd_config -Encoding ascii
$password = [Security.SecureString]::new()
foreach ($character in ([guid]::NewGuid().ToString() + 'aA!').ToCharArray()) {
    $password.AppendChar($character)
}
$password.MakeReadOnly()
if (-not (Get-LocalUser winvm -ErrorAction SilentlyContinue)) {
    New-LocalUser winvm -Password $password -AccountNeverExpires -PasswordNeverExpires | Out-Null
}
$users = Get-LocalGroup -SID S-1-5-32-545
if (-not (Get-LocalGroupMember $users | Where-Object Name -Match '\\winvm$')) {
    Add-LocalGroupMember -Group $users -Member winvm
}
$admins = Get-LocalGroup -SID S-1-5-32-544
if (Get-LocalGroupMember $admins | Where-Object Name -Match '\\winvm$') { throw 'winvm is an administrator' }
icacls.exe $runs /inheritance:r /grant:r 'winvm:(OI)(CI)F' 'SYSTEM:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' | Out-Default
if ($LASTEXITCODE -ne 0) { throw 'Cannot protect the guest run directory' }
Copy-Item (Join-Path $media authorized_key.pub) C:\ProgramData\ssh\winvm_authorized_keys
icacls.exe C:\ProgramData\ssh\winvm_authorized_keys /inheritance:r /grant:r 'winvm:R' 'SYSTEM:F' | Out-Default
if ($LASTEXITCODE -ne 0) { throw 'Cannot protect the SSH authorized key' }
Set-Service sshd -StartupType Automatic; Start-Service sshd
if (-not (Get-NetFirewallRule -Name winvm-sshd -ErrorAction SilentlyContinue)) {
    New-NetFirewallRule -Name winvm-sshd -DisplayName 'winvm sshd' -Direction Inbound -Protocol TCP -Action Allow -LocalPort 22 | Out-Null
}

$wintunSource = Join-Path $media 'wintun\wintun-amd64.dll'
$wintunArm64Source = Join-Path $media 'wintun\wintun-arm64.dll'
if (-not (Test-Path -LiteralPath $wintunSource -PathType Leaf)) {
    # xorriso places explicitly supplied directory contents at the ISO root.
    $wintunSource = Join-Path $media 'wintun-amd64.dll'
    $wintunArm64Source = Join-Path $media 'wintun-arm64.dll'
}
$wintunDestination = Join-Path $env:SystemRoot 'System32\wintun.dll'
if (-not (Test-Path -LiteralPath $wintunSource -PathType Leaf)) { throw 'Provisioned Wintun DLL is absent' }
Copy-Item $wintunSource $wintunDestination -Force
$wintunSignature = Get-AuthenticodeSignature $wintunDestination
if ($wintunSignature.Status -ne 'Valid' -or
    $wintunSignature.SignerCertificate.Subject -notlike '*CN=WireGuard LLC*') {
    throw 'Wintun DLL signature is not valid'
}
$dependencyFixtures = Join-Path $root dependencies
New-Item -ItemType Directory -Force -Path $dependencyFixtures | Out-Null
Copy-Item $wintunArm64Source (Join-Path $dependencyFixtures 'wintun-arm64.dll') -Force

$driverSource = Join-Path $media driver
if (-not (Test-Path -LiteralPath $driverSource -PathType Container)) {
    $driverSource = $media
}
$driverStore = Join-Path $root driver
New-Item -ItemType Directory -Force -Path $driverStore | Out-Null
@('mullvad-split-tunnel.sys', 'mullvad-split-tunnel.inf', 'mullvad-split-tunnel.cat') |
    ForEach-Object { Copy-Item (Join-Path $driverSource $_) $driverStore -Force }
$catalogSignature = Get-AuthenticodeSignature (Join-Path $driverStore 'mullvad-split-tunnel.cat')
$driverSignature = Get-AuthenticodeSignature (Join-Path $driverStore 'mullvad-split-tunnel.sys')
if ($catalogSignature.Status -ne 'Valid' -or $driverSignature.Status -ne 'Valid') { throw 'Split-driver package signature is not valid' }
& pnputil.exe /add-driver (Join-Path $driverStore 'mullvad-split-tunnel.inf') | Out-Default
if ($LASTEXITCODE -ne 0) { throw "Cannot stage split-driver package: $LASTEXITCODE" }
$installedDriver = Join-Path $env:SystemRoot 'System32\drivers\mullvad-split-tunnel.sys'
Copy-Item (Join-Path $driverStore 'mullvad-split-tunnel.sys') $installedDriver -Force
$driverServiceName = 'mullvad-split-tunnel'
& sc.exe create $driverServiceName type= kernel start= demand error= normal binPath= '\SystemRoot\System32\drivers\mullvad-split-tunnel.sys' | Out-Default
if ($LASTEXITCODE -notin @(0,1073)) { throw "Cannot create split-driver service: $LASTEXITCODE" }
& sc.exe config $driverServiceName start= demand | Out-Default
if ($LASTEXITCODE -ne 0) { throw "Cannot configure split-driver service: $LASTEXITCODE" }
$bcd = (& bcdedit.exe /enum '{current}' | Out-String)
if ($bcd -match '(?im)^testsigning\s+Yes' -or $bcd -match '(?im)^nointegritychecks\s+Yes') {
    throw 'Windows code-integrity checks are disabled'
}

powercfg.exe /change standby-timeout-ac 0; powercfg.exe /change hibernate-timeout-ac 0
Stop-Service wuauserv -Force -ErrorAction SilentlyContinue; Set-Service wuauserv -StartupType Manual
$goVersion = (& go version | Out-String).Trim()
$torVersion = (& tor.exe --version | Select-Object -First 1 | Out-String).Trim()
$dotnetRuntimes = @(& dotnet.exe --list-runtimes)
if ($LASTEXITCODE -ne 0 -or $dotnetRuntimes -notmatch '^Microsoft\.NETCore\.App 6\.0\.36 ') { throw 'Unexpected .NET runtime' }
$wixVersion = (& wix.exe --version | Out-String).Trim()
if ($LASTEXITCODE -ne 0 -or $wixVersion -notmatch '^4\.0\.6(?:\+.*)?$') { throw "Unexpected WiX version: $wixVersion" }
$qgaVersion = (Get-Item 'C:\Program Files\qemu-ga\qemu-ga.exe').VersionInfo.FileVersion
$driverService = Get-CimInstance Win32_SystemDriver -Filter "Name='$driverServiceName'"
if (-not $driverService -or $driverService.StartMode -ne 'Manual' -or $driverService.State -ne 'Stopped') {
    throw 'Split-driver service final state is not stopped and demand-start'
}
$manifest = [ordered]@{
    windowsBuild=[Environment]::OSVersion.Version.ToString(); architecture=$env:PROCESSOR_ARCHITECTURE
    goVersion=$goVersion; torVersion=$torVersion; dotnetRuntimes=$dotnetRuntimes
    wixVersion=$wixVersion; wixExtensions=@('WixToolset.Util.wixext 4.0.6', 'WixToolset.DifxApp.wixext 4.0.6')
    qemuGuestAgentVersion=$qgaVersion; provisionedAt=(Get-Date).ToUniversalTime().ToString('o')
    driverService=$driverServiceName
}
$manifestTemp = Join-Path $root 'manifest.json.tmp'; $manifest | ConvertTo-Json -Depth 4 | Set-Content $manifestTemp -Encoding utf8
@('C:\Windows\Panther\unattend.xml','C:\Windows\Panther\Unattend\unattend.xml') | ForEach-Object { Remove-Item $_ -Force -ErrorAction SilentlyContinue }
Unregister-ScheduledTask $taskName -Confirm:$false; Remove-Item $media -Recurse -Force
Move-Item $manifestTemp (Join-Path $root manifest.json) -Force
# A boot identity makes readiness checks detect a reboot as well as reprovisioning.
$readyTemp = Join-Path $root 'ready.tmp'; [IO.File]::WriteAllText($readyTemp, [guid]::NewGuid().ToString()); Move-Item $readyTemp (Join-Path $root ready) -Force
