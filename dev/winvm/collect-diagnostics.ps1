param(
    [Parameter(Mandatory=$true)][string]$OutputDir,
    [Parameter(Mandatory=$true)][string]$Reason,
    [Parameter(Mandatory=$true)][string]$Stage
)
$ErrorActionPreference = 'Continue'
Set-StrictMode -Version Latest

New-Item -ItemType Directory -Force $OutputDir | Out-Null

# Small crash dumps are copied before slower CIM/event queries. The full kernel
# dump stays on the retained VM disk; it can be hundreds of MiB or more.
$dumpDir = Join-Path $OutputDir 'minidumps'
New-Item -ItemType Directory -Force $dumpDir | Out-Null
Get-ChildItem -LiteralPath (Join-Path $env:SystemRoot 'Minidump') -Filter '*.dmp' -ErrorAction SilentlyContinue |
    Copy-Item -Destination $dumpDir -Force -ErrorAction Continue
Get-Item -LiteralPath (Join-Path $env:SystemRoot 'MEMORY.DMP') -ErrorAction SilentlyContinue |
    Select-Object FullName,Length,CreationTimeUtc,LastWriteTimeUtc |
    Format-List | Out-File (Join-Path $OutputDir 'kernel-dump.txt') -Encoding utf8

function Capture-Text([string]$Name, [scriptblock]$Action) {
    $path = Join-Path $OutputDir $Name
    try {
        & $Action 2>&1 | Out-File -LiteralPath $path -Encoding utf8 -Width 4096
    } catch {
        $_ | Format-List * -Force | Out-File -LiteralPath $path -Encoding utf8 -Width 4096
    }
}

Capture-Text 'summary.txt' {
    $os = Get-CimInstance Win32_OperatingSystem
    [pscustomobject]@{
        capturedAtUTC = (Get-Date).ToUniversalTime().ToString('o')
        reason = $Reason
        stage = $Stage
        computerName = $env:COMPUTERNAME
        identity = [Security.Principal.WindowsIdentity]::GetCurrent().Name
        lastBootUTC = $os.LastBootUpTime.ToUniversalTime().ToString('o')
        localDateTime = $os.LocalDateTime
        freePhysicalMemoryKiB = $os.FreePhysicalMemory
        totalVisibleMemoryKiB = $os.TotalVisibleMemorySize
        freeVirtualMemoryKiB = $os.FreeVirtualMemory
        totalVirtualMemoryKiB = $os.TotalVirtualMemorySize
        processCount = $os.NumberOfProcesses
    } | Format-List
    Get-CimInstance Win32_LogicalDisk | Select-Object DeviceID,DriveType,FileSystem,
        Size,FreeSpace,VolumeName | Format-Table -AutoSize
}

Capture-Text 'processes.txt' {
    Get-CimInstance Win32_Process | Sort-Object ProcessId | Select-Object ProcessId,
        ParentProcessId,Name,CreationDate,KernelModeTime,UserModeTime,WorkingSetSize,
        ReadOperationCount,WriteOperationCount,ReadTransferCount,WriteTransferCount,
        ThreadCount,HandleCount,ExecutablePath,CommandLine | Format-List
}

Capture-Text 'go-processes.txt' {
    Get-CimInstance Win32_Process | Where-Object {
        $_.Name -match '^(go|compile|link|vet|asm|cgo)(\.exe)?$'
    } | Sort-Object ProcessId | Select-Object * | Format-List
}

Capture-Text 'services.txt' {
    Get-CimInstance Win32_Service | Sort-Object Name | Select-Object Name,State,
        StartMode,Status,ProcessId,ExitCode,PathName | Format-Table -AutoSize
}

Capture-Text 'network.txt' {
    Get-NetAdapter | Sort-Object ifIndex | Format-List Name,InterfaceDescription,
        ifIndex,Status,LinkSpeed,MacAddress,DriverInformation
    ipconfig.exe /all
    route.exe print
    netstat.exe -ano
}

Capture-Text 'system-events.txt' {
    Get-WinEvent -FilterHashtable @{
        LogName='System'; StartTime=(Get-Date).AddHours(-4); Level=1,2,3
    } -MaxEvents 300 | Select-Object TimeCreated,Id,LevelDisplayName,ProviderName,Message |
        Format-List
}

Capture-Text 'application-events.txt' {
    Get-WinEvent -FilterHashtable @{
        LogName='Application'; StartTime=(Get-Date).AddHours(-4); Level=1,2,3
    } -MaxEvents 300 | Select-Object TimeCreated,Id,LevelDisplayName,ProviderName,Message |
        Format-List
}

Capture-Text 'defender-events.txt' {
    Get-WinEvent -FilterHashtable @{
        LogName='Microsoft-Windows-Windows Defender/Operational'
        StartTime=(Get-Date).AddHours(-4)
    } -MaxEvents 300 | Select-Object TimeCreated,Id,LevelDisplayName,ProviderName,Message |
        Format-List
}

Capture-Text 'test-artifact-inventory.txt' {
    $artifactRoot = Split-Path -Parent $OutputDir
    Get-ChildItem -LiteralPath $artifactRoot -Recurse -Force | Sort-Object FullName |
        Select-Object FullName,Length,CreationTimeUtc,LastWriteTimeUtc | Format-Table -AutoSize
}

Capture-Text 'active-test-tails.txt' {
    $artifactRoot = Split-Path -Parent $OutputDir
    $diagnosticRoot = (Get-Item -LiteralPath $OutputDir).FullName.TrimEnd('\') + '\'
    Get-ChildItem -LiteralPath $artifactRoot -Recurse -File |
        Where-Object {
            $_.Extension -in '.log','.jsonl','.txt' -and
            -not $_.FullName.StartsWith($diagnosticRoot, [StringComparison]::OrdinalIgnoreCase)
        } |
        Sort-Object FullName | ForEach-Object {
            "`r`n===== $($_.FullName) ====="
            Get-Content -LiteralPath $_.FullName -Tail 100
        }
}

foreach ($log in @(
    @{Name='System'; File='system.evtx'},
    @{Name='Application'; File='application.evtx'},
    @{Name='Microsoft-Windows-Windows Defender/Operational'; File='defender.evtx'}
)) {
    $query = '*[System[TimeCreated[timediff(@SystemTime) <= 14400000]]]'
    & wevtutil.exe epl $log.Name (Join-Path $OutputDir $log.File) "/q:$query" /ow:true 2>>(
        Join-Path $OutputDir 'event-export-errors.log')
}

[IO.File]::WriteAllText((Join-Path $OutputDir 'complete.txt'),
    (Get-Date).ToUniversalTime().ToString('o'))
