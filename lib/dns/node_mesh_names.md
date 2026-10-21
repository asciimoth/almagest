# MeshNames Node

`almagest/MeshNames` publishes an `almagest/dns` provider backed by
`github.com/asciimoth/gonnect/dns/meshnames.Resolver`.

## Ports

- `DNS`, left-directed `almagest/dns`: emits detached DNS wrappers to consumers.
- `Network`, right-directed `almagest/network`: required. Mesh authority DNS
  exchanges are dialed through this network, and no DNS provider is published
  until this input is linked and ready.
- `Fallback DNS`, right-directed `almagest/dns`: optional. The node adapts this
  DNS input to a `gonnect.Resolver` and installs it as the MeshNames fallback
  for names outside the mesh-specific suffixes understood by `meshnames`.

## Resolution

Mesh-specific names such as `.meshname`, `.meship`, `.pk.ygg`, and `.ygg` are
handled by `gonnect/dns/meshnames`. Names outside those schemes are rejected
unless `Fallback DNS` is explicitly linked.

## Configuration

The `Request timeout (ms)` menu field limits one DNS exchange with an
authoritative mesh node. Set it to `0` to use the gonnect default. Saved config
stores it in `requestTimeoutMS`.

## Lifecycle

The node owns one `meshnames.Resolver` and one `dns.ResolverProvider` for the
current Network, optional Fallback DNS input, and timeout. Replacing or
detaching an input, or changing the timeout, closes previously emitted DNS
wrappers, closes the old provider, and publishes a fresh provider when the
required Network input is ready. Detaching the fallback rebuilds the resolver
with a nil fallback so ordinary names cannot leak into an implicit direct or
system resolver.
