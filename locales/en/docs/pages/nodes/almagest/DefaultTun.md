DefaultTun captures traffic from the host system and provides it through the
**Tun** port. Connect this port to the start of a TUN processing path, such as a
tunnel, splitter, firewall, or spoofer. The node also installs the operating
system routes and DNS settings that send traffic to this TUN.

Only one DefaultTun node can exist in a workspace. The node is available only
when the active platform backend supports a system default TUN.

## TUN configuration

In most configurations, leave **Name**, **TUN addrs**, **TUN routes**, **MTU**,
and **DNS IP** empty. The platform then selects a TUN name, private address, DNS
address, routes, and MTU.

Use these fields when the connected TUN path requires fixed values:

- **TUN addrs** assigns IPv4 or IPv6 prefixes to the system TUN.
- **TUN routes** adds destination prefixes to its routing policy. The selected
  address and route families determine whether the node captures IPv4, IPv6, or
  both.
- **MTU** sets an upper limit. A value of `0` uses the platform default.
- **DNS IP** selects the local TUN address on which DefaultTun provides its DNS
  proxy. This value is not an upstream DNS server address.

Nodes connected to **Tun** can also suggest addresses, routes, source routes,
and an MTU. DefaultTun combines their addresses and routes with the menu values.
If there is more than one positive MTU, it uses the smallest value. The
**Effective** section shows the configuration that the node applied.

DefaultTun does not use a DNS server suggested through the **Tun** connection.
Connect a DNS resolver to the separate **DNS** port. System DNS requests then go
to that resolver through the local proxy. If no ready resolver is connected, the
proxy drops DNS requests. It does not fall back to the host resolver.

## Process routing

Leave **Exclude** and **Include** empty to send all captured host traffic
through the TUN path.

Use **Exclude** to let matching processes bypass the TUN in normal, non-strict
mode. Use **Include** to send only matching processes through the TUN. All rules
in a list are alternatives: a process matches the list when it matches one rule.
**Exclude** and **Include** cannot be active at the same time.

**Strict routing** prevents traffic from falling back to a direct route. In
strict include mode, nonmatching traffic is blocked. In strict exclude mode,
matching traffic is blocked instead of bypassing the TUN. Use strict routing
when traffic must stop if the TUN path cannot carry it.

The menu offers only the rule types that the active platform supports. Select a
rule type first, and then use autocomplete where it is available.

### Linux

Linux implements process routing with eBPF process tracking and packet marks. It
supports **Exclude**, **Include**, and **Strict routing**. The supported rule
types are:

- `comm`: a regular expression for the process command name
- `exec`: an executable basename or absolute-path pattern
- `cmd`: a regular expression for the complete command line
- `pid`: one or more space-separated process IDs
- `user`: a user name
- `uid`: one or more space-separated user IDs
- `group`: a group name
- `gid`: one or more space-separated group IDs

Process routing requires the Linux eBPF and packet-mark features. If the host
does not provide them, the menu reports the rules as unavailable.

### Windows

Windows supports non-strict **Exclude** rules with the `win-exe-tree` type.
Enter an absolute executable path, such as
`C:\Program Files\Example\example.exe`. The rule excludes that executable and
its descendant processes. Windows does not support **Include** or **Strict
routing**.

Executable-tree exclusions use the pinned Mullvad split-tunnel driver and
Windows Filtering Platform rules. The driver is a global, exclusive resource. An
exclusion cannot start if the compatible driver is missing, another program owns
it, or its state requires recovery. DefaultTun reports the failure in the node
menu.
