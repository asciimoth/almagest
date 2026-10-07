param(
    [Parameter(Mandatory=$true)][ValidateSet('ordinary','privileged')][string]$Mode,
    [Parameter(Mandatory=$true)][string]$SourceDir,
    [Parameter(Mandatory=$true)][string]$ArtifactDir,
    [switch]$ContinueAfterFailure
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$manifestPath = Join-Path $SourceDir 'dev\winvm\test-suites.json'
$manifest = Get-Content -Raw $manifestPath | ConvertFrom-Json
$suite = $manifest.suites | Where-Object privilege -eq $Mode
if (@($suite).Count -ne 1) { throw "The $Mode suite is absent or duplicated" }
$caseTimeoutSeconds = [int]$manifest.caseTimeoutSeconds
$goPackageTimeoutSeconds = [int]$manifest.goPackageTimeoutSeconds
$progressIntervalSeconds = [int]$manifest.progressIntervalSeconds
if ($caseTimeoutSeconds -le 0 -or $goPackageTimeoutSeconds -le 0 -or
    $goPackageTimeoutSeconds -ge $caseTimeoutSeconds -or
    $progressIntervalSeconds -le 0 -or $progressIntervalSeconds -ge $caseTimeoutSeconds) {
    throw 'The test timeout and progress interval settings are invalid'
}

$windowsIdentity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($windowsIdentity)
$elevated = $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
$wantElevated = $Mode -eq 'privileged'
if ($elevated -ne $wantElevated) { throw "The $Mode suite has an incorrect execution token" }
if ([Environment]::OSVersion.Version.Build -ne 20348 -or $env:PROCESSOR_ARCHITECTURE -ne 'AMD64') {
    throw 'The native suite requires Windows Server 2022/amd64'
}
if ($Mode -eq 'privileged') {
    $wintunDLL = Join-Path $env:SystemRoot 'System32\wintun.dll'
    if (-not (Test-Path -LiteralPath $wintunDLL -PathType Leaf)) {
        throw 'The provisioned Wintun DLL is absent'
    }
    $env:SYSNET_WINDOWS_WINTUN_DLL = $wintunDLL
}

$suiteDir = Join-Path $ArtifactDir $suite.id
$eventDir = Join-Path $suiteDir 'events'
$coverageDir = Join-Path $suiteDir 'coverage'
New-Item -ItemType Directory -Force $eventDir,$coverageDir | Out-Null
$progressDir = Join-Path $suiteDir 'progress'
$diagnosticDir = Join-Path $suiteDir 'diagnostics'
New-Item -ItemType Directory -Force $progressDir,$diagnosticDir | Out-Null
$progressIndex = 0
function Write-TestProgress([string]$Status, [string]$CaseID) {
    $line = "[$Mode] $Status $CaseID"
    $path = Join-Path $progressDir ("{0:D3}.txt" -f $script:progressIndex)
    $line | Set-Content -LiteralPath $path -Encoding ascii
    $script:progressIndex++
    Write-Output $line
}
function Write-CaseState([string]$Path, [string]$Reason, [int]$RootProcessID) {
    $now = (Get-Date).ToUniversalTime().ToString('o')
    "`r`n=== $now $Reason root-pid=$RootProcessID ===" | Add-Content -LiteralPath $Path -Encoding utf8
    try {
        $drive = New-Object IO.DriveInfo 'C'
        $all = @(Get-Process -ErrorAction SilentlyContinue)
        $workingSet = ($all | Measure-Object WorkingSet64 -Sum).Sum
        "process-count=$($all.Count) process-working-set-bytes=$workingSet disk-free-bytes=$($drive.AvailableFreeSpace) disk-size-bytes=$($drive.TotalSize)" |
            Add-Content -LiteralPath $Path -Encoding utf8
    } catch {
        "resource-state-error=$($_.Exception.Message)" | Add-Content -LiteralPath $Path -Encoding utf8
    }
    try {
        $all | Where-Object {
            $_.Id -eq $RootProcessID -or
            $_.ProcessName -match '^(go|compile|link|vet|asm|cgo)$'
        } | Select-Object Id,ProcessName,StartTime,CPU,WorkingSet64,PrivateMemorySize64,
            VirtualMemorySize64,PagedMemorySize64,Handles,@{Name='ThreadCount';Expression={$_.Threads.Count}},
            Path | Format-List | Out-String -Width 4096 |
            Add-Content -LiteralPath $Path -Encoding utf8
    } catch {
        "process-state-error=$($_.Exception.Message)" | Add-Content -LiteralPath $Path -Encoding utf8
    }
}
$identityPath = Join-Path $suiteDir 'identity.json'
@{
    name=$windowsIdentity.Name
    sid=$windowsIdentity.User.Value
    elevated=$elevated
    nativeExecution=$true
    os='windows-server-2022'
    architecture='amd64'
} | ConvertTo-Json | Set-Content $identityPath -Encoding utf8

$processResults = @{}
foreach ($case in $suite.cases) {
    if ($case.kind -ne 'go') { continue }
    $eventPath = Join-Path $eventDir $case.eventFile
    $coveragePath = Join-Path $coverageDir "$($case.eventFile).coverage.out"
    $statePath = Join-Path $diagnosticDir "$($case.eventFile).state.log"
    $moduleDir = Join-Path $SourceDir $case.module
    $hardTimeoutSeconds = if ($case.PSObject.Properties['timeoutSeconds']) { [int]$case.timeoutSeconds } else { $caseTimeoutSeconds }
    if ($hardTimeoutSeconds -le 0) { throw "The $($case.id) timeout is invalid" }
    Write-TestProgress 'RUN ' "$($case.id) (hard-timeout=${hardTimeoutSeconds}s go-timeout=${goPackageTimeoutSeconds}s)"
    Push-Location $moduleDir
    try {
        $startedServices = @()
        $serviceNames = if ($case.PSObject.Properties['startServices']) { @($case.startServices) } else { @() }
        foreach ($serviceName in $serviceNames) {
            $service = Get-Service -Name $serviceName
            if ($service.Status -ne 'Stopped') { throw "Required service is not initially stopped: $serviceName" }
            Start-Service -Name $serviceName
            $startedServices += $serviceName
        }
        # Exclusive privileged cases use this same loop. The manifest also
        # requires that the privileged suite is serialized.
        $testArguments = @(
            'test', '-json', '-count=1', '-p=1', '-parallel=1',
            "-timeout=${goPackageTimeoutSeconds}s", '-covermode=atomic',
            "-coverprofile=$coveragePath"
        ) + @($case.arguments)
        $stderrPath = "$eventPath.stderr.log"
        $started = [DateTime]::UtcNow
        $timedOut = $false
        try {
            # Start-Process keeps native stderr separate and gives the harness a
            # process handle for a deadline that also covers loading and build.
            $startParameters = @{
                FilePath = (Get-Command go).Source
                ArgumentList = $testArguments
                WorkingDirectory = $moduleDir
                RedirectStandardOutput = $eventPath
                RedirectStandardError = $stderrPath
                NoNewWindow = $true
                PassThru = $true
            }
            $process = Start-Process @startParameters
            # Windows PowerShell can lose ExitCode after process exit unless a
            # native process handle is opened while the process is still live.
            $processHandle = $process.Handle
            $null = $processHandle
            Write-CaseState $statePath 'started' $process.Id
            $deadline = $started.AddSeconds($hardTimeoutSeconds)
            while (-not $process.HasExited) {
                $remainingMilliseconds = [int][Math]::Max(1, [Math]::Min(
                    $progressIntervalSeconds * 1000,
                    ($deadline - [DateTime]::UtcNow).TotalMilliseconds))
                if ($process.WaitForExit($remainingMilliseconds)) { break }
                $elapsed = [int]([DateTime]::UtcNow - $started).TotalSeconds
                Write-TestProgress 'WAIT' "$($case.id) elapsed=${elapsed}s pid=$($process.Id)"
                Write-CaseState $statePath "heartbeat elapsed=${elapsed}s" $process.Id
                if ([DateTime]::UtcNow -ge $deadline) {
                    $timedOut = $true
                    Write-TestProgress 'TIMEOUT' "$($case.id) after ${hardTimeoutSeconds}s pid=$($process.Id)"
                    Write-CaseState $statePath "hard timeout after ${hardTimeoutSeconds}s" $process.Id
                    & taskkill.exe /PID $process.Id /T /F 2>&1 |
                        Add-Content -LiteralPath $statePath -Encoding utf8
                    $process.WaitForExit()
                    break
                }
            }
            # Complete redirected stream handling before validation reads files.
            $process.WaitForExit()
            $process.Refresh()
            $processExitCode = if ($timedOut) { 124 } else { $process.ExitCode }
            $elapsed = [int]([DateTime]::UtcNow - $started).TotalSeconds
            Write-CaseState $statePath "finished elapsed=${elapsed}s exit=$processExitCode timed-out=$timedOut" $process.Id
        } catch {
            "launcher-error=$($_ | Out-String)" | Add-Content -LiteralPath $statePath -Encoding utf8
            throw
        }
        $processResults[$case.id] = $processExitCode
        Write-TestProgress $(if ($processExitCode -eq 0) { 'PASS' } else { 'FAIL' }) $case.id
        if (-not (Test-Path -LiteralPath $coveragePath -PathType Leaf)) {
            throw "The $($case.id) coverage profile is absent"
        }
        if ($processExitCode -ne 0 -and -not $ContinueAfterFailure) {
            $processPath = Join-Path $suiteDir 'processes.json'
            @{cases=$processResults} | ConvertTo-Json -Depth 5 | Set-Content $processPath -Encoding utf8
            throw "The $($case.id) test case failed; stopping the $Mode suite"
        }
    } finally {
        foreach ($serviceName in $startedServices) {
            Stop-Service -Name $serviceName -Force -ErrorAction Continue
        }
        Pop-Location
    }
}
$processPath = Join-Path $suiteDir 'processes.json'
@{cases=$processResults} | ConvertTo-Json -Depth 5 | Set-Content $processPath -Encoding utf8

$tool = Join-Path $suiteDir 'windows-test-evidence.exe'
& go -C (Join-Path $SourceDir 'bin') build -o $tool ./cmd/windows-test-evidence
if ($LASTEXITCODE -ne 0) { throw 'Cannot build the native evidence validator' }
$suiteArtifact = Join-Path $suiteDir 'suite.json'
& $tool -manifest $manifestPath -suite $suite.id -identity $identityPath -processes $processPath -events $eventDir -out $suiteArtifact
if ($LASTEXITCODE -ne 0) { throw "The $Mode suite did not pass validation" }
