param(
    [Parameter(Mandatory = $true)]
    [string]$WixExecutable,
    [string]$Output = ''
)

$ErrorActionPreference = 'Stop'
if (-not $IsWindows) {
    throw 'The WiX fixture build must run on Windows'
}
$packageDirectory = Split-Path -Parent $MyInvocation.MyCommand.Path
$sourceDirectory = Split-Path -Parent (Split-Path -Parent $packageDirectory)
if (-not $Output) {
    $Output = Join-Path $packageDirectory 'almagest-fixture.msi'
}

$fixture = Join-Path ([System.IO.Path]::GetTempPath()) "almagest-wix-fixture-$PID"
$payload = Join-Path $fixture 'payload'
$wintun = Join-Path $fixture 'wintun\amd64'
$mullvad = Join-Path $fixture 'mullvad\amd64'
$wintunLicenses = Join-Path $fixture 'wintun\licenses'
$mullvadLicenses = Join-Path $fixture 'mullvad\licenses'

try {
    @($payload, $wintun, $mullvad, $wintunLicenses, $mullvadLicenses) |
        ForEach-Object { New-Item -ItemType Directory -Path $_ -Force | Out-Null }

    @('almagestd.exe', 'almagest-cli.exe') | ForEach-Object {
        [System.IO.File]::WriteAllBytes((Join-Path $payload $_), [byte[]](0x4d, 0x5a))
    }
    [System.IO.File]::WriteAllBytes((Join-Path $wintun 'wintun.dll'), [byte[]](0x4d, 0x5a))
    @('mullvad-split-tunnel.inf', 'mullvad-split-tunnel.sys', 'mullvad-split-tunnel.cat') |
        ForEach-Object { Set-Content -LiteralPath (Join-Path $mullvad $_) -Value 'fixture' }
    Set-Content -LiteralPath (Join-Path $wintunLicenses 'wintun-LICENSE.txt') -Value 'fixture'
    Set-Content -LiteralPath (Join-Path $mullvadLicenses 'mullvad-LICENSE-MPL.txt') -Value 'fixture'

    $arguments = @(
        'build',
        (Join-Path $packageDirectory 'almagest.wxs'),
        (Join-Path $packageDirectory 'payload.wxs'),
        '-arch', 'x64',
        '-ext', 'WixToolset.Util.wixext',
        '-ext', 'WixToolset.DifxApp.wixext',
        '-d', 'Version=0.0.1',
        '-d', 'ProductCode=20F0DF2E-E10B-5C7F-966E-02E556488306',
        '-d', "SourceDir=$sourceDirectory",
        '-d', "PayloadDir=$payload",
        '-d', "WintunDir=$wintun",
        '-d', "MullvadDir=$mullvad",
        '-o', $Output
    )
    & $WixExecutable @arguments
    if ($LASTEXITCODE -ne 0) {
        throw "WiX fixture build failed with exit code $LASTEXITCODE"
    }
    if (-not (Test-Path -LiteralPath $Output -PathType Leaf)) {
        throw "WiX fixture output is absent: $Output"
    }
} finally {
    if (Test-Path -LiteralPath $fixture) {
        Remove-Item -LiteralPath $fixture -Recurse -Force
    }
}
