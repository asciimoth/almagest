# TUN configuration suggestions

`almagest/tun` carries a TUN payload from a provider to a consumer. It also
carries `TunConfig` suggestions from the consumer back to the provider.
`TunConfig` can suggest interface addresses, routes, destination-to-source
routes, an MTU, and one DNS server IP address.

`SourceRoutes` preserves the association between a remote destination prefix
and the local address that is valid for that route. This is necessary when one
userspace network stack has two or more local addresses from the same IP
family. The most-specific destination prefix has priority. A default route is
not implicit for an IP family that has source routes, so the policy must include
`0.0.0.0/0` or `::/0` when that family needs a fallback.

`TunSplitter` creates source routes before it merges output configs. For each
plain route without an explicit source route, it uses the first valid address
of the same IP family from that output config. This keeps addresses from one
splitter slot associated with the routes from that slot. A consumer can send an
explicit source route when it needs a different address for a route.

The `DNS` field is optional. It must contain one IPv4 or IPv6 address, such as
`8.8.8.8`. An empty value means that there is no DNS suggestion. It does not
accept a host name, a CIDR prefix, or a list of servers.

Use this DNS suggestion only for a simple direct path, such as:

```text
VTun --- WireGuard
```

The suggestion does not replace the `almagest/dns` link type. Use DNS links
and DNS nodes when a graph needs multiple resolvers, DNS routing, resolver
composition, caching, or policy.

TUN middleware with one TUN input and one TUN output passes the DNS suggestion
through. `TunFirewall` and `LogTun` follow this rule. `LogTun` also writes the
complete `TunConfig` value to its debug log.

Middleware with multiple TUN inputs or outputs drops the DNS suggestion because
one address cannot describe a branched graph. `TunSplitter` follows this rule,
but it preserves and creates source routes as described above. `TunJoiner` is
the explicit exception: it sends the same downstream suggestion to its default
and secondary inputs. `TunCopy` has two inputs and does not forward
configuration suggestions.

Node behavior is as follows:

- `VTun` passes source-route suggestions to gonnect-netstack and uses live route
  updates when only that policy changes. An explicit local addresses field
  disables source-route suggestions because it replaces the suggested address
  list. VTun uses the DNS suggestion when its own DNS servers field is empty.
  Its own field has priority. If neither value is set, VTun uses `8.8.8.8`.
- `Tun` drops the DNS suggestion. It continues to use address, route, and MTU
  suggestions.
- `DefaultTun` passes source-route suggestions to the system network
  implementation. A source-route change rebuilds the logical default-TUN
  configuration, but an implementation can reuse the active native device.
  `DefaultTun` drops the DNS suggestion. Configure its DNS service through its
  `almagest/dns` port.
- `TunConfigStripper` drops the complete `TunConfig`, including DNS.

WireGuard has an optional `DNS` menu field. WireGuard validates it as one IP
address and sends it in `TunConfig.DNS` with its owned-IP and peer-route
suggestions.
