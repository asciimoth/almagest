Every port has a type. You can connect two ports only when their types are
compatible. Port colors help you identify types. Also read the port name and the
node documentation when a graph is unfamiliar. Read
[Nodes, ports, and links](@docs/almagest/graph) first if these graph terms are
new to you.

## Network

A Network link gives connected nodes a reusable network environment. According
to its features, a node can use the link to create connections, open listeners,
resolve names, or exchange datagrams. The link does not represent one connection
or an operating-system network interface.

Network nodes can have one or more of these roles:

- Providers supply a network environment. Examples are
  [OutNetwork](@docs/node/almagest/OutNetwork), for the service's direct host
  network, [TorDriver](@docs/node/almagest/TorDriver), for TCP and DNS through
  Tor, and [MockNetwork](@docs/node/almagest/MockNetwork), for an isolated test
  network.
- Consumers use a network environment. Clients, servers, and
  [WireGuard](@docs/node/almagest/WireGuard) are examples.
- Middleware nodes both consume and provide Network links. Proxies, routers, and
  firewalls can examine or change network operations before they pass them to
  another Network link.

A Network link carries operations such as "connect to this host and port" or
"listen on this address." It works with connections instead of raw IP packets.
This lets middleware keep information such as the requested hostname or the type
of operation.

For compatibility, a Network link also supports simple hostname resolution. The
Network provider decides how it resolves these names. A DNS link gives more
direct control of DNS questions, routing, and caching.
[DnsInject](@docs/node/almagest/DnsInject) adds a DNS resolver to a Network.
[Dns2Network](@docs/node/almagest/Dns2Network) makes the resolver of a Network
available through a DNS link. See [DNS](@docs/almagest/dns) for these adapters
and complete resolver paths.

This graph uses one MockNetwork for an HTTP client and server. All communication
stays in memory. It does not use the host network.

```graph
{
  "HTTP Client": {
    "Class": "almagest/HttpClient",
    "Pos": "{\"x\":61,\"y\":67}",
    "Links": ["Network -> [Mock Network] Network"],
  },
  "HTTP Server": {
    "Class": "almagest/HttpServer",
    "Pos": "{\"x\":61,\"y\":173}",
    "Links": ["Network -> [Mock Network] Network"],
  },
  "Mock Network": {
    "Class": "almagest/MockNetwork",
    "Pos": "{\"x\":291,\"y\":120}",
  },
}
```

## TUN

A TUN link connects packet endpoints. It carries raw IPv4 and IPv6 packets in
both directions between the connected nodes. A TUN provider does not always
create an operating-system interface.

[DefaultTun](@docs/node/almagest/DefaultTun) and [Tun](@docs/node/almagest/Tun)
create host interfaces and provide their packet paths. Other nodes can split,
join, filter, wrap, or consume these paths.

Nodes connected by a TUN link can also send suggested interface settings. The
suggestions can include addresses, routes, source routes, DNS servers, and MTU.
Each node decides which suggestions it supports and which ones it applies. For
example, the node that owns a host interface decides which settings it can apply
to that interface.

[TunConfigStripper](@docs/node/almagest/TunConfigStripper) passes packets but
blocks all these suggestions. Use it when the packet path must stay connected,
but the settings on one side must not configure the other side.

### Network and TUN adapters

[Spoofer](@docs/node/almagest/Spoofer) consumes a TUN packet path. It converts
TCP and UDP flows from that path into new connections through a Network link.

[VTun](@docs/node/almagest/VTun) converts in the opposite direction. It provides
a Network backed by a userspace IP stack. The stack sends raw packets through
the TUN link. Packets that arrive through the TUN link supply data to the
applicable Network connections and listeners.

This graph converts HTTP client connections to packets and then converts the TCP
or UDP flows back to Network connections for the host network.

```graph
{
  "HTTP Client": {
    "Class": "almagest/HttpClient",
    "Pos": "{\"x\":31,\"y\":120}",
    "Links": ["Network -> [IP Stack] Network"],
  },
  "IP Stack": {
    "Class": "almagest/VTun",
    "Pos": "{\"x\":199,\"y\":120}",
    "Links": ["Tun -> [Flow Converter] Tun"],
  },
  "Flow Converter": {
    "Class": "almagest/Spoofer",
    "Pos": "{\"x\":368,\"y\":120}",
    "Links": ["Network -> [Outside Network] Network"],
  },
  "Outside Network": {
    "Class": "almagest/OutNetwork",
    "Pos": "{\"x\":552,\"y\":120}",
  },
}
```

### Virtual test paths

[TunCopy](@docs/node/almagest/TunCopy) connects two TUN providers directly. For
example, two VTun nodes and TunCopy make a fully virtual IP network. This setup
does not create a host interface or use the host network.

```graph
{
  "Client IP Stack": {
    "Class": "almagest/VTun",
    "Pos": "{\"x\":61,\"y\":80}",
    "Links": ["Tun -> [Direct TUN Link] TunA"],
  },
  "Direct TUN Link": {
    "Class": "almagest/TunCopy",
    "Pos": "{\"x\":276,\"y\":133}",
  },
  "Server IP Stack": {
    "Class": "almagest/VTun",
    "Pos": "{\"x\":61,\"y\":187}",
    "Links": ["Tun -> [Direct TUN Link] TunB"],
  },
}
```

MockNetwork gives multiple Network consumers a fully virtual connection and
listener environment. TunCopy gives two TUN providers a fully virtual packet
path. Add [HttpClient](@docs/node/almagest/HttpClient) and
[HttpServer](@docs/node/almagest/HttpServer) to test the topology and traffic
path. These HTTP nodes are diagnostic tools, not application services.

## DNS

A DNS link gives connected nodes a DNS question-and-answer service. It is
separate from a Network link and from the host DNS configuration. DNS nodes can
provide static records, forward questions, route questions, cache answers, or
make DNS available through a Network or TUN path.

[DefaultTun](@docs/node/almagest/DefaultTun) has a separate DNS input. A packet
route does not identify which resolver the host must use. See
[DNS](@docs/almagest/dns) for resolver paths, caches, and reverse lookups.

## Object

An Object link carries JSON-compatible structured data. An Object can contain
objects, arrays, strings, numbers, Boolean values, and `null`. Use Object when
one link must carry several named fields or a variable structure.

[ObjectPacker](@docs/node/pasta/ObjectPacker) builds an Object from fields.
[ObjectUnpacker](@docs/node/pasta/ObjectUnpacker) extracts fields. Their menus
define the field names and types. Other Object nodes can get or set fields and
convert an Object to text. Object operations do not grant access to service
configuration or stored secrets.

## Specialized types

Some nodes add a link type for one protocol or control interface. For example,
[WGConfig](@docs/node/almagest/WGConfig) and WireGuard nodes use a WireGuard
control link. Only nodes that support the same specialized type can connect to
that link. The upstream
[WireGuard conceptual overview](https://www.wireguard.com/#conceptual-overview)
explains peer keys, endpoints, and allowed IPs at the protocol level.
