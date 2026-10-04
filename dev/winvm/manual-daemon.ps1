param(
    [Parameter(Mandatory=$true)][ValidateSet('Start','Export')][string]$Mode,
    [Parameter(Mandatory=$true)][string]$SourceDir,
    [Parameter(Mandatory=$true)][string]$RunDir,
    [Parameter(Mandatory=$true)][int]$WebPort
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$manualDir = Join-Path $RunDir 'manual'
$configDir = Join-Path $manualDir 'config'
$binary = Join-Path $SourceDir 'dev\winvm\almagestd.exe'
$cliSource = Join-Path $SourceDir 'dev\winvm\almagest-cli.exe'
$toolDir = 'C:\winvm\bin'
$cli = Join-Path $toolDir 'almagest-cli.exe'
$stdout = Join-Path $manualDir 'almagestd.stdout.log'
$stderr = Join-Path $manualDir 'almagestd.stderr.log'
$pidPath = Join-Path $manualDir 'almagestd.pid'
$storage = Join-Path $env:ProgramData ('Almagest\manual-' + (Split-Path $RunDir -Leaf))
$importDir = Join-Path $manualDir 'import'
$exportDir = Join-Path $manualDir 'export'
New-Item -ItemType Directory -Force $configDir | Out-Null
New-Item -ItemType Directory -Force $storage | Out-Null
& icacls.exe $storage /inheritance:r /grant:r 'SYSTEM:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' | Out-Null
if ($LASTEXITCODE -ne 0) { throw "cannot protect manual file storage: $storage" }

if ($Mode -eq 'Export') {
    if (Test-Path $pidPath) {
        $launcherPid = [int](Get-Content -Raw $pidPath)
        & taskkill.exe /PID $launcherPid /T /F 2>&1 | Out-Null
        if ($LASTEXITCODE -ne 0 -and (Get-Process -Id $launcherPid -ErrorAction SilentlyContinue)) {
            throw "cannot stop almagestd launcher process $launcherPid"
        }
        Start-Sleep -Milliseconds 500
    }
    Remove-Item $exportDir -Recurse -Force -ErrorAction SilentlyContinue
    New-Item -ItemType Directory -Force (Join-Path $exportDir 'config'), (Join-Path $exportDir 'store'), (Join-Path $exportDir 'logs') | Out-Null
    Get-ChildItem $configDir -Force | Copy-Item -Destination (Join-Path $exportDir 'config') -Recurse -Force
    Get-ChildItem $storage -Force | Copy-Item -Destination (Join-Path $exportDir 'store') -Recurse -Force
    @($stdout, $stderr) | Where-Object { Test-Path $_ } |
        ForEach-Object { Copy-Item $_ (Join-Path $exportDir 'logs') -Force }
    & tar.exe -cf (Join-Path $manualDir 'state-export.tar') -C $exportDir .
    if ($LASTEXITCODE -ne 0) { throw 'cannot archive manual daemon state' }
    exit 0
}

$stateArchive = Join-Path $manualDir 'state-import.tar'
if (Test-Path -LiteralPath $stateArchive -PathType Leaf) {
    Remove-Item $importDir -Recurse -Force -ErrorAction SilentlyContinue
    New-Item -ItemType Directory -Force $importDir | Out-Null
    & tar.exe -xf $stateArchive -C $importDir
    if ($LASTEXITCODE -ne 0) { throw 'cannot extract manual daemon state' }
    if (Test-Path (Join-Path $importDir 'config')) {
        Get-ChildItem (Join-Path $importDir 'config') -Force | Copy-Item -Destination $configDir -Recurse -Force
    }
    if (Test-Path (Join-Path $importDir 'store')) {
        Get-ChildItem (Join-Path $importDir 'store') -Force | Copy-Item -Destination $storage -Recurse -Force
    }
    if (Test-Path (Join-Path $importDir 'logs')) {
        Get-ChildItem (Join-Path $importDir 'logs') -Force | Copy-Item -Destination $manualDir -Recurse -Force
    }
}
if (-not (Test-Path (Join-Path $configDir 'alm.hujson'))) {
    '{}' | Set-Content (Join-Path $configDir 'alm.hujson') -Encoding ascii
}
if (-not (Test-Path (Join-Path $configDir 'protected_alm.hujson'))) {
    '{}' | Set-Content (Join-Path $configDir 'protected_alm.hujson') -Encoding ascii
}
if (-not (Test-Path -LiteralPath $binary -PathType Leaf)) {
    throw "staged almagestd binary is absent: $binary"
}
if (-not (Test-Path -LiteralPath $cliSource -PathType Leaf)) {
    throw "staged almagest-cli binary is absent: $cliSource"
}
New-Item -ItemType Directory -Force $toolDir | Out-Null
& icacls.exe $toolDir /inheritance:r /grant:r 'winvm:(OI)(CI)RX' 'SYSTEM:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' | Out-Null
if ($LASTEXITCODE -ne 0) { throw "cannot protect manual tool directory: $toolDir" }
Copy-Item $cliSource $cli -Force
& icacls.exe $cli /inheritance:r /grant:r 'winvm:RX' 'SYSTEM:F' '*S-1-5-32-544:F' | Out-Null
if ($LASTEXITCODE -ne 0) { throw "cannot protect manual CLI: $cli" }

$arguments = @(
    '-c', $configDir,
    '-l', 'debug',
    '-w', [string]$WebPort,
    '--debug-nodes',
    '--experimental',
    '--unsafe-config',
    '--file-storage', $storage,
    '--no-dns-cache-expiration'
)
$commandLine = "cmd.exe /d /c $binary $($arguments -join ' ') 1>>$stdout 2>>$stderr"
$result = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{
    CommandLine = $commandLine
    CurrentDirectory = $manualDir
}
if ($result.ReturnValue -ne 0) {
    throw "cannot start almagestd: Win32_Process.Create returned $($result.ReturnValue)"
}
[IO.File]::WriteAllText($pidPath, [string]$result.ProcessId)
Start-Sleep -Seconds 1
if (-not (Get-Process -Id $result.ProcessId -ErrorAction SilentlyContinue)) {
    $details = @($stdout, $stderr) | Where-Object { Test-Path $_ } |
        ForEach-Object { Get-Content -Raw $_ }
    if (-not $details) { $details = 'no daemon log' }
    throw "almagestd exited during startup: $details"
}
Write-Output "almagestd launcher pid=$($result.ProcessId) web=http://127.0.0.1:$WebPort/"
