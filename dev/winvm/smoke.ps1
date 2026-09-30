param([Parameter(Mandatory=$true)][string]$ArtifactDir)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
New-Item -ItemType Directory -Force $ArtifactDir | Out-Null
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
if ($principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'The smoke command must use the standard SSH account'
}
$version = (& go version | Out-String).Trim()
if ($LASTEXITCODE -ne 0 -or $version -ne 'go version go1.26.6 windows/amd64') {
    throw "Unexpected guest toolchain: $version"
}
if ([Environment]::OSVersion.Version.Build -ne 20348 -or $env:PROCESSOR_ARCHITECTURE -ne 'AMD64') {
    throw 'This appliance requires Windows Server 2022/amd64'
}
@{
    schemaVersion=1; suite='harness-smoke'; outcome='passed'
    identity=$identity.Name; sid=$identity.User.Value; goVersion=$version
    windowsVersion=[Environment]::OSVersion.Version.ToString(); architecture='amd64'
    testExecution='not-run'
} | ConvertTo-Json | Set-Content (Join-Path $ArtifactDir 'smoke.json') -Encoding utf8
