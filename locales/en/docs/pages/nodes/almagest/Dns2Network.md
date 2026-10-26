Provides [DNS](@port/left/DNS) service through the resolver of an attached
[Network](@port/right/Network). Connect the network to
[Network](@port/right/Network), then connect DNS consumers to
[DNS](@port/left/DNS).

The node converts each [DNS](https://www.rfc-editor.org/rfc/rfc1034) question to
a resolver lookup on the [Network](@port/right/Network). It does not select a
DNS server or DNS transport. The attached [Network](@port/right/Network)
controls how it resolves names. For example, if DNS was injected into the
[Network](@port/right/Network), this node makes that injected resolver available
again as a [DNS](@port/left/DNS) output.

The node supports these
[Internet-class record types](https://www.iana.org/assignments/dns-parameters/dns-parameters.xhtml#dns-parameters-4):

- `A` and `AAAA`;
- `PTR` for complete IPv4 and IPv6 reverse names;
- `CNAME`, `TXT`, `MX`, `NS`, and `SRV`.

Successful answers have a TTL of 60 seconds. A name-not-found error from the
Network becomes an `NXDOMAIN` response. Other lookup errors become `SERVFAIL`.
Unsupported record types and DNS classes return `NOTIMP`. A malformed request,
including a malformed reverse name, returns `FORMERR`.

The [DNS](@port/left/DNS) output is available only while
[Network](@port/right/Network) is ready. If the [Network](@port/right/Network)
is removed or replaced, current DNS outputs close and in-progress requests are
canceled. The node supplies new DNS outputs when the replacement
[Network](@port/right/Network) is ready. Removing one DNS consumer does not
affect the [Network](@port/right/Network) or other DNS consumers.
