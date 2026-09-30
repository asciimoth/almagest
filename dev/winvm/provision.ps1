param([switch]$Install)
$ErrorActionPreference = 'Stop'; Set-StrictMode -Version Latest
$root = 'C:\winvm'; $runs = Join-Path $root 'runs'; $taskName = 'almagest-winvm-provision'
New-Item -ItemType Directory -Force -Path $runs | Out-Null
if (-not $Install) {
    $source = (Get-Volume -FileSystemLabel WINVM_PROVISION).DriveLetter + ':'
    $staging = Join-Path $root 'provision'; New-Item -ItemType Directory -Force -Path $staging | Out-Null
    Copy-Item (Join-Path $source '*') $staging -Recurse -Force
    $command = "& '$staging\provision.ps1' -Install *> '$root\provision.log'"
    $action = New-ScheduledTaskAction powershell.exe "-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -Command `"$command`""
    $trigger = New-ScheduledTaskTrigger -AtStartup; $trigger.Delay = 'PT1M'
    Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -User SYSTEM -RunLevel Highest -Force | Out-Null
    Start-ScheduledTask -TaskName $taskName
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
$machinePath = [Environment]::GetEnvironmentVariable('Path','Machine')
[Environment]::SetEnvironmentVariable('Path', "C:\go\bin;$sshRoot;$machinePath", 'Machine'); $env:Path = "C:\go\bin;$sshRoot;$env:Path"
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
$password = ConvertTo-SecureString ([guid]::NewGuid().ToString() + 'aA!') -AsPlainText -Force
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

powercfg.exe /change standby-timeout-ac 0; powercfg.exe /change hibernate-timeout-ac 0
Stop-Service wuauserv -Force -ErrorAction SilentlyContinue; Set-Service wuauserv -StartupType Manual
$goVersion = (& go version | Out-String).Trim()
if ($goVersion -notmatch '^go version go@@GO_VERSION@@ windows/amd64$') { throw "Unexpected Go version: $goVersion" }
$qgaVersion = (Get-Item 'C:\Program Files\qemu-ga\qemu-ga.exe').VersionInfo.FileVersion
$manifest = [ordered]@{
    windowsBuild=[Environment]::OSVersion.Version.ToString(); architecture=$env:PROCESSOR_ARCHITECTURE
    goVersion=$goVersion; qemuGuestAgentVersion=$qgaVersion; provisionedAt=(Get-Date).ToUniversalTime().ToString('o')
}
$manifestTemp = Join-Path $root 'manifest.json.tmp'; $manifest | ConvertTo-Json -Depth 4 | Set-Content $manifestTemp -Encoding utf8
@('C:\Windows\Panther\unattend.xml','C:\Windows\Panther\Unattend\unattend.xml') | ForEach-Object { Remove-Item $_ -Force -ErrorAction SilentlyContinue }
Unregister-ScheduledTask $taskName -Confirm:$false; Remove-Item $media -Recurse -Force
Move-Item $manifestTemp (Join-Path $root manifest.json) -Force
# A boot identity makes readiness checks detect a reboot as well as reprovisioning.
$readyTemp = Join-Path $root 'ready.tmp'; [IO.File]::WriteAllText($readyTemp, [guid]::NewGuid().ToString()); Move-Item $readyTemp (Join-Path $root ready) -Force
