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

`TunSplitter` and `AutoTunSplitter` create source routes before they merge
output configs. For each plain route without an explicit source route, they use
the first valid address of the same IP family from that output config. This
keeps addresses from one splitter slot associated with the routes from that
slot. A consumer can send an explicit source route when it needs a different
address for a route.

`AutoTunSplitter` also uses each slot's complete latest `TunConfig` as its
packet-routing input. The most-specific destination in `Routes` or
`SourceRoutes` selects the slot. If multiple slots claim the same most-specific
prefix, the source associated with that route can select between those slots;
otherwise, the lower slot number wins. An address does not make its slot own
unrelated destinations. Invalid address and prefix suggestions are ignored,
and unmatched packets use slot 1. A replacement config removes learned entries
that it omits. Removing or replacing a slot link removes its learned entries
before the next consumer can send a config.

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
one address cannot describe a branched graph. `TunSplitter` and
`AutoTunSplitter` follow this rule, but they preserve and create source routes
as described above. `TunJoiner` is the explicit exception: it sends the same
downstream suggestion to its default and secondary inputs. `TunCopy` has two
inputs and does not forward configuration suggestions.

Node behavior is as follows:

- `VTun` passes source-route suggestions to gonnect-netstack and uses live route
  updates when only that policy changes. An explicit local addresses field
  disables source-route suggestions because it replaces the suggested address
  list. VTun uses the DNS suggestion when its own DNS servers field is empty.
  Its own field has priority. If neither value is set, VTun uses `8.8.8.8`.

  VTun infers source routes again after it merges all TUN suggestions. This
  permits one consumer to advertise a route and another consumer to supply the
  address for that route. If a family has source routes but no default source
  route, VTun adds a default that uses the first suggested source for that
  family. This keeps general traffic reachable while more-specific source
  routes keep priority.

  VTun reports its effective address families in the node menu. The value shows
  which families the userspace stack can use. It does not prove that the carrier
  can reach the public Internet with those families. A dual-stack VTun uses the
  gonnect-netstack TCP fallback behavior when one resolved family cannot connect.

  `noLoopbackAddr` controls whether VTun adds `127.0.0.1` and `::1`. It controls
  address assignment only. It is not a reachability check and does not replace
  TCP fallback. To require IPv4-only operation, configure one unique IPv4 local
  address and enable `noLoopbackAddr`. The equivalent rule applies to IPv6-only
  operation.
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
