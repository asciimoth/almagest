[CmdletBinding()]
param([Parameter(Mandatory)][ValidateSet('Client','Endpoint')][string]$Role)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Get-FlowAdapter([string]$MacAddress) {
    $adapter = @(Get-NetAdapter | Where-Object MacAddress -eq $MacAddress)
    if ($adapter.Count -ne 1) { throw "Expected one adapter with MAC address $MacAddress" }
    return $adapter[0]
}

function Reset-FlowAdapter {
    [CmdletBinding(SupportsShouldProcess)]
    param($Adapter, [bool]$WeakHost)
    if (-not $PSCmdlet.ShouldProcess($Adapter.Name, 'Reset flow adapter')) { return }
    $state = if ($WeakHost) { 'Enabled' } else { 'Disabled' }
    foreach ($family in 'IPv4','IPv6') {
        Set-NetIPInterface -InterfaceIndex $Adapter.ifIndex -AddressFamily $family `
            -Dhcp Disabled -AutomaticMetric Disabled -InterfaceMetric 5 `
            -WeakHostReceive $state -WeakHostSend $state
    }
    Get-NetIPAddress -InterfaceIndex $Adapter.ifIndex -ErrorAction SilentlyContinue |
        Where-Object PrefixOrigin -ne 'WellKnown' |
        Remove-NetIPAddress -Confirm:$false -ErrorAction SilentlyContinue
    Get-NetRoute -InterfaceIndex $Adapter.ifIndex -ErrorAction SilentlyContinue |
        Where-Object DestinationPrefix -ne 'ff00::/8' |
        Remove-NetRoute -Confirm:$false -ErrorAction SilentlyContinue
}

$suffix = if ($Role -eq 'Client') { @('10','11') } else { @('20','21') }
$tunnel = Get-FlowAdapter "52-54-00-12-34-$($suffix[0])"
$underlay = Get-FlowAdapter "52-54-00-12-34-$($suffix[1])"
Reset-FlowAdapter $tunnel ($Role -eq 'Endpoint')
Reset-FlowAdapter $underlay ($Role -eq 'Endpoint')

$last = if ($Role -eq 'Client') { '2' } else { '1' }
New-NetIPAddress -InterfaceIndex $tunnel.ifIndex -IPAddress "198.18.0.$last" -PrefixLength 24 | Out-Null
New-NetIPAddress -InterfaceIndex $tunnel.ifIndex -IPAddress "fd00:18:0::$last" -PrefixLength 64 | Out-Null
New-NetIPAddress -InterfaceIndex $underlay.ifIndex -IPAddress "198.18.1.$last" -PrefixLength 24 | Out-Null
New-NetIPAddress -InterfaceIndex $underlay.ifIndex -IPAddress "fd00:18:1::$last" -PrefixLength 64 | Out-Null

if ($Role -eq 'Client') {
    New-NetRoute -InterfaceIndex $tunnel.ifIndex -DestinationPrefix '203.0.113.1/32' `
        -NextHop '198.18.0.1' -RouteMetric 5 | Out-Null
    New-NetRoute -InterfaceIndex $underlay.ifIndex -DestinationPrefix '203.0.113.1/32' `
        -NextHop '198.18.1.1' -RouteMetric 50 | Out-Null
    New-NetRoute -InterfaceIndex $tunnel.ifIndex -DestinationPrefix '2001:db8:ffff::1/128' `
        -NextHop 'fd00:18:0::1' -RouteMetric 5 | Out-Null
    New-NetRoute -InterfaceIndex $underlay.ifIndex -DestinationPrefix '2001:db8:ffff::1/128' `
        -NextHop 'fd00:18:1::1' -RouteMetric 50 | Out-Null
    Set-DnsClientServerAddress -InterfaceIndex $underlay.ifIndex `
        -ServerAddresses @('203.0.113.1', '2001:db8:ffff::1')
} else {
    New-NetIPAddress -InterfaceIndex $tunnel.ifIndex -IPAddress '203.0.113.1' `
        -PrefixLength 32 -SkipAsSource $true | Out-Null
    New-NetIPAddress -InterfaceIndex $tunnel.ifIndex -IPAddress '2001:db8:ffff::1' `
        -PrefixLength 128 -SkipAsSource $true | Out-Null
}

@{role=$Role; tunnel=@{name=$tunnel.Name; index=$tunnel.ifIndex};
  underlay=@{name=$underlay.Name; index=$underlay.ifIndex};
  addresses=@(Get-NetIPAddress -InterfaceIndex $tunnel.ifIndex,$underlay.ifIndex |
      Select-Object InterfaceIndex,AddressFamily,IPAddress,PrefixLength,SkipAsSource);
  routes=@(Get-NetRoute -InterfaceIndex $tunnel.ifIndex,$underlay.ifIndex |
      Select-Object InterfaceIndex,AddressFamily,DestinationPrefix,NextHop,RouteMetric)} |
    ConvertTo-Json -Depth 6
