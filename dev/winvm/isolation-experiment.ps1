param(
    [Parameter(Mandatory=$true)][ValidateSet('caller','service')][string]$Profile,
    [Parameter(Mandatory=$true)][string]$SourceDir,
    [Parameter(Mandatory=$true)][string]$ArtifactDir
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
$elevated = $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (($Profile -eq 'caller' -and $elevated) -or ($Profile -eq 'service' -and -not $elevated)) {
    throw "The $Profile experiment has an incorrect launcher token"
}
if ([Environment]::OSVersion.Version.Build -ne 20348 -or $env:PROCESSOR_ARCHITECTURE -ne 'AMD64') {
    throw 'The experiment requires Windows Server 2022/amd64'
}

$profileDir = Join-Path $ArtifactDir "windows-isolation-$Profile"
$storageDir = Join-Path $profileDir 'storage'
New-Item -ItemType Directory -Force $profileDir,$storageDir | Out-Null
$probe = Join-Path $profileDir 'windows-isolation-probe.exe'
& go -C (Join-Path $SourceDir 'bin') build -trimpath -o $probe ./cmd/windows-isolation-probe
if ($LASTEXITCODE -ne 0) { throw 'Cannot build the Windows isolation probe' }

# This file represents a bootstrap-only host input. Its protected ACL makes a
# successful read by an AppContainer a hard experiment failure. Package init
# probes it before the restricted child enters main.
$secret = 'C:\winvm\isolation-host-secret.txt'
'bootstrap-only' | Set-Content -LiteralPath $secret -Encoding ascii
& icacls.exe $secret /inheritance:r /grant:r '*S-1-5-18:(F)' | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'Cannot protect the host-only probe file' }

$result = Join-Path $profileDir 'result.json'
try {
    & $probe -profile $Profile -storage $storageDir -output $result
    if ($LASTEXITCODE -ne 0) { throw "The $Profile restricted probe failed" }
    $record = Get-Content -Raw $result | ConvertFrom-Json
    if ($record.schemaVersion -ne 1 -or $record.profile -ne $Profile) {
        throw 'The isolation result identity is invalid'
    }
    $failed = @($record.checks.PSObject.Properties | Where-Object { -not $_.Value.ok })
    if ($failed.Count) { throw "Failed restricted checks: $($failed.Name -join ', ')" }
    @{
        schemaVersion=1
        profile=$Profile
        launcherName=$identity.Name
        launcherSID=$identity.User.Value
        launcherElevated=$elevated
        os='windows-server-2022'
        build=[Environment]::OSVersion.Version.Build
        architecture='amd64'
        go=(go version)
    } | ConvertTo-Json | Set-Content (Join-Path $profileDir 'environment.json') -Encoding utf8
} finally {
    if ($Profile -eq 'service') { Remove-Item -LiteralPath $secret -Force -ErrorAction Continue }
}
