Provides the daemon's outbound DNS service as a DNS output. Use OutDns as the
normal upstream for [DnsCache](@docs/node/almagest/DnsCache), [DnsRouter](@docs/node/almagest/DnsRouter), [DnsFallback](@docs/node/almagest/DnsFallback), or [DefaultTun](@docs/node/almagest/DefaultTun).

OutDns is a source for the graph. It has no DNS input and no node settings.
Connecting it does not change the host DNS configuration. A node such as
 [DefaultTun](@docs/node/almagest/DefaultTun) must use the output before graph DNS can handle system requests.
OutDns also does not add hosts-file records or other Network name-resolution
rules.

On Linux, OutDns uses the outbound DNS provider selected when the daemon
starts. It keeps the upstream resolver that was available before [DefaultTun](@docs/node/almagest/DefaultTun)
changes the host DNS configuration. The systemd-resolved integration can
follow upstream changes. Direct and resolvconf integrations keep the upstream
servers that they read at startup.

On Windows, OutDns uses the numeric DNS servers from the selected physical
network. It sends UDP and TCP requests through sockets bound to that network.
The output is unavailable when there is no selected network or no usable DNS
server.
