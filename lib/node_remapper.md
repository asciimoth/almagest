# Network Remapper

Wraps one input `almagest/network` value with `gonnect.Remapper` and publishes
the remapped network on one output port. Use it between network-producing nodes
and network-consuming nodes when calls should be redirected without changing the
rest of the graph.

Rules are evaluated from top to bottom. Each matching rule rewrites the selected
source or destination endpoint, and later rules see the values left by earlier
rules.

```text
ENDPOINT FIELD REPLACEMENT [MATCH pattern] [OP operation[,operation...]]
```

`ENDPOINT` is `SRC` or `DST`.

`FIELD` is:

- `ADDR`: replace only the host/address part.
- `PORT`: replace only the port part.
- `ADDRPORT`: replace both host/address and port. The replacement can be
  `host port` or one `host:port` token.

`MATCH` is optional. Its pattern uses Gonnect's `FilterFromString` syntax:
hostnames, wildcard host patterns, IP literals, CIDRs, and optional ports are
supported. Matching is syntactic and does not resolve names.

`OP` is optional. It restricts a rule to specific Gonnect remap operation names,
for example `Dial`, `Listen`, `PacketDial`, `ListenPacket`, `DialTCP`,
`ListenTCP`, `DialUDP`, or `ListenUDP`.

Examples:

```text
# Redirect client dials from example.test:80 to a local service port.
DST ADDRPORT 127.0.0.1 8080 MATCH example.test:80 OP Dial

# Rewrite listen addresses to bind on loopback.
SRC ADDR 127.0.0.1 OP Listen,ListenTCP

# Change only the destination port for every dial-style address with that port.
DST PORT 8443 MATCH *:443
```
