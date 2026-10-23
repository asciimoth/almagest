Rewrites connection and listener addresses on a connected
[Network](@port/right/Network). Use it to redirect a client to a different
service or to change the address on which a server listens.

An empty **Remap rules** field makes no changes. DNS lookup operations also pass
through without changes.

## Rule structure

Write one instruction on each line. Comments start with `#`. Each rule has zero
or more predicates and ends with this action:

```rulev1
REMAP ENDPOINT FIELD VALUE
```

Use these action values:

- `DST` changes the destination of a connection operation.
- `SRC` changes a listener address or an explicit local address of a connection
  operation.
- `ADDR_PORT` replaces the complete host and port. Use `host:port`, and put an
  IPv6 address in brackets, for example `[::1]:8443`.
- `ADDR` replaces only the host and keeps the current port.
- `PORT` replaces only the port and keeps the current host.

A port action can use a decimal port or a service name. If an address action
changes between an IPv4 and IPv6 literal, the node also changes an explicit
protocol family such as `tcp4` or `tcp6` to match.

## Predicates

Use predicates to select operations and addresses:

- Operation: `DIAL` or `LISTEN`.
- Protocol and family: `TCP`, `UDP`, `NET4`, or `NET6`.
- Remote address: `FQDN`, `ADDR_S`, `ADDR_RE`, `ADDR4`, `ADDR6`, `SNET4`,
  `SNET6`, or `PORT`.
- Local address: `LFQDN`, `LADDR_S`, `LADDR_RE`, `LADDR4`, `LADDR6`, `LSNET4`,
  `LSNET6`, or `LPORT`.
- Logic: `TRUE`, `FALSE`, `NOT`, `AND`, or `OR`.

`FQDN` and `LFQDN` test whether the address contains a hostname instead of an IP
address. `ADDR_S` and `LADDR_S` test an exact host string. `ADDR_RE` and
`LADDR_RE` use a regular expression. The address-number predicates test one IP
address, and the `SNET` predicates test a CIDR subnet. `PORT` and `LPORT` take a
numeric port from 0 through 65535.

Predicates use postfix order. Put each test before the operator that combines
it. This predicate matches a connection to `service.test` on port 80:

```rulev1
DIAL
ADDR_S service.test
AND
PORT 80
AND
```

The predicate for each rule must produce one true or false result. A rule with
no predicate matches every operation that has the selected endpoint.

## Examples

Redirect a matching client connection to a local service:

```rulev1
DIAL
ADDR_S service.test
AND
PORT 80
AND
REMAP DST ADDR_PORT 127.0.0.1:8080
```

Change all listener hosts to IPv4 loopback and keep their configured ports:

```rulev1
LISTEN
REMAP SRC ADDR 127.0.0.1
```

Rules run from top to bottom. Every matching rule applies. Later rules see
changes from earlier rules, and the last matching rewrite of the same field
wins.

Matching is syntactic. The node does not resolve hostnames or service names
while it evaluates rules. For example, `ADDR_S service.test` does not match an
operation that already uses the resolved IP address.

## Validation and status

The node rejects an unknown instruction, an invalid regular expression, IP
address, or subnet predicate, an incomplete action, and a predicate that does
not produce one result. Invalid rules are not applied, and the node label
becomes `invalid config`.

The label is `waiting for network` until the source Network is available. With
one or more valid rules, the label shows the number of configured rules.
