param([Parameter(Mandatory = $true)][string]$ArtifactDir)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$programFiles = Join-Path $env:ProgramFiles 'Almagest'
$programData = Join-Path $env:ProgramData 'Almagest'
$probe = Join-Path $programFiles 'ordinary-user-write-probe'
$writeDenied = $false
try {
    [IO.File]::WriteAllText($probe, 'must not be written')
} catch {
    $writeDenied = $true
} finally {
    Remove-Item $probe -Force -ErrorAction SilentlyContinue
}
if (-not $writeDenied) { throw 'Ordinary user can write to Program Files' }

$cli = Join-Path $programFiles 'almagest-cli.exe'
$cliStdout = Join-Path $ArtifactDir 'package-user-cli.stdout.txt'
$cliStderr = Join-Path $ArtifactDir 'package-user-cli.stderr.txt'
$cliProcess = Start-Process $cli -ArgumentList @(
    '--url', 'pipe://machine', 'admin', 'ping', '--nonce', 'must-not-pass'
) -RedirectStandardOutput $cliStdout -RedirectStandardError $cliStderr -Wait -PassThru
$cliExit = $cliProcess.ExitCode
$cliOutput = ((Get-Content -Raw $cliStdout -ErrorAction SilentlyContinue) +
    (Get-Content -Raw $cliStderr -ErrorAction SilentlyContinue))
Remove-Item $cliStdout, $cliStderr -Force -ErrorAction SilentlyContinue
if ($cliExit -eq 0) { throw 'Ordinary user can use pipe://machine' }

$privateReadDenied = $false
try {
    Get-Content -Raw (Join-Path $programData 'protected_alm.hujson') | Out-Null
} catch {
    $privateReadDenied = $true
}
if (-not $privateReadDenied) { throw 'Ordinary user can read protected ProgramData' }

[ordered]@{
    schemaVersion = 1
    suite = 'windows-msi-package-smoke-user'
    outcome = 'passed'
    identity = (& whoami.exe | Out-String).Trim()
    programFilesWriteDenied = $writeDenied
    machinePipeAccessDenied = $true
    cliExitCode = $cliExit
    cliOutput = $cliOutput.Trim()
    protectedConfigReadDenied = $privateReadDenied
} | ConvertTo-Json -Depth 4 | Set-Content (Join-Path $ArtifactDir 'package-user-smoke.json') -Encoding utf8
