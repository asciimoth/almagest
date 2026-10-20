`DnsRouter` wraps one `github.com/asciimoth/gonnect/dns.Router` and publishes
it as an `almagest/dns` interface.

The left `DNS` port emits detached wrappers around the router. Each right-side
`slot N` port accepts one upstream DNS interface and installs it in the router
under the same backend name. Detaching a slot removes that backend and closes
only the DNS wrapper received by this node; the router instance and other slots
remain alive.

Routing rules use the `github.com/asciimoth/gonnect/routing` DNS bytecode text
form. Rules are evaluated top to bottom against the first DNS question in the
message. Empty or comment-only rules route everything to `slot 1` by compiling
the same default as `TRUE` followed by `BACKEND slot 1`.

Each route segment ends with `BACKEND slot N` or `DROP`. `BACKEND` names must
use the node's existing `slot N` port names, from `slot 1` through `slot 16`.
A non-empty ruleset has no implicit fallback; add `TRUE` and `BACKEND slot N`
when unmatched requests should continue to a backend.

```text
ADDR_RE (^|.*\.)example\.test\.$
BACKEND slot 1

ADDR_S host.example.test.
QTYPE AAAA
AND
BACKEND slot 2

TRUE
BACKEND slot 1
```

Useful DNS predicates include `ADDR_S` for exact case-insensitive names,
`ADDR_RE` for regexp name matches, `QTYPE`, `QCLASS`, `OPCODE`, `NET4`, `NET6`,
`ADDR4`, `ADDR6`, `SNET4`, and `SNET6`. Use `AND`, `OR`, and `NOT` to combine
predicates. Line comments that start with `#` and blank lines are ignored.

Only slots mentioned by rules, slots that still have links, and the default
`slot 1` for an empty ruleset are kept visible. The node keeps the underlying
router stable across rule edits, slot attach/detach, copy/paste, and save/restore
so existing downstream DNS consumers continue to hold a valid router wrapper.
