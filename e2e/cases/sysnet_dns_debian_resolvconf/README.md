# sysnet-linux Debian resolvconf compatibility

This privileged case ports the Debian resolvconf Docker scenario from
`github.com/asciimoth/sysnet-linux` v0.2.19 to `almagestd`. The native system
integration auto-detects Debian resolvconf, a DefaultTun node installs its DNS
provider, and a normal system resolver query must reach the graph-connected
DnsClient and its deterministic upstream.
