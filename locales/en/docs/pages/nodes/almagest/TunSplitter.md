TunSplitter sends packets from one TUN connection to numbered output slots.
Use this node when you must select each path with explicit packet rules. Use
AutoTunSplitter instead when the output paths advertise the routes that they
can reach.

Connect the shared TUN to **Tun**, and connect each path to a numbered slot.
Rules apply to packets read from the shared TUN. Packets written by any slot
return to the shared TUN without rule evaluation.

## Routing rules

Rules are evaluated from top to bottom. Each rule segment ends with `SLOT N`
or `DROP`. The first matching action stops evaluation. `SLOT N` sends the
packet to that slot. `DROP` discards the packet.

There is no implicit default slot. A packet is also dropped if no rule
matches, if the selected slot is not connected, or if the packet is not a
valid IPv4 or IPv6 packet. Empty or comment-only rules therefore drop all
packets. Add a final `TRUE` segment when you need a default path.

This example sends traffic for the Tailscale IPv4 range to slot 2 and sends
all other valid packets to slot 1:

```text
SNET4 100.64.0.0/10
SLOT 2

TRUE
SLOT 1
```

Use these predicates to inspect a packet:

- `NET4` and `NET6` match the IP version. `TCP` and `UDP` match the transport
  protocol.
- `ADDR4`, `ADDR6`, `SNET4`, and `SNET6` match the destination address or
  destination subnet. Add the `L` prefix, such as `LADDR4` or `LSNET6`, to
  match the source address.
- `PORT` matches the destination TCP or UDP port. `LPORT` matches the source
  port. A port predicate is false when the packet has no usable port header.
- `ADDR_S` and `ADDR_RE` match the destination IP text or a cached reverse DNS
  name. The `LADDR_S` and `LADDR_RE` forms use the source. Select **DNS cache
  storage** to enable cached-name matches. The node does not make live reverse
  DNS requests.
- `RULE type text` applies a platform-specific process or socket-owner rule.
  These rules require a usable TCP or UDP flow. They work only with packets
  from a native system TUN. If you put middleware nodes between the native TUN
  and TunSplitter, these rules might not work. The result depends on whether
  each middleware node preserves the flow information that the platform needs.

Use `TRUE`, `FALSE`, `NOT`, `AND`, and `OR` to make conditions. Boolean
operators use postfix order: put their input values before the operator. For
example, `TCP`, `PORT 443`, `AND` matches TCP packets with destination port
443.

`FQDN` and `LFQDN` do not match because a raw IP packet does not contain an
address hostname. `DIAL`, `LISTEN`, and `LOOKUP` are not valid in TunSplitter
rules. Blank lines and lines that start with `#` are ignored. A `#` after an
operation is not an inline comment.

### Linux platform rules

Linux supports these `RULE` types:

- `comm`: a regular expression for the process command name
- `exec`: an executable basename or absolute-path pattern
- `cmd`: a regular expression for the complete command line
- `pid`: one or more space-separated process IDs
- `user`: a user name
- `uid`: one or more space-separated user IDs
- `group`: a group name
- `gid`: one or more space-separated group IDs

These rules require the Linux process-tracking and socket-owner features. The
node reports an error if the host does not provide the required feature.

### Windows platform rules

Windows supports these `RULE` types:

- `win-pid`: one process ID
- `win-exe-path`: the absolute path of the executable that owns the flow

Executable paths use exact, case-insensitive matching. The path must use a
local drive letter. Relative paths, UNC paths, and wildcard patterns are not
supported. Windows socket ownership is best effort. Short-lived sockets,
shared UDP sockets, protected processes, and processes that have stopped can
prevent a match.

Rules add the slots that they reference. The node also keeps one unused slot
available while fewer than 16 slots exist. A connected slot stays available
if new rules stop referencing it. The slot can disappear after you disconnect
it. Invalid rule text is not applied, so the last valid rules continue to
route packets while you correct the text.

## DNS names and caches

To route by a cached name, populate the selected cache storage with a DnsCache
node. Exact cached names match with or without one trailing dot. Regular
expressions are also tested with both forms.

The rule cache stores system-rule results for a flow. The route cache stores
the final slot decision for a flow. A zero TTL or size uses the default. A
negative TTL or size disables that cache. If both route-cache values are zero,
the route cache follows the rule-cache settings. Reduce the TTL when external
process rules can change during a connection.

## TUN configuration

The node merges address, route, source route, and MTU suggestions from all
connected output slots and sends the result to the shared TUN provider. Packet
rules do not use these suggestions and do not change when a suggestion changes.

For a plain route, TunSplitter associates the first valid address of the same
IP family from that slot. This keeps each destination route associated with a
source address from the same path after all slot suggestions are merged. An
explicit source route suggestion has priority for its route.

DNS suggestions are not sent through TunSplitter because different slots can
use different DNS services. Use DNS links and DNS nodes when the graph needs
DNS routing, multiple resolvers, caching, or policy.
