param(
    [Parameter(Mandatory=$true)][ValidateSet('ordinary','privileged')][string]$Mode,
    [Parameter(Mandatory=$true)][string]$SourceDir,
    [Parameter(Mandatory=$true)][string]$ArtifactDir
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$manifestPath = Join-Path $SourceDir 'dev\winvm\test-suites.json'
$manifest = Get-Content -Raw $manifestPath | ConvertFrom-Json
$suite = $manifest.suites | Where-Object privilege -eq $Mode
if (@($suite).Count -ne 1) { throw "The $Mode suite is absent or duplicated" }

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
New-Item -ItemType Directory -Force $eventDir | Out-Null
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
    $moduleDir = Join-Path $SourceDir $case.module
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
        $testArguments = @('test', '-json', '-count=1', '-p=1', '-parallel=1') + @($case.arguments)
        $stderrPath = "$eventPath.stderr.log"
        $savedErrorActionPreference = $ErrorActionPreference
        try {
            # Go writes module-download progress to stderr. Windows PowerShell
            # converts merged native stderr into terminating error records when
            # ErrorActionPreference is Stop. Keep stderr separate so that the
            # evidence file contains only the go test JSON event stream.
            $ErrorActionPreference = 'Continue'
            & go @testArguments 2> $stderrPath |
                Out-File -FilePath $eventPath -Encoding utf8
            $processExitCode = $LASTEXITCODE
        } finally {
            $ErrorActionPreference = $savedErrorActionPreference
        }
        $processResults[$case.id] = $processExitCode
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
