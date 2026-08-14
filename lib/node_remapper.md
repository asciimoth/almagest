# Network Remapper

Wraps one input `almagest/network` value with `gonnect.Remapper` and publishes
the remapped network on one output port. Use it between network-producing nodes
and network-consuming nodes when calls should be redirected without changing the
rest of the graph.

Rules are evaluated from top to bottom. Each matching rule rewrites the selected
source or destination endpoint, and later rules see the values left by earlier
rules.

Rules use the `github.com/asciimoth/gonnect/routing` remapper bytecode text
form. Each segment contains zero or more predicates and ends with a `REMAP`
action:

```text
REMAP ENDPOINT FIELD VALUE
```

`ENDPOINT` is `SRC` or `DST`.

`FIELD` is:

- `ADDR_PORT`: replace both host/address and port. `VALUE` must be one
  `host:port` token, such as `127.0.0.1:8080` or `[::1]:9443`.
- `ADDR`: replace only the host/address part.
- `PORT`: replace only the port part.

Predicates are stack-based bytecode operations such as `DIAL`, `LISTEN`, `TCP`,
`UDP`, `ADDR_S`, `ADDR_RE`, `PORT`, `LADDR_S`, `LADDR_RE`, `LPORT`, `AND`,
`OR`, and `NOT`. Matching is syntactic and does not resolve names.

Examples:

```text
# Redirect client dials from example.test:80 to a local service port.
DIAL
ADDR_S example.test
AND
PORT 80
AND
REMAP DST ADDR_PORT 127.0.0.1:8080

# Rewrite listen addresses to bind on loopback.
LISTEN
REMAP SRC ADDR 127.0.0.1

# Change only the destination port for every dial-style address with that port.
DIAL
PORT 443
AND
REMAP DST PORT 8443
```
