<#
.SYNOPSIS
Collects an Almagest support archive from an installed Windows system.

.DESCRIPTION
The script requests administrator access through User Account Control when it
does not already have it. It preserves information about the original user
token and writes a ZIP archive that contains report.txt, installer logs, daemon
logs, desktop-app logs, and filtered event-log exports. It does not copy
Almagest configuration, databases, or process memory.

.PARAMETER RunExperiments
Stops and starts almagestd, then monitors it and the desktop app. This briefly
interrupts the active Almagest network graph.

.EXAMPLE
powershell.exe -ExecutionPolicy Bypass -File .\scripts\collect-windows-support.ps1

.EXAMPLE
powershell.exe -ExecutionPolicy Bypass -File .\scripts\collect-windows-support.ps1 -RunExperiments
#>
[CmdletBinding()]
param(
    [string]$OutputPath,
    [ValidateRange(1, 168)][int]$EventHours = 24,
    [switch]$RunExperiments,
    [ValidateRange(30, 900)][int]$MonitorSeconds = 240,
    [switch]$Elevated,
    [string]$OriginalIdentity,
    [string]$OriginalSid,
    [int]$OriginalSessionId = -1,
    [string]$PreElevationReport
)

$ErrorActionPreference = 'Continue'
Set-StrictMode -Version Latest

function Test-IsAdministrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($identity)
    $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function ConvertTo-QuotedProcessArgument([string]$Value) {
    if ($null -eq $Value) { return '""' }
    '"' + $Value.Replace('"', '\"') + '"'
}

if ($env:OS -ne 'Windows_NT') {
    throw 'This script must run on Windows.'
}

if ([string]::IsNullOrWhiteSpace($OutputPath)) {
    $stamp = (Get-Date).ToUniversalTime().ToString('yyyyMMddTHHmmssZ')
    $publicDocuments = Join-Path $env:PUBLIC 'Documents'
    $OutputPath = Join-Path $publicDocuments "almagest-support-$stamp.zip"
}
$OutputPath = [IO.Path]::GetFullPath($OutputPath)
if ([IO.Path]::GetExtension($OutputPath) -ne '.zip') {
    throw 'OutputPath must name a .zip file.'
}
if (Test-Path -LiteralPath $OutputPath) {
    throw "Output file already exists: $OutputPath"
}

if (-not (Test-IsAdministrator)) {
    if ($Elevated) {
        throw 'The elevated collector does not have an administrator token.'
    }
    $currentIdentity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $currentProcess = Get-Process -Id $PID
    $preElevation = Join-Path $env:TEMP ("almagest-pre-elevation-{0}.txt" -f [guid]::NewGuid())
    @(
        'Almagest pre-elevation identity report'
        "Captured UTC: $((Get-Date).ToUniversalTime().ToString('o'))"
        "Identity: $($currentIdentity.Name)"
        "SID: $($currentIdentity.User.Value)"
        "Session ID: $($currentProcess.SessionId)"
        ''
        'whoami /all'
        (& whoami.exe /all 2>&1 | Out-String)
    ) | Out-File -LiteralPath $preElevation -Encoding utf8 -Width 4096

    $arguments = @(
        '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $PSCommandPath,
        '-OutputPath', $OutputPath,
        '-EventHours', $EventHours,
        '-MonitorSeconds', $MonitorSeconds,
        '-Elevated',
        '-OriginalIdentity', $currentIdentity.Name,
        '-OriginalSid', $currentIdentity.User.Value,
        '-OriginalSessionId', $currentProcess.SessionId,
        '-PreElevationReport', $preElevation
    )
    if ($RunExperiments) { $arguments += '-RunExperiments' }
    $argumentLine = ($arguments | ForEach-Object { ConvertTo-QuotedProcessArgument ([string]$_) }) -join ' '

    Write-Output 'Administrator access is required to inspect and control the service.'
    Write-Output 'Windows will now show a User Account Control prompt.'
    try {
        $process = Start-Process -FilePath 'powershell.exe' -ArgumentList $argumentLine `
            -Verb RunAs -Wait -PassThru
    } catch {
        Remove-Item -LiteralPath $preElevation -Force -ErrorAction SilentlyContinue
        throw "Could not start the elevated collector: $($_.Exception.Message)"
    }
    Remove-Item -LiteralPath $preElevation -Force -ErrorAction SilentlyContinue
    if ($process.ExitCode -ne 0) {
        throw "The elevated collector failed with exit code $($process.ExitCode)."
    }
    Write-Output "Almagest support report: $OutputPath"
    return
}

if ([string]::IsNullOrWhiteSpace($OriginalIdentity)) {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $OriginalIdentity = $identity.Name
    $OriginalSid = $identity.User.Value
    $OriginalSessionId = (Get-Process -Id $PID).SessionId
}

$stagingRoot = Join-Path $env:TEMP ("almagest-support-{0}" -f [guid]::NewGuid())
$logsRoot = Join-Path $stagingRoot 'logs'
$daemonLogsRoot = Join-Path $logsRoot 'daemon'
$appLogsRoot = Join-Path $logsRoot 'app'
$installerLogsRoot = Join-Path $logsRoot 'installer'
$eventsRoot = Join-Path $stagingRoot 'events'
$reportPath = Join-Path $stagingRoot 'report.txt'
$errorsPath = Join-Path $stagingRoot 'collector-errors.txt'
New-Item -ItemType Directory -Force $stagingRoot,$logsRoot,$daemonLogsRoot,$appLogsRoot,`
    $installerLogsRoot,$eventsRoot | Out-Null

function Get-ProfilePathForSid([string]$Sid) {
    if ([string]::IsNullOrWhiteSpace($Sid)) { return $null }
    $key = "Registry::HKEY_LOCAL_MACHINE\SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList\$Sid"
    $profilePath = (Get-ItemProperty -LiteralPath $key -Name ProfileImagePath -ErrorAction SilentlyContinue).ProfileImagePath
    if ([string]::IsNullOrWhiteSpace($profilePath)) { return $null }
    [Environment]::ExpandEnvironmentVariables($profilePath)
}

function Add-ReportLine([string]$Text = '') {
    $Text | Out-File -LiteralPath $reportPath -Append -Encoding utf8 -Width 4096
}

function Add-ReportSection([string]$Name, [scriptblock]$Action) {
    Add-ReportLine
    Add-ReportLine ('=' * 78)
    Add-ReportLine $Name
    Add-ReportLine ('=' * 78)
    try {
        & $Action 2>&1 | Out-File -LiteralPath $reportPath -Append -Encoding utf8 -Width 4096
    } catch {
        $_ | Format-List * -Force |
            Out-File -LiteralPath $reportPath -Append -Encoding utf8 -Width 4096
    }
}

function Get-AlmagestProcess {
    $allProcesses = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue)
    $allProcesses |
        Where-Object { $_.Name -in @('almagestd.exe','almagest-app.exe','almagest-cli.exe') } |
        Sort-Object Name,ProcessId | ForEach-Object {
            $owner = Invoke-CimMethod -InputObject $_ -MethodName GetOwner -ErrorAction SilentlyContinue
            $parentId = $_.ParentProcessId
            $parent = $allProcesses | Where-Object { $_.ProcessId -eq $parentId } | Select-Object -First 1
            $responding = $null
            try { $responding = (Get-Process -Id $_.ProcessId -ErrorAction Stop).Responding } catch { $responding = $null }
            [pscustomobject]@{
                Name = $_.Name
                ProcessId = $_.ProcessId
                ParentProcessId = $parentId
                ParentName = if ($parent) { $parent.Name } else { '<exited or unavailable>' }
                ParentSessionId = if ($parent) { $parent.SessionId } else { $null }
                SessionId = $_.SessionId
                Owner = if ($owner -and $owner.ReturnValue -eq 0) { "$($owner.Domain)\$($owner.User)" } else { '<unavailable>' }
                CreationDate = $_.CreationDate
                Responding = $responding
                ThreadCount = $_.ThreadCount
                HandleCount = $_.HandleCount
                WorkingSetSize = $_.WorkingSetSize
                ExecutablePath = $_.ExecutablePath
            }
        }
}

function Get-ServiceSnapshot([string]$Label) {
    $now = (Get-Date).ToUniversalTime().ToString('o')
    "[$now] $Label"
    Get-CimInstance Win32_Service -Filter "Name='almagestd'" -ErrorAction SilentlyContinue |
        Select-Object Name,State,Status,StartMode,ProcessId,ExitCode,ServiceSpecificExitCode,
            PathName,StartName | Format-List
    Get-AlmagestProcess | Format-List
    "Machine pipe present: $(Test-Path -LiteralPath '\\.\pipe\almagest-admin')"
}

function Find-AlmagestCli {
    $candidates = @(
        (Join-Path $env:ProgramFiles 'Almagest\almagest-cli.exe'),
        (Get-Command almagest-cli.exe -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Source -First 1)
    ) | Where-Object { $_ -and (Test-Path -LiteralPath $_) }
    $candidates | Select-Object -First 1
}

function Invoke-CliProbe([string]$Cli, [string]$Label, [int]$TimeoutMilliseconds = 8000) {
    $stdout = Join-Path $stagingRoot ("cli-{0}-stdout.txt" -f [guid]::NewGuid())
    $stderr = Join-Path $stagingRoot ("cli-{0}-stderr.txt" -f [guid]::NewGuid())
    try {
        $nonce = "support-$Label-$([guid]::NewGuid().ToString('N'))"
        $process = Start-Process -FilePath $Cli -ArgumentList @(
            '--url','pipe://machine','admin','ping','--nonce',$nonce
        ) -RedirectStandardOutput $stdout -RedirectStandardError $stderr -PassThru
        if (-not $process.WaitForExit($TimeoutMilliseconds)) {
            $process.Kill()
            $process.WaitForExit()
            return [pscustomobject]@{ Label=$Label; Result='timeout'; ExitCode=$null; Output=''; Error='' }
        }
        $process.Refresh()
        [pscustomobject]@{
            Label = $Label
            Result = if ($process.ExitCode -eq 0) { 'success' } else { 'failure' }
            ExitCode = $process.ExitCode
            Output = (Get-Content -LiteralPath $stdout -Raw -ErrorAction SilentlyContinue)
            Error = (Get-Content -LiteralPath $stderr -Raw -ErrorAction SilentlyContinue)
        }
    } catch {
        [pscustomobject]@{ Label=$Label; Result='error'; ExitCode=$null; Output=''; Error=$_.Exception.Message }
    } finally {
        Remove-Item -LiteralPath $stdout,$stderr -Force -ErrorAction SilentlyContinue
    }
}

function Wait-ServiceState([string]$State, [int]$TimeoutSeconds) {
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    do {
        $service = Get-CimInstance Win32_Service -Filter "Name='almagestd'" -ErrorAction SilentlyContinue
        if ($service -and $service.State -eq $State) { return $true }
        Start-Sleep -Milliseconds 500
    } while ([DateTime]::UtcNow -lt $deadline)
    $false
}

function Invoke-ServiceStartTrace([int]$TimeoutSeconds = 45) {
    $stdout = Join-Path $stagingRoot ("sc-start-{0}-stdout.txt" -f [guid]::NewGuid())
    $stderr = Join-Path $stagingRoot ("sc-start-{0}-stderr.txt" -f [guid]::NewGuid())
    $start = (Get-Date).ToUniversalTime()
    $sc = Start-Process -FilePath 'sc.exe' -ArgumentList @('start','almagestd') `
        -RedirectStandardOutput $stdout -RedirectStandardError $stderr -PassThru
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    $sample = 0
    do {
        $sample++
        $elapsed = [math]::Round(((Get-Date).ToUniversalTime() - $start).TotalSeconds, 3)
        "START TRACE sample=$sample elapsedSeconds=$elapsed scExited=$($sc.HasExited)"
        Get-CimInstance Win32_Service -Filter "Name='almagestd'" -ErrorAction SilentlyContinue |
            Select-Object Name,State,Status,ProcessId,ExitCode,ServiceSpecificExitCode | Format-List
        Get-AlmagestProcess | Format-List
        Get-CimInstance Win32_Process -Filter "Name='services.exe'" -ErrorAction SilentlyContinue |
            Select-Object Name,ProcessId,ParentProcessId,SessionId,CreationDate,ExecutablePath |
            Format-List
        if ($sc.HasExited) { break }
        Start-Sleep -Milliseconds 500
        $sc.Refresh()
    } while ([DateTime]::UtcNow -lt $deadline)
    if (-not $sc.HasExited) {
        "sc.exe start did not finish within $TimeoutSeconds seconds; terminating only the diagnostic sc.exe client."
        $sc.Kill()
        $sc.WaitForExit()
    }
    $sc.Refresh()
    [pscustomobject]@{
        ExitCode = $sc.ExitCode
        Output = (Get-Content -LiteralPath $stdout -Raw -ErrorAction SilentlyContinue)
        Error = (Get-Content -LiteralPath $stderr -Raw -ErrorAction SilentlyContinue)
    } | Format-List
    Remove-Item -LiteralPath $stdout,$stderr -Force -ErrorAction SilentlyContinue
}

Add-ReportLine 'Almagest Windows support report'
Add-ReportLine "Capture started UTC: $((Get-Date).ToUniversalTime().ToString('o'))"
Add-ReportLine "Collector script: $PSCommandPath"
Add-ReportLine "Experiments requested: $([bool]$RunExperiments)"
Add-ReportLine "Event history hours: $EventHours"
Add-ReportLine "Monitor seconds: $MonitorSeconds"
Add-ReportLine 'Privacy note: configuration files, databases, process command lines, and process dumps are not copied.'

if ($PreElevationReport -and (Test-Path -LiteralPath $PreElevationReport)) {
    Copy-Item -LiteralPath $PreElevationReport -Destination (Join-Path $stagingRoot 'pre-elevation-identity.txt') -Force
}

Add-ReportSection 'Identity and elevation' {
    [pscustomobject]@{
        ElevatedIdentity = [Security.Principal.WindowsIdentity]::GetCurrent().Name
        IsAdministrator = Test-IsAdministrator
        OriginalIdentity = $OriginalIdentity
        OriginalSid = $OriginalSid
        OriginalSessionId = $OriginalSessionId
        PowerShellVersion = $PSVersionTable.PSVersion.ToString()
        PowerShellEdition = $PSVersionTable.PSEdition
        Is64BitProcess = [Environment]::Is64BitProcess
    } | Format-List
    whoami.exe /all
    quser.exe
    if (Get-Command Get-LocalGroupMember -ErrorAction SilentlyContinue) {
        Get-LocalGroupMember -Group 'Almagest Administrators' -ErrorAction Continue | Format-List *
    } else {
        net.exe localgroup 'Almagest Administrators'
    }
}

Add-ReportSection 'Operating system and boot state' {
    Get-CimInstance Win32_OperatingSystem | Select-Object Caption,Version,BuildNumber,
        OSArchitecture,LastBootUpTime,LocalDateTime,FreePhysicalMemory,TotalVisibleMemorySize |
        Format-List
    Get-CimInstance Win32_ComputerSystem | Select-Object Manufacturer,Model,SystemType,
        TotalPhysicalMemory,Domain,PartOfDomain | Format-List
    Get-TimeZone | Format-List *
    bcdedit.exe /enum '{current}'
}

Add-ReportSection 'Service configuration and current state' {
    Get-ServiceSnapshot 'initial state'
    sc.exe qc almagestd
    sc.exe queryex almagestd
    sc.exe qfailure almagestd
    sc.exe qfailureflag almagestd
    sc.exe qtriggerinfo almagestd
    sc.exe sdshow almagestd
}

Add-ReportSection 'Installed package and autostart' {
    $uninstallRoots = @(
        'Registry::HKEY_LOCAL_MACHINE\Software\Microsoft\Windows\CurrentVersion\Uninstall\*',
        'Registry::HKEY_LOCAL_MACHINE\Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\*'
    )
    Get-ItemProperty $uninstallRoots -ErrorAction SilentlyContinue |
        Where-Object {
            $_.PSObject.Properties['DisplayName'] -and $_.DisplayName -match 'Almagest'
        } |
        Select-Object DisplayName,DisplayVersion,Publisher,InstallDate,InstallLocation,
            UninstallString,PSPath | Format-List
    Get-ItemProperty 'Registry::HKEY_LOCAL_MACHINE\Software\Microsoft\Windows\CurrentVersion\Run' `
        -ErrorAction SilentlyContinue | Select-Object Almagest,PSPath | Format-List
    if ($OriginalSid) {
        Get-ItemProperty "Registry::HKEY_USERS\$OriginalSid\Software\Microsoft\Windows\CurrentVersion\Run" `
            -ErrorAction SilentlyContinue | Select-Object Almagest,PSPath | Format-List
    }
    Get-ScheduledTask -ErrorAction SilentlyContinue |
        Where-Object { $_.TaskName -match 'Almagest' -or $_.TaskPath -match 'Almagest' } |
        Format-List *
}

Add-ReportSection 'Almagest process inventory' {
    Get-AlmagestProcess | Format-List
    Get-CimInstance Win32_Process -Filter "Name='services.exe'" -ErrorAction SilentlyContinue |
        Select-Object Name,ProcessId,ParentProcessId,SessionId,CreationDate,ExecutablePath |
        Format-List
    tasklist.exe /svc /fi 'IMAGENAME eq almagestd.exe'
    tasklist.exe /v /fi 'IMAGENAME eq almagestd.exe'
    tasklist.exe /v /fi 'IMAGENAME eq almagest-app.exe'
}

Add-ReportSection 'Installed binary metadata' {
    $installRoot = Join-Path $env:ProgramFiles 'Almagest'
    Get-ChildItem -LiteralPath $installRoot -File -ErrorAction SilentlyContinue |
        Where-Object { $_.Extension -in @('.exe','.dll','.sys') } |
        Sort-Object Name | ForEach-Object {
            $hash = Get-FileHash -Algorithm SHA256 -LiteralPath $_.FullName
            $signature = Get-AuthenticodeSignature -LiteralPath $_.FullName
            [pscustomobject]@{
                Name = $_.Name
                FullName = $_.FullName
                Length = $_.Length
                LastWriteTimeUtc = $_.LastWriteTimeUtc
                FileVersion = $_.VersionInfo.FileVersion
                ProductVersion = $_.VersionInfo.ProductVersion
                SHA256 = $hash.Hash
                SignatureStatus = $signature.Status
                Signer = if ($signature.SignerCertificate) { $signature.SignerCertificate.Subject } else { $null }
            }
        } | Format-List
}

Add-ReportSection 'Filesystem and registry permissions' {
    icacls.exe (Join-Path $env:ProgramFiles 'Almagest')
    icacls.exe (Join-Path $env:ProgramData 'Almagest')
    icacls.exe (Join-Path $env:ProgramData 'Almagest\logs')
    Get-Acl 'Registry::HKEY_LOCAL_MACHINE\Software\Microsoft\Windows\CurrentVersion\Run' |
        Format-List *
}

Add-ReportSection 'Named pipes and network state' {
    Get-ChildItem -LiteralPath '\\.\pipe\' -ErrorAction SilentlyContinue |
        Where-Object { $_.Name -match 'almagest' } | Select-Object Name,FullName | Format-Table -AutoSize
    Get-NetAdapter -ErrorAction SilentlyContinue | Sort-Object ifIndex |
        Select-Object Name,InterfaceDescription,ifIndex,Status,LinkSpeed,MacAddress,DriverInformation |
        Format-List
    ipconfig.exe /all
    route.exe print
    netstat.exe -ano
}

Add-ReportSection 'Drivers relevant to Almagest' {
    sc.exe query mullvad-split-tunnel
    sc.exe qc mullvad-split-tunnel
    driverquery.exe /v /fo list
    pnputil.exe /enum-drivers
}

$cli = Find-AlmagestCli
Add-ReportSection 'Admin API probes before experiments' {
    "CLI path: $cli"
    if ($cli) {
        Invoke-CliProbe $cli 'before-experiments' | Format-List
        & $cli --url pipe://machine admin --help 2>&1
    }
}

if ($RunExperiments) {
    Add-ReportSection 'Controlled service stop/start experiment' {
        'WARNING: This experiment briefly stops the daemon and active network graph.'
        $initialService = Get-CimInstance Win32_Service -Filter "Name='almagestd'" -ErrorAction SilentlyContinue
        $initialState = if ($initialService) { $initialService.State } else { 'Absent' }
        "Initial service state: $initialState"
        Get-ServiceSnapshot 'before service control'
        if ($cli) { Invoke-CliProbe $cli 'before-stop' | Format-List }

        try {
            if ($initialState -eq 'Running') {
                "sc.exe stop issued UTC: $((Get-Date).ToUniversalTime().ToString('o'))"
                sc.exe stop almagestd
                "Reached Stopped: $(Wait-ServiceState 'Stopped' 45)"
                Get-ServiceSnapshot 'after stop'
                if ($cli) { Invoke-CliProbe $cli 'while-stopped' | Format-List }
                Start-Sleep -Seconds 5
            }

            if ($initialState -ne 'Absent') {
                "Asynchronous sc.exe start issued UTC: $((Get-Date).ToUniversalTime().ToString('o'))"
                Invoke-ServiceStartTrace 45
                $reachedRunning = Wait-ServiceState 'Running' 5
                "Reached Running: $reachedRunning"
                Get-ServiceSnapshot 'after start'
                if ($cli) { Invoke-CliProbe $cli 'after-start' | Format-List }

                if ($reachedRunning) {
                    $deadline = [DateTime]::UtcNow.AddSeconds($MonitorSeconds)
                    $sample = 0
                    while ([DateTime]::UtcNow -lt $deadline) {
                        $sample++
                        Get-ServiceSnapshot "monitor sample $sample"
                        if ($cli -and ($sample % 2 -eq 0)) {
                            Invoke-CliProbe $cli "monitor-$sample" | Format-List
                        }
                        Start-Sleep -Seconds 5
                    }
                } else {
                    'Skipping the long monitor because the service did not reach Running.'
                }
            }
        } finally {
            $current = Get-CimInstance Win32_Service -Filter "Name='almagestd'" -ErrorAction SilentlyContinue
            if ($initialState -eq 'Running' -and $current -and $current.State -ne 'Running') {
                'Restoring initial Running service state.'
                sc.exe start almagestd
                [void](Wait-ServiceState 'Running' 90)
            } elseif ($initialState -eq 'Stopped' -and $current -and $current.State -ne 'Stopped') {
                'Restoring initial Stopped service state.'
                sc.exe stop almagestd
                [void](Wait-ServiceState 'Stopped' 45)
            }
            Get-ServiceSnapshot 'final restored state'
        }
    }
}

Add-ReportSection 'Recent service-control events' {
    Get-WinEvent -FilterHashtable @{
        LogName='System'; ProviderName='Service Control Manager'; StartTime=(Get-Date).AddHours(-$EventHours)
    } -ErrorAction Continue | Where-Object { $_.Message -match 'almagestd|Almagest' } |
        Select-Object TimeCreated,Id,LevelDisplayName,ProviderName,Message | Format-List
}

Add-ReportSection 'Recent application events' {
    Get-WinEvent -FilterHashtable @{
        LogName='Application'; StartTime=(Get-Date).AddHours(-$EventHours)
    } -ErrorAction Continue | Where-Object {
        $_.Message -match 'almagestd\.exe|almagest-app\.exe|Almagest'
    } | Select-Object TimeCreated,Id,LevelDisplayName,ProviderName,Message | Format-List
}

Add-ReportSection 'Windows Error Reporting and reliability records' {
    Get-CimInstance Win32_ReliabilityRecords -ErrorAction SilentlyContinue |
        Where-Object {
            $_.TimeGenerated -ge (Get-Date).AddHours(-$EventHours) -and
            ($_.ProductName -match 'Almagest|almagest' -or $_.Message -match 'Almagest|almagest')
        } | Sort-Object TimeGenerated | Format-List *
    Get-ChildItem -LiteralPath 'Registry::HKEY_LOCAL_MACHINE\Software\Microsoft\Windows\Windows Error Reporting\LocalDumps' `
        -ErrorAction SilentlyContinue | Format-List *
    Get-ChildItem -LiteralPath (Join-Path $env:ProgramData 'Microsoft\Windows\WER') -Recurse -File `
        -ErrorAction SilentlyContinue | Where-Object { $_.FullName -match 'almagest' } |
        Select-Object FullName,Length,CreationTimeUtc,LastWriteTimeUtc | Format-List
}

Add-ReportSection 'Relevant Defender events' {
    Get-WinEvent -FilterHashtable @{
        LogName='Microsoft-Windows-Windows Defender/Operational'; StartTime=(Get-Date).AddHours(-$EventHours)
    } -ErrorAction Continue | Where-Object { $_.Message -match 'Almagest|almagest' } |
        Select-Object TimeCreated,Id,LevelDisplayName,ProviderName,Message | Format-List
}

$daemonLogRoot = Join-Path $env:ProgramData 'Almagest\logs'
Get-ChildItem -LiteralPath $daemonLogRoot -Filter 'almagestd.log*' -File -ErrorAction SilentlyContinue |
    Copy-Item -Destination $daemonLogsRoot -Force -ErrorAction Continue

$originalProfile = Get-ProfilePathForSid $OriginalSid
if ($originalProfile) {
    $appLogRoot = Join-Path $originalProfile 'AppData\Local\Almagest\logs'
    Get-ChildItem -LiteralPath $appLogRoot -Filter 'almagest-app.log*' -File -ErrorAction SilentlyContinue |
        Copy-Item -Destination $appLogsRoot -Force -ErrorAction Continue
}

$installerLogDirectories = @($env:TEMP, (Join-Path $env:windir 'Temp'))
if ($originalProfile) {
    $installerLogDirectories += Join-Path $originalProfile 'AppData\Local\Temp'
}
$minimumInstallerLogTime = (Get-Date).ToUniversalTime().AddHours(-$EventHours)
$installerLogDirectories | Where-Object { $_ } | Select-Object -Unique | ForEach-Object {
    Get-ChildItem -LiteralPath $_ -Filter 'MSI*.LOG' -File -ErrorAction SilentlyContinue |
        Where-Object {
            $_.LastWriteTimeUtc -ge $minimumInstallerLogTime -and
            (Select-String -LiteralPath $_.FullName -Pattern 'Almagest' -SimpleMatch -Quiet -ErrorAction SilentlyContinue)
        } | ForEach-Object {
            $destinationName = '{0:yyyyMMddTHHmmssZ}-{1}' -f $_.LastWriteTimeUtc,$_.Name
            Copy-Item -LiteralPath $_.FullName -Destination (Join-Path $installerLogsRoot $destinationName) `
                -Force -ErrorAction Continue
        }
}

$eventMilliseconds = [int64]$EventHours * 60 * 60 * 1000
$eventQuery = "*[System[TimeCreated[timediff(@SystemTime) <= $eventMilliseconds]]]"
foreach ($eventLog in @(
    @{ Name='System'; File='system.evtx' },
    @{ Name='Application'; File='application.evtx' },
    @{ Name='Microsoft-Windows-Windows Defender/Operational'; File='defender.evtx' }
)) {
    & wevtutil.exe epl $eventLog.Name (Join-Path $eventsRoot $eventLog.File) "/q:$eventQuery" /ow:true `
        2>> $errorsPath
}

Add-ReportSection 'Collected artifact inventory' {
    Get-ChildItem -LiteralPath $stagingRoot -Recurse -File |
        Sort-Object FullName | Select-Object FullName,Length,CreationTimeUtc,LastWriteTimeUtc |
        Format-Table -AutoSize
}

Add-ReportLine
Add-ReportLine "Capture completed UTC: $((Get-Date).ToUniversalTime().ToString('o'))"

$outputDirectory = Split-Path -Parent $OutputPath
New-Item -ItemType Directory -Force $outputDirectory | Out-Null
try {
    Compress-Archive -Path (Join-Path $stagingRoot '*') -DestinationPath $OutputPath -CompressionLevel Optimal
} catch {
    throw "Could not create support archive $OutputPath`: $($_.Exception.Message). Staging data remains at $stagingRoot"
}

Remove-Item -LiteralPath $stagingRoot -Recurse -Force -ErrorAction SilentlyContinue
Write-Output "Almagest support report: $OutputPath"
