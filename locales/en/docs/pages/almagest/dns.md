DNS translates names to records such as IP addresses and service data. In
Almagest, a DNS link carries a question-and-answer service. It does not
automatically change host DNS settings.

## Build a resolver path

Common DNS nodes have separate roles:

- [StaticDns](@docs/node/almagest/StaticDns) answers from configured records.
- [DnsClient](@docs/node/almagest/DnsClient) sends questions to an upstream DNS
  server through a Network.
- [OutDns](@docs/node/almagest/OutDns) uses the service's outbound resolver.
- [DnsRouter](@docs/node/almagest/DnsRouter) selects a DNS path by question.
- [DnsFallback](@docs/node/almagest/DnsFallback) tries another resolver after a
  configured failure.
- [DnsCache](@docs/node/almagest/DnsCache) stores and reuses answers.
- [DnsServer](@docs/node/almagest/DnsServer) serves a DNS link through a
  Network.

Connect a DNS service to the separate DNS port of
[DefaultTun](@docs/node/almagest/DefaultTun) when host applications must use it.
If that resolver is not ready, DefaultTun drops DNS requests. It does not
silently use the original host resolver.

This graph uses a static resolver first. If the static resolver cannot answer,
DnsFallback uses the service's outbound resolver. DnsCache stores answers from
both paths.

```graph
{
  "DNS Query": {
    "Class": "almagest/DnsQuery",
    "Pos": "{\"x\":31,\"y\":120}",
    "Links": ["DNS -> [DNS Cache] DNS"],
    "name": "service.example.",
  },
  "DNS Cache": {
    "Class": "almagest/DnsCache",
    "Pos": "{\"x\":199,\"y\":120}",
    "Links": ["DNS -> [DNS Fallback] DNS"],
  },
  "DNS Fallback": {
    "Class": "almagest/DnsFallback",
    "Pos": "{\"x\":368,\"y\":120}",
    "Links": [
      "slot 1 -> [Static DNS] DNS",
      "slot 2 -> [Outbound DNS] DNS",
    ],
  },
  "Static DNS": {
    "Class": "almagest/StaticDns",
    "Pos": "{\"x\":583,\"y\":67}",
    "records": "service.example A 192.0.2.10",
  },
  "Outbound DNS": {
    "Class": "almagest/OutDns",
    "Pos": "{\"x\":583,\"y\":173}",
  },
}
```

## Network DNS and graph DNS

A Network can have its own lookup behavior. For example,
[OutNetwork](@docs/node/almagest/OutNetwork) uses the service's outbound
resolver, while a [TorDriver](@docs/node/almagest/TorDriver) Network resolves
through Tor. A DNS link is an explicit resolver service that can be routed and
cached independently.

[DnsInject](@docs/node/almagest/DnsInject) gives a Network an explicit DNS
service. Use it when clients on that Network must use a graph resolver instead
of the Network provider's normal resolver.

```graph
{
  "HTTP Client": {
    "Class": "almagest/HttpClient",
    "Pos": "{\"x\":31,\"y\":107}",
    "Links": ["Network -> [Network with DNS] Network"],
  },
  "Network with DNS": {
    "Class": "almagest/DnsInject",
    "Pos": "{\"x\":215,\"y\":107}",
    "Links": [
      "Network -> [Outside Network] Network",
      "DNS -> [DNS Cache] DNS",
    ],
  },
  "Outside Network": {
    "Class": "almagest/OutNetwork",
    "Pos": "{\"x\":445,\"y\":67}",
  },
  "DNS Cache": {
    "Class": "almagest/DnsCache",
    "Pos": "{\"x\":445,\"y\":160}",
    "Links": ["DNS -> [Outbound DNS] DNS"],
  },
  "Outbound DNS": {
    "Class": "almagest/OutDns",
    "Pos": "{\"x\":629,\"y\":160}",
  },
}
```

[Dns2Network](@docs/node/almagest/Dns2Network) and
[Tun2Dns](@docs/node/almagest/Tun2Dns) adapt DNS to other traffic paths. Read
their node pages before use. Each adapter has different transport and address
behavior. Read [Network, TUN, DNS, and Object](@docs/almagest/types) for the
differences between the three link types.

## Intercept DNS packets

Some applications send DNS packets to a configured or built-in resolver address.
To apply one graph DNS policy to this traffic, intercept UDP packets with
destination port `53` before the normal traffic path. On a TUN link, use
[TunSplitter](@docs/node/almagest/TunSplitter) to select these packets. Send the
selected packets to Tun2Dns and send the other packets to their normal path.

Tun2Dns answers as if the reply came from the address that the application
selected. This pattern can intercept requests to different DNS server addresses.
It does not require a dedicated virtual DNS address.

```graph
{
  "Host TUN": {
    "Class": "almagest/Tun",
    "Pos": "{\"x\":31,\"y\":147}",
    "Links": ["Tun -> [Packet Router] Tun"],
  },
  "Packet Router": {
    "Class": "almagest/TunSplitter",
    "Pos": "{\"x\":199,\"y\":147}",
    "Links": [
      "slot 1 -> [DNS Packet Adapter] Tun",
      "slot 2 -> [Flow Converter] Tun",
    ],
    "rules": "UDP\nPORT 53\nAND\nSLOT 1\n\nTRUE\nSLOT 2",
  },
  "DNS Packet Adapter": {
    "Class": "almagest/Tun2Dns",
    "Pos": "{\"x\":399,\"y\":53}",
    "Links": ["DNS -> [DNS Cache] DNS"],
  },
  "DNS Cache": {
    "Class": "almagest/DnsCache",
    "Pos": "{\"x\":583,\"y\":53}",
    "Links": ["DNS -> [Outbound DNS] DNS"],
  },
  "Outbound DNS": {
    "Class": "almagest/OutDns",
    "Pos": "{\"x\":767,\"y\":53}",
  },
  "Flow Converter": {
    "Class": "almagest/Spoofer",
    "Pos": "{\"x\":399,\"y\":233}",
    "Links": ["Network -> [Outside Network] Network"],
  },
  "Outside Network": {
    "Class": "almagest/OutNetwork",
    "Pos": "{\"x\":598,\"y\":233}",
  },
}
```

For a Network link, use [DnsServer](@docs/node/almagest/DnsServer) to make a
graph DNS service available as a UDP endpoint on that Network.
[NetworkRouter](@docs/node/almagest/NetworkRouter) and
[Remapper](@docs/node/almagest/Remapper) can select UDP port `53` operations and
redirect them to that endpoint. This changes Network operations. It does not
catch raw packets that bypass the Network path.

In this graph, DNS Client tries to contact `203.0.113.53:53`. Network Router
selects all UDP port `53` operations. DNS Redirect changes their destination to
the DNS Server endpoint. The server then sends each question to Graph Resolver.
The same router can serve other Network consumers and send non-DNS operations to
Outside Network.

```graph
{
  "DNS Query": {
    "Class": "almagest/DnsQuery",
    "Pos": "{\"x\":15,\"y\":80}",
    "Links": ["DNS -> [DNS Client] DNS"],
    "name": "service.example.",
  },
  "DNS Client": {
    "Class": "almagest/DnsClient",
    "Pos": "{\"x\":169,\"y\":80}",
    "Links": ["Network -> [Network Router] Network"],
    "servers": ["udp://203.0.113.53:53"],
  },
  "Network Router": {
    "Class": "almagest/NetworkRouter",
    "Pos": "{\"x\":353,\"y\":120}",
    "Links": [
      "slot 1 -> [DNS Redirect] Network",
      "slot 2 -> [Outside Network] Network",
    ],
    "rules": "DIAL\nUDP\nAND\nPORT 53\nAND\nSLOT 1\n\nTRUE\nSLOT 2",
  },
  "DNS Redirect": {
    "Class": "almagest/Remapper",
    "Pos": "{\"x\":552,\"y\":53}",
    "Links": ["Network -> [DNS Transport] Network"],
    "rules": "DIAL\nUDP\nAND\nPORT 53\nAND\nREMAP DST ADDR_PORT 127.0.0.1:5353",
  },
  "DNS Transport": {
    "Class": "almagest/MockNetwork",
    "Pos": "{\"x\":751,\"y\":53}",
  },
  "DNS Server": {
    "Class": "almagest/DnsServer",
    "Pos": "{\"x\":552,\"y\":-53}",
    "Links": [
      "Network -> [DNS Transport] Network",
      "DNS -> [Graph Resolver] DNS",
    ],
  },
  "Graph Resolver": {
    "Class": "almagest/StaticDns",
    "Pos": "{\"x\":751,\"y\":-67}",
    "records": "service.example A 192.0.2.10",
  },
  "Outside Network": {
    "Class": "almagest/OutNetwork",
    "Pos": "{\"x\":552,\"y\":200}",
  },
}
```

Port `53` interception covers plain DNS over UDP. It does not intercept DNS over
TCP, DNS over TLS, DNS over HTTPS, or DNS over QUIC. Route or filter those
protocols separately when the policy must include them.

## Caches

A DnsCache uses a named persistent DNS cache store. Cached records keep their
expiration time. Expired data is normally removed and no longer answers new
questions. A service option can disable cache expiration for diagnostics, but
stale DNS data can then remain usable.

The same stored cache can support name-based routing and inspection.
[NetworkRouter](@docs/node/almagest/NetworkRouter),
[TunSplitter](@docs/node/almagest/TunSplitter), and
[Sniffer](@docs/node/almagest/Sniffer) can compare an IP address with a cached
reverse mapping. They do not send a live reverse-DNS question for each operation
or packet. Populate the selected store with DnsCache first.

Cached names are observations, not verified process identity. Multiple resources
with different DNS names can share the same IP address. This is common with
cloud services, content delivery networks (CDNs), and shared hosting. An address
can also be reassigned after the cache records it. Keep these cases in mind when
you use cached reverse lookups for routing or filtering. Do not use a DNS-cache
match as the only security boundary.

## Check DNS privacy

When DNS privacy is required, check both paths: the application traffic path and
the resolver's upstream Network. A protected traffic path with direct host DNS
can still reveal requested names.
