param(
    [Parameter(Mandatory = $true)][string]$ArtifactDir,
    [Parameter(Mandatory = $true)][string]$SourceDir,
    [string]$ManifestName = 'package-manifest.json',
    [string]$EvidenceName = 'package-build.json'
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
New-Item -ItemType Directory -Force $ArtifactDir | Out-Null

$manifestPath = Join-Path (Join-Path $SourceDir 'dev\winvm') $ManifestName
$manifest = Get-Content -Raw $manifestPath | ConvertFrom-Json
$cargo = 'C:\Rust\bin\cargo.exe'
$rustTools = 'C:\Rust\lib\rustlib\x86_64-pc-windows-gnu\bin\self-contained'
$buildTools = Join-Path $SourceDir 'dev\winvm\rust-build-tools'
New-Item -ItemType Directory -Force $buildTools | Out-Null
Copy-Item (Join-Path $rustTools 'dlltool.exe') $buildTools -Force
Copy-Item (Join-Path $SourceDir 'dev\winvm\llvm-as-wrapper.exe') `
    (Join-Path $buildTools 'as.exe') -Force
Copy-Item (Join-Path $rustTools 'libwinpthread-1.dll') $buildTools -Force
Copy-Item 'C:\llvm-mingw\bin\clang-23.exe' $buildTools -Force
Copy-Item 'C:\llvm-mingw\bin\*.dll' $buildTools -Force
$env:Path = "$buildTools;C:\Rust\bin;$rustTools;C:\llvm-mingw\bin;$env:Path"
$env:CARGO_TARGET_X86_64_PC_WINDOWS_GNU_LINKER = `
    'C:\llvm-mingw\bin\x86_64-w64-mingw32-clang.exe'
$env:LIBRARY_PATH = 'C:\Rust\lib\rustlib\x86_64-pc-windows-gnu\lib\self-contained'
$cargoHome = Join-Path $SourceDir 'dev\winvm\cargo-home'
$env:CARGO_HOME = $cargoHome
$env:RUSTFLAGS = "--remap-path-prefix=$SourceDir=."
Push-Location $SourceDir
try {
    & $cargo build --release --locked --offline --manifest-path app\src-tauri\Cargo.toml
    if ($LASTEXITCODE -ne 0) { throw "Desktop app build failed: $LASTEXITCODE" }
} finally {
    Pop-Location
}
$appSource = Join-Path $SourceDir 'app\src-tauri\target\release\almagest-app.exe'
$appPayload = Join-Path $SourceDir 'dev\winvm\almagest-app.exe'
Copy-Item -LiteralPath $appSource -Destination $appPayload -Force
& python.exe (Join-Path $SourceDir 'packaging\windows\create-build-manifest.py') `
    --version $manifest.version --root $SourceDir `
    --source-record (Join-Path $SourceDir 'dev\winvm\source.json') `
    --image-lock (Join-Path $SourceDir 'dev\winvm\image-lock.json') `
    --output $manifestPath
if ($LASTEXITCODE -ne 0) { throw "Package manifest refresh failed: $LASTEXITCODE" }
$manifest = Get-Content -Raw $manifestPath | ConvertFrom-Json
$inputs = Join-Path $SourceDir 'dev\winvm\package-inputs'
$wintun = Join-Path $inputs 'wintun\amd64'
$mullvad = Join-Path $inputs 'split-driver\amd64'
& (Join-Path $SourceDir 'packaging\windows\verify-dependencies.ps1') `
    -WintunDirectory $wintun -MullvadDirectory $mullvad

# The CAB format stores each payload file's local DOS modification time. Make
# that metadata stable before WiX reads the files. The manifest is the complete
# list of files that the MSI can contain.
$fixedTime = [DateTime]::new(2000, 1, 1, 0, 0, 0, [DateTimeKind]::Local)
foreach ($inputFile in $manifest.inputs.files) {
    $inputPath = Join-Path $SourceDir ($inputFile.path -replace '/', '\')
    [IO.File]::SetLastWriteTime($inputPath, $fixedTime)
}

$output = Join-Path $ArtifactDir $manifest.output
$extensions = 'C:\winvm\wixext'
$arguments = @(
    'build',
    (Join-Path $SourceDir 'packaging\windows\almagest.wxs'),
    (Join-Path $SourceDir 'packaging\windows\payload.wxs'),
    (Join-Path $extensions 'difxapp_x64.wixlib'),
    '-arch', 'x64',
    '-ext', (Join-Path $extensions 'WixToolset.Util.wixext.dll'),
    '-ext', (Join-Path $extensions 'WixToolset.DifxApp.wixext.dll'),
    '-ext', (Join-Path $extensions 'WixToolset.UI.wixext.dll'),
    '-d', "Version=$($manifest.version)",
    '-d', "ProductCode=$($manifest.productCode)",
    '-d', "SourceDir=$SourceDir",
    '-d', "PayloadDir=$(Join-Path $SourceDir 'dev\winvm')",
    '-d', "WintunDir=$wintun",
    '-d', "MullvadDir=$mullvad",
    '-o', $output
)
& 'C:\winvm\wix\wix.exe' @arguments
if ($LASTEXITCODE -ne 0) { throw "WiX build failed: $LASTEXITCODE" }

# WiX v4 generates package identity and save timestamps at link time. Replace
# only those summary fields so that the complete locked input manifest defines
# one byte-reproducible package build.
Add-Type -Path 'C:\winvm\wix\WixToolset.Dtf.WindowsInstaller.dll'
$summary = [WixToolset.Dtf.WindowsInstaller.SummaryInfo]::new($output, $true)
try {
    $summary.RevisionNumber = "{$($manifest.packageCode)}"
    $fixedUtcTime = [DateTime]::new(2000, 1, 1, 0, 0, 0, [DateTimeKind]::Utc)
    $summary.CreateTime = $fixedUtcTime
    $summary.LastSaveTime = $fixedUtcTime
    $summary.Persist()
} finally {
    $summary.Dispose()
}

# Persist() also writes the current time to the root directory entry of the
# MSI compound file. The first directory entry is always the root storage.
# Replace its creation and modification FILETIMEs without changing any stream.
$compound = [IO.File]::ReadAllBytes($output)
$compoundSignature = [byte[]](0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1)
if ([BitConverter]::ToString($compound, 0, 8) -ne
        [BitConverter]::ToString($compoundSignature)) {
    throw 'WiX output is not a compound file'
}
$sectorShift = [BitConverter]::ToUInt16($compound, 30)
if ($sectorShift -notin @(9, 12)) {
    throw "Unsupported compound-file sector shift: $sectorShift"
}
$directorySector = [BitConverter]::ToUInt32($compound, 48)
$directoryOffset = ([int64]$directorySector + 1) * (1 -shl $sectorShift)
if ($directoryOffset + 128 -gt $compound.LongLength) {
    throw 'Compound-file root directory entry is outside the MSI'
}
$rootType = $compound[$directoryOffset + 66]
if ($rootType -ne 5) {
    throw "Compound-file first directory entry is not root storage: $rootType"
}
$fixedFileTime = [BitConverter]::GetBytes($fixedUtcTime.ToFileTimeUtc())
[Array]::Copy($fixedFileTime, 0, $compound, $directoryOffset + 100, 8)
[Array]::Copy($fixedFileTime, 0, $compound, $directoryOffset + 108, 8)
[IO.File]::WriteAllBytes($output, $compound)

$summary = [WixToolset.Dtf.WindowsInstaller.SummaryInfo]::new($output, $false)
try {
    $actualPackageCode = $summary.RevisionNumber.Trim('{}').ToUpperInvariant()
} finally {
    $summary.Dispose()
}
if ($actualPackageCode -ne $manifest.packageCode) {
    throw "PackageCode normalization failed: $actualPackageCode"
}

$msiHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $output).Hash.ToLowerInvariant()
"$msiHash  $($manifest.output)" | Set-Content `
    (Join-Path $ArtifactDir "$($manifest.output).sha256") -Encoding ascii
$image = Get-Content -Raw 'C:\winvm\manifest.json' | ConvertFrom-Json
$evidence = [ordered]@{
    schemaVersion = 1
    outcome = 'passed'
    suite = 'windows-msi-build'
    package = $manifest.output
    sha256 = $msiHash
    version = $manifest.version
    productCode = $manifest.productCode
    packageCode = $manifest.packageCode
    actualPackageCode = $actualPackageCode
    normalizedMetadata = @(
        'SummaryInformation.RevisionNumber',
        'SummaryInformation.CreateTime',
        'SummaryInformation.LastSaveTime',
        'Cabinet.PayloadLastWriteTime',
        'CompoundFile.RootEntryTimes'
    )
    source = $manifest.source
    inputs = $manifest.inputs
    reproducibility = $manifest.reproducibility
    tools = [ordered]@{
        dotnetRuntimes = @(& dotnet.exe --list-runtimes)
        wix = (& 'C:\winvm\wix\wix.exe' --version | Out-String).Trim()
        extensions = $image.wixExtensions
        dtf = (Get-Item 'C:\winvm\wix\WixToolset.Dtf.WindowsInstaller.dll').VersionInfo.ProductVersion
    }
}
$evidence | ConvertTo-Json -Depth 8 | Set-Content `
    (Join-Path $ArtifactDir $EvidenceName) -Encoding utf8
