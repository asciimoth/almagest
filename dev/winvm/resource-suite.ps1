param(
    [Parameter(Mandatory=$true)][string]$SourceDir,
    [Parameter(Mandatory=$true)][string]$ArtifactDir,
    [string]$SoakDuration
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
if (-not $identity.IsSystem) { throw 'The resource gate must run as SYSTEM' }
if ([Environment]::OSVersion.Version.Build -ne 20348 -or $env:PROCESSOR_ARCHITECTURE -ne 'AMD64') {
    throw 'The resource gate requires Windows Server 2022/amd64'
}

$suiteDir = Join-Path $ArtifactDir 'windows-m8-resource'
$eventDir = Join-Path $suiteDir 'events'
New-Item -ItemType Directory -Force $suiteDir,$eventDir | Out-Null
. (Join-Path $SourceDir 'dev\winvm\resource-state.ps1')

# These limits and the settling period are qualification inputs. Do not tune
# them from the result of a failed qualification run.
$settlingSeconds = 10
$budgets = [ordered]@{
    maximumHandleGrowth = 8
    maximumGoroutineGrowth = 4
    maximumPrivateByteGrowth = 67108864
}
$effectiveSoak = if ($SoakDuration) { $SoakDuration } else { '30m' }
$diagnosticOverride = [bool]$SoakDuration
$before = Get-NativeResourceSnapshot
$before | ConvertTo-Json -Depth 8 | Set-Content (Join-Path $suiteDir 'resources-before.json')
$startedAt = (Get-Date).ToUniversalTime().ToString('o')
$results = [ordered]@{}

function Invoke-ResourceTest(
    [string]$ID,
    [string]$Module,
    [string[]]$Arguments,
    [string]$EventFile,
    [string[]]$RequiredTests,
    [hashtable]$Environment = @{}
) {
    Write-Output "[resource] RUN $ID"
    $saved = @{}
    foreach ($entry in $Environment.GetEnumerator()) {
        $saved[$entry.Key] = [Environment]::GetEnvironmentVariable($entry.Key, 'Process')
        [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, 'Process')
    }
    $eventPath = Join-Path $eventDir $EventFile
    Push-Location (Join-Path $SourceDir $Module)
    try {
        & go test -json -count=1 -p=1 -parallel=1 -timeout=55m @Arguments 2>&1 |
            Tee-Object -FilePath $eventPath
        if ($LASTEXITCODE -ne 0) { throw "$ID failed" }
    } finally {
        Pop-Location
        foreach ($entry in $Environment.GetEnumerator()) {
            [Environment]::SetEnvironmentVariable($entry.Key, $saved[$entry.Key], 'Process')
        }
    }
    $events = @(Get-Content $eventPath | Where-Object { $_ -match '^\s*\{' } | ConvertFrom-Json)
    foreach ($test in $RequiredTests) {
        $passed = @($events | Where-Object { $_.Test -eq $test -and $_.Action -eq 'pass' })
        $skipped = @($events | Where-Object { $_.Test -eq $test -and $_.Action -eq 'skip' })
        if ($passed.Count -ne 1 -or $skipped.Count -ne 0) { throw "$ID did not pass required test $test exactly once" }
    }
    $script:results[$ID] = 'passed'
    Write-Output "[resource] PASS $ID"
}

$resourceReport = Join-Path $suiteDir 'process-resources.json'
$backendEnvironment = @{
    SYSNET_RESOURCE_ARTIFACT = $resourceReport
    SYSNET_WINDOWS_SOAK_DURATION = $effectiveSoak
    SYSNET_WINDOWS_WINTUN_DLL = (Join-Path $env:SystemRoot 'System32\wintun.dll')
}
$serviceStarted = $false
try {
    $service = Get-Service -Name 'mullvad-split-tunnel'
    if ($service.Status -ne 'Stopped') { throw 'The split-driver service must be stopped at resource-gate start' }
    Start-Service -Name $service.Name
    $serviceStarted = $true
    Invoke-ResourceTest 'backend-lifecycle-cancellation-soak' 'bin' @(
        '-tags=winintegration,winresource',
        'github.com/asciimoth/sysnet-windows/integration',
        '-run', '^Test(M6(ResourceLifecycleBatches|CancellationCycles|NativeTransferReconfigurationSoak)|M3NativeDefaultTunCrashResourceRecovery)$'
    ) 'backend-resource.jsonl' @(
        'TestM6ResourceLifecycleBatches',
        'TestM6CancellationCycles',
        'TestM6NativeTransferReconfigurationSoak',
        'TestM3NativeDefaultTunCrashResourceRecovery'
    ) $backendEnvironment
    Invoke-ResourceTest 'daemon-graph-and-forced-restart' 'bin' @(
        '-tags=winintegration', './integration', '-run',
        '^TestWindowsDaemonNative(RepeatedGraphChanges|ForcedTerminationAndRestart)$'
    ) 'daemon-resource.jsonl' @(
        'TestWindowsDaemonNativeRepeatedGraphChanges',
        'TestWindowsDaemonNativeForcedTerminationAndRestart'
    ) @{SYSNET_WINDOWS_WINTUN_DLL=(Join-Path $env:SystemRoot 'System32\wintun.dll')}
    Invoke-ResourceTest 'fault-injection-recovery-contracts' 'bin' @(
        'github.com/asciimoth/sysnet-windows', '-run',
        '^Test(SystemCloseRetriesAfter(Resource|Worker|JournalCallback)Timeout|D17D20DNSRestorationIsOwnershipAware|T16T18RegularTunRejectsForeignClosedAndReusedIdentity|T22T24RegularTunExternalChangeHasVisibleFailedState|RegularTunCloseRetriesAfterTransientBusyRejection)$'
    ) 'fault-injection.jsonl' @(
        'TestSystemCloseRetriesAfterResourceTimeout',
        'TestSystemCloseRetriesAfterWorkerTimeout',
        'TestSystemCloseRetriesAfterJournalCallbackTimeout',
        'TestD17D20DNSRestorationIsOwnershipAware',
        'TestT16T18RegularTunRejectsForeignClosedAndReusedIdentity',
        'TestT22T24RegularTunExternalChangeHasVisibleFailedState',
        'TestRegularTunCloseRetriesAfterTransientBusyRejection'
    )
    Invoke-ResourceTest 'fault-injection-split-recovery-contracts' 'bin' @(
        'github.com/asciimoth/sysnet-windows/internal/split', '-run',
        '^TestR37R40Session(ReconcilesCommittedResetError|RetainsJournalAndRetriesExplicitRecovery|RejectsZombieAndChangedDeployment)$'
    ) 'fault-injection-split.jsonl' @(
        'TestR37R40SessionReconcilesCommittedResetError',
        'TestR37R40SessionRetainsJournalAndRetriesExplicitRecovery',
        'TestR37R40SessionRejectsZombieAndChangedDeployment'
    )
} finally {
    if ($serviceStarted) {
        Stop-Service -Name 'mullvad-split-tunnel' -Force -ErrorAction Continue
        (Get-Service -Name 'mullvad-split-tunnel').WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))
    }
}

Start-Sleep -Seconds $settlingSeconds
$after = Get-NativeResourceSnapshot
$after | ConvertTo-Json -Depth 8 | Set-Content (Join-Path $suiteDir 'resources-after.json')
Compare-NativeResourceSnapshot $before $after

$report = Get-Content $resourceReport -Raw | ConvertFrom-Json
$first = $report.samples[0]
$last = $report.samples[$report.samples.Count - 1]
if (($last.handles - $first.handles) -gt $budgets.maximumHandleGrowth) { throw 'Handle growth exceeds the fixed resource budget' }
if (($last.goroutines - $first.goroutines) -gt $budgets.maximumGoroutineGrowth) { throw 'Goroutine growth exceeds the fixed resource budget' }
if (($last.privateBytes - $first.privateBytes) -gt $budgets.maximumPrivateByteGrowth) { throw 'Private-memory growth exceeds the fixed resource budget' }

$faults = @(
    [ordered]@{case='dns-external-edit'; method='fault-injection'; retained='foreign DNS state'; recovery='owned state is removed without overwriting the foreign edit'},
    [ordered]@{case='adapter-name-reuse'; method='fault-injection'; retained='foreign replacement adapter identity'; recovery='operation rejects the stale identity'},
    [ordered]@{case='busy-close-and-failed-reset'; method='fault-injection'; retained='owned journal and resource handles'; recovery='explicit close retry uses the retained recovery information'},
    [ordered]@{case='zombie-or-changed-driver'; method='fault-injection'; retained='committed WFP ownership journal'; recovery='automatic destructive reset is rejected; operator recovery is required'}
)
[ordered]@{
    schemaVersion=1
    suite='windows-m8-resource'
    outcome='passed'
    qualifying=(-not $diagnosticOverride)
    startedAt=$startedAt
    finishedAt=(Get-Date).ToUniversalTime().ToString('o')
    platformId='windows-server-2022-amd64'
    identity=[ordered]@{name=$identity.Name; sid=$identity.User.Value; elevated=$true}
    workload=[ordered]@{warmupCycles=10; batchCount=4; cyclesPerBatch=25; cancellationCycles=100; soakDuration=$effectiveSoak; diagnosticOverride=$diagnosticOverride}
    settlingSeconds=$settlingSeconds
    budgets=$budgets
    cases=$results
    failureInjection=$faults
    recoveryJournal=[ordered]@{persistentCrossProcessSplitJournal=$false; manualRecovery='Stop Almagest and the split-driver service. Preserve logs and WFP ownership data. Remove only verified Almagest-owned WFP objects, then restart the provisioned driver service.'}
} | ConvertTo-Json -Depth 8 | Set-Content (Join-Path $suiteDir 'suite.json')
