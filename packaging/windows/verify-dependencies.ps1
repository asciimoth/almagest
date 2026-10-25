[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$WintunDirectory,

    [Parameter(Mandatory = $true)]
    [string]$MullvadDirectory,

    [string]$ContractPath = (Join-Path $PSScriptRoot 'contract.json')
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Assert-Equal {
    param(
        [Parameter(Mandatory = $true)] [string]$Actual,
        [Parameter(Mandatory = $true)] [string]$Expected,
        [Parameter(Mandatory = $true)] [string]$Label
    )
    if ($Actual -cne $Expected) {
        throw "$Label is '$Actual'; expected '$Expected'"
    }
}

function Get-PEMachine {
    param([Parameter(Mandatory = $true)] [string]$Path)

    $stream = [System.IO.File]::OpenRead($Path)
    try {
        $reader = [System.IO.BinaryReader]::new($stream)
        if ($reader.ReadUInt16() -ne 0x5a4d) {
            throw "File is not a PE image: $Path"
        }
        $stream.Position = 0x3c
        $peOffset = $reader.ReadUInt32()
        $stream.Position = $peOffset
        if ($reader.ReadUInt32() -ne 0x00004550) {
            throw "File has no PE signature: $Path"
        }
        return ('0x{0:x4}' -f $reader.ReadUInt16())
    } finally {
        $stream.Dispose()
    }
}

function Assert-FileHash {
    param(
        [Parameter(Mandatory = $true)] [string]$Path,
        [Parameter(Mandatory = $true)] [string]$Expected
    )
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "Required dependency is absent: $Path"
    }
    $actual = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
    Assert-Equal $actual $Expected.ToLowerInvariant() "SHA-256 for $Path"
}

function Assert-Signature {
    param(
        [Parameter(Mandatory = $true)] [string]$Path,
        [string]$SignerSubjectContains
    )

    $signature = Get-AuthenticodeSignature -LiteralPath $Path
    if ($signature.Status -ne [System.Management.Automation.SignatureStatus]::Valid) {
        throw "Authenticode signature for $Path is $($signature.Status): $($signature.StatusMessage)"
    }
    if ($SignerSubjectContains -and
        $signature.SignerCertificate.Subject -notlike "*$SignerSubjectContains*") {
        throw "Authenticode signer for $Path is '$($signature.SignerCertificate.Subject)'; expected '*$SignerSubjectContains*'"
    }
}

$contract = Get-Content -LiteralPath $ContractPath -Raw | ConvertFrom-Json
Assert-Equal $contract.target.architecture 'amd64' 'Contract target architecture'
Assert-Equal $contract.target.installerArchitecture 'x64' 'Installer architecture'

$wintun = Join-Path $WintunDirectory $contract.drivers.wintun.file
Assert-FileHash $wintun $contract.drivers.wintun.sha256
Assert-Equal (Get-PEMachine $wintun) $contract.drivers.wintun.peMachine 'Wintun PE machine'
Assert-Signature $wintun $contract.drivers.wintun.authenticodeSignerSubjectContains

$driver = $contract.drivers.mullvadSplitTunnel
foreach ($property in $driver.files.PSObject.Properties) {
    Assert-FileHash (Join-Path $MullvadDirectory $property.Name) $property.Value.sha256
}

$sys = Join-Path $MullvadDirectory 'mullvad-split-tunnel.sys'
Assert-Equal (Get-PEMachine $sys) $driver.peMachine 'Mullvad driver PE machine'
Assert-Signature $sys
Assert-Signature (Join-Path $MullvadDirectory 'mullvad-split-tunnel.cat')

$inf = Get-Content -LiteralPath (Join-Path $MullvadDirectory 'mullvad-split-tunnel.inf') -Raw
if ($inf -notmatch ('(?im)^DriverVer\s*=\s*[^,]+,' + [regex]::Escape($driver.version) + '\s*$')) {
    throw "Mullvad INF does not declare driver version $($driver.version)"
}
if ($inf -notmatch ('(?im)^%ManufacturerName%=Standard,' + [regex]::Escape($driver.declaredInfArchitecture) + '\s*$')) {
    throw "Mullvad INF does not declare architecture $($driver.declaredInfArchitecture)"
}
if ($inf -notmatch '(?im)^StartType\s*=\s*3(?:\s*;.*)?$') {
    throw 'Mullvad INF is not demand-start'
}

Write-Output 'Windows amd64 package dependencies match the frozen contract.'
