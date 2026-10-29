$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Assert-True([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw $Message }
}

function Invoke-PackageCli([string]$Cli, [string[]]$Arguments) {
    $output = & $Cli --url pipe://machine @Arguments 2>&1 | Out-String
    if ($LASTEXITCODE -ne 0) {
        throw "Packaged CLI failed ($LASTEXITCODE): $($Arguments -join ' ')`n$output"
    }
    $output
}

function Wait-AlmagestService([int]$TimeoutSeconds = 120, [int]$DifferentProcessId = 0) {
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    do {
        $service = Get-CimInstance Win32_Service -Filter "Name='almagestd'" -ErrorAction SilentlyContinue
        if ($service -and $service.State -eq 'Running' -and
                $service.ProcessId -ne 0 -and $service.ProcessId -ne $DifferentProcessId -and
                (Test-Path -LiteralPath '\\.\pipe\almagest-admin')) {
            return $service
        }
        Start-Sleep -Seconds 1
    } while ([DateTime]::UtcNow -lt $deadline)
    throw 'almagestd did not become ready before the lifecycle timeout'
}

function Assert-LifecycleState([string]$Cli, [string]$ProgramData) {
    $kv = Invoke-PackageCli $Cli @('kv', 'get', '--key', 'package-lifecycle') | ConvertFrom-Json
    Assert-True ($kv.found -and $kv.value.data -eq 'retained-kv-value') 'KV state was not retained'

    $secrets = Invoke-PackageCli $Cli @('secrets', 'list') | ConvertFrom-Json
    $secret = @($secrets.secrets | Where-Object {
        $_.key -eq 'package-lifecycle' -and $_.value -eq 'retained-secret-value'
    })
    Assert-True ($secret.Count -eq 1) 'Secret state was not retained'

    $snapshots = Invoke-PackageCli $Cli @('snapshots', 'list') | ConvertFrom-Json
    $snapshot = @($snapshots.snapshots | Where-Object name -eq 'Package lifecycle retained snapshot')
    Assert-True ($snapshot.Count -eq 1) 'Snapshot state was not retained'

    Invoke-PackageCli $Cli @('graph', 'node', '--name', 'Package lifecycle retained node') | Out-Null
    Assert-True ((Get-Content -Raw (Join-Path $ProgramData 'alm.hujson')) -match
        'package-lifecycle-regular') 'Regular configuration edit was not retained'
    Assert-True ((Get-Content -Raw (Join-Path $ProgramData 'protected_alm.hujson')) -match
        'package-lifecycle-protected') 'Protected configuration edit was not retained'
}

function Invoke-MSI([string[]]$Arguments, [string]$Description) {
    $process = Start-Process msiexec.exe -ArgumentList $Arguments -Wait -PassThru
    Assert-True ($process.ExitCode -in @(0, 3010)) "$Description failed: $($process.ExitCode)"
    $process.ExitCode
}

function Get-FileSHA256([string]$Path) {
    (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash.ToLowerInvariant()
}

function Assert-PrivateProgramDataAcl([string]$ProgramData) {
    $trusted = @('S-1-5-18', 'S-1-5-32-544')
    foreach ($path in @(
        $ProgramData,
        (Join-Path $ProgramData 'alm.hujson'),
        (Join-Path $ProgramData 'protected_alm.hujson'),
        (Join-Path $ProgramData 'store'),
        (Join-Path $ProgramData 'logs')
    )) {
        $acl = Get-Acl -LiteralPath $path
        $owner = New-Object Security.Principal.NTAccount($acl.Owner)
        $ownerSid = $owner.Translate([Security.Principal.SecurityIdentifier]).Value
        Assert-True ($ownerSid -eq 'S-1-5-18') "ProgramData owner was not reconciled: $path ($ownerSid)"
        foreach ($entry in $acl.Access) {
            $sid = $entry.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value
            Assert-True ($sid -in $trusted) "ProgramData ACL contains an untrusted SID: $path ($sid)"
        }
    }
}
