Provides the daemon's direct operating-system network as a Network output. Use
OutNetwork as the final network for clients, proxies, and tunnel transports.
Network servers can also use it to open TCP or UDP listeners.

Connections created through OutNetwork bypass DefaultTun and use the original
outbound path. This behavior prevents a loop when a DefaultTun graph ends at
OutNetwork. It also applies to a client that connects directly to OutNetwork:
that client's traffic does not pass through the DefaultTun graph.

OutNetwork resolves hostnames with the daemon's outbound resolver. It does
not use DNS paths that you build from DNS nodes. Use DnsInject after
OutNetwork when a Network consumer must use a DNS service from the graph.

On Linux, each new socket uses the current main routing table with the
DefaultTun bypass. New connections follow physical-network and route changes
without a daemon restart. Existing connections are not moved to the new
network and can stop when their interface or source address disappears.
Linux name resolution checks the hosts file before it uses the original
outbound DNS service. It does not fall back to DNS installed by DefaultTun.

On Windows, each socket is bound to the selected physical network. IPv4 or
IPv6 operations are available only when a network for that address family is
selected. Operations fail after the selected network is lost; there is no
unrestricted fallback. TCP and UDP unicast are supported. Multicast and raw
sockets are not supported.
