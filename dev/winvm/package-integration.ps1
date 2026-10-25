param(
    [Parameter(Mandatory = $true)][string]$ArtifactDir,
    [Parameter(Mandatory = $true)][string]$SourceDir
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Assert-True([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw $Message }
}

function Invoke-PackageCli([string[]]$Arguments) {
    $output = & $script:Cli --url pipe://machine @Arguments 2>&1 | Out-String
    if ($LASTEXITCODE -ne 0) {
        throw "Packaged CLI failed ($LASTEXITCODE): $($Arguments -join ' ')`n$output"
    }
    $output
}

function Invoke-PackageJson([string[]]$Arguments) {
    Invoke-PackageCli $Arguments | ConvertFrom-Json
}

function Wait-Condition([scriptblock]$Condition, [string]$Description, [int]$Seconds = 30) {
    $deadline = [DateTime]::UtcNow.AddSeconds($Seconds)
    do {
        if (& $Condition) { return }
        Start-Sleep -Milliseconds 500
    } while ([DateTime]::UtcNow -lt $deadline)
    throw "Timed out waiting for $Description"
}

function Wait-ServiceReady([int]$DifferentProcessId = 0) {
    $service = $null
    Wait-Condition {
        $script:waitService = Get-CimInstance Win32_Service -Filter "Name='almagestd'" `
            -ErrorAction SilentlyContinue
        $script:waitService -and $script:waitService.State -eq 'Running' -and
            $script:waitService.ProcessId -ne 0 -and
            $script:waitService.ProcessId -ne $DifferentProcessId -and
            (Test-Path -LiteralPath '\\.\pipe\almagest-admin')
    } 'the packaged service' 120
    $service = $script:waitService
    $service
}

function Wait-GraphNode([string]$Name) {
    Wait-Condition {
        $output = & $script:Cli --url pipe://machine graph node --name $Name 2>$null
        $LASTEXITCODE -eq 0 -and $output
    } "graph node $Name"
}

function Get-MenuText([object]$Menu, [string]$Block, [string]$Item) {
    $entry = @($Menu.blocks | Where-Object id -eq $Block).items |
        Where-Object id -eq $Item | Select-Object -First 1
    if ($entry.PSObject.Properties.Name -contains 'value' -and $null -ne $entry.value) {
        return [string]$entry.value
    }
    if ($entry.PSObject.Properties.Name -contains 'text' -and $null -ne $entry.text) {
        return [string]$entry.text
    }
    ''
}

function Invoke-PackageConfig([string]$Path) {
    $snapshot = Invoke-PackageJson @('config', 'snapshot')
    Invoke-PackageJson @('config', 'apply', '--revision', [string]$snapshot.revision, $Path)
}

function Get-OwnedAdapter {
    @(Get-NetAdapter -IncludeHidden -ErrorAction SilentlyContinue |
        Where-Object Name -Like "$script:AdapterPrefix*")
}

function Save-Diagnostic([string]$Failure = '') {
    if ($Failure) {
        $Failure | Set-Content (Join-Path $ArtifactDir 'package-integration-failure.txt') -Encoding utf8
    }
    $logPath = Join-Path $script:ProgramData 'logs\almagestd.log'
    if (Test-Path -LiteralPath $logPath) {
        Copy-Item $logPath (Join-Path $ArtifactDir 'package-integration-daemon.log') -Force
    }
    Get-WinEvent -FilterHashtable @{
        LogName = 'System'
        ProviderName = 'Service Control Manager'
        StartTime = $script:StartedAt
    } -ErrorAction SilentlyContinue | Format-List TimeCreated,Id,LevelDisplayName,Message |
        Out-File (Join-Path $ArtifactDir 'package-integration-events.txt') -Encoding utf8 -Width 4096
}

New-Item -ItemType Directory -Force $ArtifactDir | Out-Null
$script:StartedAt = Get-Date
$script:ProgramFiles = Join-Path $env:ProgramFiles 'Almagest'
$script:ProgramData = Join-Path $env:ProgramData 'Almagest'
$script:Cli = Join-Path $script:ProgramFiles 'almagest-cli.exe'
$script:AdapterPrefix = 'almpkg'
$httpJob = $null
$failure = ''

try {
    Assert-True (Test-Path -LiteralPath $script:Cli -PathType Leaf) 'Installed CLI is absent'
    $serviceBeforeConfig = Wait-ServiceReady

    # Use a unique prefix so that all network resources created by this gate
    # can be identified without matching an unrelated Windows adapter.
    Stop-Service almagestd
    $protectedConfig = @'
{
  "System": {
    "Type": "native",
    "Windows": {
      "AdapterNamePrefix": "almpkg",
      "OperationTimeout": "15s",
      "Recovery": "inspect",
    },
  },
}
'@
    [IO.File]::WriteAllText((Join-Path $script:ProgramData 'protected_alm.hujson'),
        $protectedConfig, [Text.UTF8Encoding]::new($false))
    Start-Service almagestd
    $serviceAfterConfig = Wait-ServiceReady -DifferentProcessId $serviceBeforeConfig.ProcessId
    Assert-True (@(Get-OwnedAdapter).Count -eq 0) 'Owned Wintun state exists before the TUN test'

    # Probe one stable read operation on every public daemon API surface.
    $hello = Invoke-PackageJson @('admin', 'hello', '--client-name', 'package-integration')
    $surfaceNames = @('admin','config','dns','docs','engine','kv','log','secrets','snapshots','tls')
    foreach ($name in $surfaceNames) {
        Assert-True ($hello.surfaces.PSObject.Properties.Name -contains $name) `
            "The daemon did not advertise the $name API surface"
    }
    $null = Invoke-PackageJson @('admin', 'colormap')
    $null = Invoke-PackageJson @('config', 'snapshot')
    $null = Invoke-PackageJson @('dns', 'caches')
    $docs = Invoke-PackageJson @('docs', 'tree', '--locale', 'en')
    Assert-True (($docs | ConvertTo-Json -Depth 20) -match 'Welcome') `
        'Packaged localized documentation is unavailable through the API'
    $null = Invoke-PackageJson @('engine', 'workspace-snapshot')
    $classes = Invoke-PackageJson @('graph', 'classes')
    Assert-True (@($classes.classes).Count -gt 20) 'Packaged graph classes are incomplete'
    $null = Invoke-PackageJson @('kv', 'stores')
    $null = Invoke-PackageJson @('log', 'subscribe')
    $null = Invoke-PackageJson @('secrets', 'list')
    $examples = Invoke-PackageJson @('snapshots', 'list')
    Assert-True (@($examples.snapshots | Where-Object example).Count -gt 1) `
        'Packaged examples are unavailable through the API'
    $null = Invoke-PackageJson @('tls', 'list')
    $null = Invoke-PackageJson @('workers', 'snapshot')
    Assert-True (Test-Path -LiteralPath (Join-Path $script:ProgramFiles 'locales\en\docs\index.json')) `
        'Installed English documentation index is absent'
    Assert-True (Test-Path -LiteralPath (Join-Path $script:ProgramFiles 'examples\002-mock-network\manifest.json')) `
        'Installed example manifest is absent'

    # A local HTTP target proves that LocalProxy connects through OutNetwork.
    $httpJob = Start-Job -ScriptBlock {
        $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 18091)
        $listener.Start()
        try {
            while ($true) {
                $client = $listener.AcceptTcpClient()
                try {
                    $stream = $client.GetStream()
                    $reader = [IO.StreamReader]::new($stream, [Text.Encoding]::ASCII, $false, 1024, $true)
                    while ($reader.ReadLine()) { }
                    $body = [Text.Encoding]::UTF8.GetBytes('packaged OutNetwork response')
                    $header = [Text.Encoding]::ASCII.GetBytes(
                        "HTTP/1.1 200 OK`r`nContent-Length: $($body.Length)`r`nConnection: close`r`n`r`n")
                    $stream.Write($header, 0, $header.Length)
                    $stream.Write($body, 0, $body.Length)
                } catch {
                    # Readiness probes can close without sending a request.
                    $null = $_
                } finally {
                    $client.Dispose()
                }
            }
        } finally {
            $listener.Stop()
        }
    }
    Wait-Condition { (Test-NetConnection 127.0.0.1 -Port 18091 -WarningAction SilentlyContinue).TcpTestSucceeded } `
        'the local HTTP target'
    $configPath = Join-Path $SourceDir 'e2e\cases\local_proxy_out_http\alm.hujson'
    $configBefore = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $script:ProgramData 'alm.hujson')).Hash
    $null = Invoke-PackageConfig $configPath
    $configAfter = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $script:ProgramData 'alm.hujson')).Hash
    Assert-True ($configAfter -ne $configBefore) 'Config API did not replace the ProgramData config file'
    Assert-True ((Get-Content -Raw (Join-Path $script:ProgramData 'alm.hujson')) -match 'LocalProxy 1101') `
        'Config API did not persist the applied graph in ProgramData'
    Wait-GraphNode 'LocalProxy 1101'
    Wait-GraphNode 'OutNetwork 1103'
    $proxyOutput = Join-Path $ArtifactDir 'local-proxy-response.txt'
    Wait-Condition {
        & curl.exe --fail --silent --show-error --noproxy '*' --socks5-hostname 127.0.0.1:1081 `
            --connect-timeout 2 --max-time 5 http://127.0.0.1:18091/package-proxy `
            --output $proxyOutput 2>$null
        $LASTEXITCODE -eq 0 -and (Test-Path $proxyOutput) -and
            (Get-Content -Raw $proxyOutput) -eq 'packaged OutNetwork response'
    } 'LocalProxy and OutNetwork request'

    Invoke-PackageCli @('kv', 'set', '--key', 'package-integration', '--data', 'retained-kv') | Out-Null
    Invoke-PackageCli @('secrets', 'set', '--key', 'package-integration', '--value', 'retained-secret') | Out-Null
    $firstPid = (Get-CimInstance Win32_Service -Filter "Name='almagestd'").ProcessId
    Restart-Service almagestd
    $null = Wait-ServiceReady -DifferentProcessId $firstPid
    Wait-GraphNode 'LocalProxy 1101'
    $kv = Invoke-PackageJson @('kv', 'get', '--key', 'package-integration')
    Assert-True ($kv.found -and $kv.value.data -eq 'retained-kv') 'KV state did not survive SCM restart'
    $secrets = Invoke-PackageJson @('secrets', 'list')
    Assert-True (@($secrets.secrets | Where-Object {
        $_.key -eq 'package-integration' -and $_.value -eq 'retained-secret'
    }).Count -eq 1) 'Secret state did not survive SCM restart'

    $loaded = Invoke-PackageJson @('snapshots', 'load', '002-mock-network')
    Assert-True ($loaded.snapshot.example -eq '002-mock-network') 'Packaged example metadata differs'
    foreach ($node in @('Mock Network','Http Client','Http Server','Response','Out Network')) {
        Wait-GraphNode $node
    }
    Invoke-PackageCli @('graph', 'click-menu-button', '--name', 'Http Client', '--block', 'request', '--button', 'send') | Out-Null
    Wait-Condition {
        $menu = Invoke-PackageJson @('graph', 'menu', '--name', 'Http Client')
        (Get-MenuText $menu 'result' 'status') -eq 'Status: 200 OK' -and
            (Get-MenuText $menu 'result' 'response') -eq 'HTTP server response'
    } 'the packaged example response'
    $examplePid = (Get-CimInstance Win32_Service -Filter "Name='almagestd'").ProcessId
    Restart-Service almagestd
    $null = Wait-ServiceReady -DifferentProcessId $examplePid
    Wait-GraphNode 'Mock Network'
    Wait-GraphNode 'Http Client'

    # A regular TUN with private test prefixes is the safe Windows-native path.
    # It creates the adapter and one non-default route without changing DNS or
    # the VM default route.
    $tunConfig = Join-Path $ArtifactDir 'package-integration-tun.hujson'
    $tunConfigContent = @'
{
  "nodes": {
    "Package Native Tun": {
      "Class": "almagest/Tun",
      "tunAddrs": "198.18.239.1/32",
      "tunRoutes": "198.19.239.0/24",
      "mtu": 1380,
    },
  },
}
'@
    [IO.File]::WriteAllText($tunConfig, $tunConfigContent, [Text.UTF8Encoding]::new($false))
    $null = Invoke-PackageConfig $tunConfig
    Wait-GraphNode 'Package Native Tun'
    Wait-Condition { @(Get-OwnedAdapter).Count -eq 1 } 'the packaged Wintun adapter'
    $adapter = @(Get-OwnedAdapter)[0]
    $addresses = @(Get-NetIPAddress -InterfaceIndex $adapter.ifIndex -ErrorAction SilentlyContinue)
    $routes = @(Get-NetRoute -InterfaceIndex $adapter.ifIndex -ErrorAction SilentlyContinue)
    Assert-True (@($addresses | Where-Object IPAddress -eq '198.18.239.1').Count -eq 1) `
        'The packaged Wintun adapter address is absent'
    Assert-True (@($routes | Where-Object DestinationPrefix -eq '198.19.239.0/24').Count -eq 1) `
        'The packaged Wintun adapter route is absent'
    Assert-True (@($routes | Where-Object DestinationPrefix -in @('0.0.0.0/0','::/0')).Count -eq 0) `
        'The safe TUN test created a default route'
    $networkAdapter = Get-CimInstance Win32_NetworkAdapter |
        Where-Object InterfaceIndex -eq $adapter.ifIndex | Select-Object -First 1
    Assert-True ([bool]$networkAdapter.PNPDeviceID) 'Cannot resolve the Wintun PnP device'
    $signedDriver = Get-CimInstance Win32_PnPSignedDriver |
        Where-Object DeviceID -eq $networkAdapter.PNPDeviceID | Select-Object -First 1
    Assert-True ($signedDriver.IsSigned) 'Windows did not load a signed Wintun adapter driver'
    Assert-True ($signedDriver.DriverProviderName -match 'WireGuard') `
        "Unexpected Wintun driver provider: $($signedDriver.DriverProviderName)"
    $adapterRecord = [ordered]@{
        name = $adapter.Name
        interfaceGuid = $adapter.InterfaceGuid.ToString().ToLowerInvariant()
        interfaceIndex = [uint32]$adapter.ifIndex
        pnpDeviceId = $networkAdapter.PNPDeviceID
        driverProvider = $signedDriver.DriverProviderName
        driverVersion = $signedDriver.DriverVersion
        signer = $signedDriver.Signer
        signed = [bool]$signedDriver.IsSigned
        address = '198.18.239.1/32'
        route = '198.19.239.0/24'
    }

    Stop-Service almagestd
    Wait-Condition { @(Get-OwnedAdapter).Count -eq 0 } 'normal service-stop Wintun cleanup' 60
    Assert-True (@(Get-NetIPAddress -InterfaceIndex $adapterRecord.interfaceIndex -ErrorAction SilentlyContinue).Count -eq 0) `
        'Owned adapter addresses remain after normal service stop'
    Assert-True (@(Get-NetRoute -InterfaceIndex $adapterRecord.interfaceIndex -ErrorAction SilentlyContinue).Count -eq 0) `
        'Owned adapter routes remain after normal service stop'

    $evidence = [ordered]@{
        schemaVersion = 1
        suite = 'windows-msi-package-integration'
        outcome = 'passed'
        installedCli = $script:Cli
        installedService = 'almagestd'
        apiSurfaces = $surfaceNames
        localizedDocumentation = $true
        packagedExamples = $true
        configAppliedToProgramData = $true
        localProxyOutNetwork = $true
        scmRestart = [ordered]@{ kvRetained = $true; secretRetained = $true; graphRetained = $true }
        example = [ordered]@{ id = '002-mock-network'; ran = $true; retainedAfterRestart = $true }
        nativeTun = [ordered]@{ safePrivateRoute = $true; adapter = $adapterRecord }
        normalServiceStop = [ordered]@{ serviceStopped = $true; adapterRemoved = $true; networkConfigurationRemoved = $true }
        serviceProcessIds = [ordered]@{
            afterProtectedConfig = $serviceAfterConfig.ProcessId
            beforeStorageRestart = $firstPid
            beforeExampleRestart = $examplePid
        }
    }
    $evidence | ConvertTo-Json -Depth 10 | Set-Content `
        (Join-Path $ArtifactDir 'package-integration.json') -Encoding utf8
} catch {
    $failure = ($_ | Out-String)
    throw
} finally {
    if ($httpJob) {
        Stop-Job $httpJob -ErrorAction SilentlyContinue
        Remove-Job $httpJob -Force -ErrorAction SilentlyContinue
    }
    if ($failure) {
        Stop-Service almagestd -ErrorAction SilentlyContinue
    }
    Save-Diagnostic $failure
}
