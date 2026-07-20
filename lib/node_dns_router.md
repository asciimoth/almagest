`DnsRouter` wraps one `github.com/asciimoth/gonnect/dns.Router` and publishes
it as an `almagest/dns` interface.

The left `DNS` port emits detached wrappers around the router. Each right-side
`slot N` port accepts one upstream DNS interface and installs it in the router
under the same backend name. Detaching a slot removes that backend and closes
only the DNS wrapper received by this node; the router instance and other slots
remain alive.

Routing rules are evaluated top to bottom against the first DNS question in the
message. Empty or comment-only rules route everything to `slot 1`. A non-empty
ruleset has no implicit fallback; add `DEFAULT SLOT N` when unmatched requests
should continue to a backend.

Supported rule tokens:

```text
SUFFIX example.test SLOT 1
NAME host.example.test SLOT 2
TYPE AAAA SLOT 3
SUFFIX internal.test TYPE A SLOT 4
DEFAULT SLOT 1
```

`SUFFIX` matches the exact name and subdomains. `NAME` matches only the exact
DNS name. `TYPE` accepts the record names supported by `StaticDns` or a numeric
DNS type from 1 to 65535. `DOMAIN` is an alias for `SUFFIX`, `EXACT` is an alias
for `NAME`, and `ANY` or `TRUE` can be used instead of `DEFAULT`.

Only slots mentioned by rules, slots that still have links, and the default
`slot 1` for an empty ruleset are kept visible. The node keeps the underlying
router stable across rule edits, slot attach/detach, copy/paste, and save/restore
so existing downstream DNS consumers continue to hold a valid router wrapper.
