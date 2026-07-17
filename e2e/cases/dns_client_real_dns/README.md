# DNS Client Real DNS

This case verifies `almagest/DnsClient` against real DNS server
processes inside the e2e container.

The case starts:

- `dnsmasq` on `127.0.0.1:5353` for ordinary UDP/TCP DNS.
- `socat` on `127.0.0.1:5853` as a TLS listener forwarding DoT traffic to
  `dnsmasq`'s TCP DNS socket.

The daemon graph contains two `DnsClient` nodes:

- `DNS UDP Client` queries `udp://127.0.0.1:5353`.
- `DNS DoT Client` queries `dot://dot-upstream.e2e.test:5853`, using a
  `StaticDns` bootstrap node to resolve the DoT server hostname.

The test drives the existing interactive `almagest/DnsQuery` query nodes
through the admin API and asserts successful DNS answers from both upstream
transports.
