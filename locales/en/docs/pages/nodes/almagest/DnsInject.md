DnsInject adds a selected DNS resolver to an existing Network. Connect the
connection path to the [Network](@port/right/Network) input, connect a resolver
to the [DNS](@port/right/DNS) input, and connect network consumers to the
[Network](@port/left/Network) output.

The resulting Network uses the connected DNS resolver instead of the resolver
from the original Network. This is useful when a client must connect through one
network but resolve names through another DNS path. For example, you can resolve
private names with [StaticDns](@docs/node/almagest/StaticDns) or
[MeshNames](@docs/node/almagest/MeshNames) before an HTTP client uses an
outgoing network.

DnsInject applies the resolver in two ways:

- Explicit network lookup operations use the connected DNS resolver. These
  operations support `A`, `AAAA`, `PTR`, `CNAME`, `NS`, `MX`, `SRV`, and `TXT`
  records.
- Connection and listen operations resolve a hostname before they use the
  original Network. The original Network receives a numeric IP address and port.
  For an IPv4-only or IPv6-only operation, DnsInject selects the first returned
  address of the requested family.

IP address literals do not cause a DNS request. A named service port, such as
`https`, is resolved from the built-in service database, not with a DNS query.
If name resolution fails or returns no address for the requested family, the
network operation fails before it reaches the original Network.

The DNS request follows the connected **DNS** path. DnsInject does not send that
request through the connected Network unless the DNS path itself is configured
to use that Network. This separation lets you select a bootstrap or out-of-band
resolver without creating a resolution loop.

DnsInject affects only consumers of its Network output. It does not change the
host operating system resolver, the original Network, or other users of that
Network. It also does not intercept DNS packets or rewrite hostnames inside
application data.

Names are queried as complete DNS names. DnsInject does not apply the host's
search domains or hosts file. The node does not add a DNS cache; caching depends
on the connected DNS path.

Both the Network and DNS connections must be ready before DnsInject provides its
Network output. The node label shows `waiting for network`, `waiting for DNS`,
or `injecting DNS`.
