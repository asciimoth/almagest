NetworkRouter selects one backend Network for each network operation. Connect
consumers to [Network](@port/left/Network), and connect backend paths to the
numbered slots. The node supports slots 1 through 16. Each slot accepts one
Network.

Rules are evaluated from top to bottom. Each rule segment ends with `SLOT N` or
`DROP`. `SLOT N` sends a matching operation to that slot. `DROP` rejects a
matching operation. The first matching action stops evaluation.

There is no automatic fallback. An operation fails if no rule matches, if the
selected slot is disconnected or not ready, or if a rule uses `DROP`. Empty or
comment-only rules reject all operations. Add this segment at the end when slot
1 must be the default:

```rulev1
TRUE
SLOT 1
```

Rules add the slots that they reference. A connected slot stays visible after
rules stop referencing it, and disappears after you disconnect it. If no rule or
connection needs a slot, slot 1 remains visible, but it is not an implicit
route. Invalid rule text is not applied, so the last valid rules continue to
operate.

## Rule predicates

Use these predicates to describe an operation:

- `DIAL`, `LISTEN`, and `LOOKUP` match the operation type. `DIAL` also matches
  the route decision for each outgoing packet from an unconnected UDP socket.
  `LISTEN` applies to TCP listeners.
- `TCP` and `UDP` match the transport. `NET4` and `NET6` match the address
  family.
- `FQDN` matches a remote hostname instead of an IP address. `LFQDN` applies the
  same test to the local address.
- `ADDR_S value` and `ADDR_RE expression` match the remote host text exactly or
  with a regular expression. They do not include the port. The `LADDR_S` and
  `LADDR_RE` forms match the local host text.
- `ADDR4`, `ADDR6`, `SNET4`, and `SNET6` match an exact remote IP address or
  subnet. Add the `L` prefix, such as `LADDR4` or `LSNET6`, to match the local
  address.
- `PORT number` matches the remote port. `LPORT number` matches the local port.

String and regular-expression matching is case-sensitive. `FQDN` means any
non-IP host text; it does not require a trailing dot. Use `TRUE`, `FALSE`,
`NOT`, `AND`, and `OR` to combine conditions. Combined conditions use postfix
order: put the values first, and then put the operator after them.

This example sends TCP dials for `internal.example` and its subdomains to slot
2\. It sends all other operations to slot 1:

```rulev1
DIAL
TCP
AND
ADDR_RE (^|.*\.)internal\.example$
AND
SLOT 2

TRUE
SLOT 1
```

Operation names are not case-sensitive. Blank lines and lines that start with
`#` are ignored. A `#` after an operation is not an inline comment.

## DNS behavior

Without a connection to [DNS](@port/right/DNS), `LOOKUP` rules select the
backend that handles each name lookup. Hostname dials keep their original host
text during routing, so `ADDR_S` and `ADDR_RE` can match that text.

When [DNS](@port/right/DNS) is connected, all explicit name lookups go directly
to that DNS service and bypass the backend slots. The router also resolves host
and service names through that DNS service before it selects a slot for a dial
or listener. Address rules then see the selected numeric address instead of the
original hostname.

**DNS cache storage** has a different purpose. Select a cache when `ADDR_S` or
`ADDR_RE` must match a cached reverse name for a numeric IP address. A
[DnsCache](@docs/node/almagest/DnsCache) node must populate that same storage
with reverse records. NetworkRouter does not perform a live reverse lookup for
these matches. Exact cached DNS names match with or without one trailing dot.
