VTun supplies a
[userspace IP stack](https://gvisor.dev/docs/architecture_guide/networking/) as
both [Network](@port/left/Network) and [Tun](@port/right/Tun). Connect client or
server nodes to [Network](@port/left/Network). Connect [Tun](@port/right/Tun) to
the path that carries the stack's IPv4 and IPv6 packets, such as
[WireGuard](@docs/node/almagest/WireGuard),
[TunCopy](@docs/node/almagest/TunCopy),
[TunSplitter](@docs/node/almagest/TunSplitter), or
[Spoofer](@docs/node/almagest/Spoofer).

Applications on [Network](@port/left/Network) can use TCP, UDP, listeners, and
DNS through this stack. Their outgoing IP packets go through
[Tun](@port/right/Tun). Packets that return through the TUN path enter the same
stack and reach the applicable connection or listener. Multiple applications can
use [Network](@port/left/Network), but [Tun](@port/right/Tun) accepts one
connection.

VTun creates its stack only while at least one of these ports is connected. The
default name is `vtun`. The **Effective** section shows the active local
addresses, address families, DNS servers, MTU, and MTU source.

## Local addresses and routes

Address selection uses this priority:

1. **Local addrs** in the menu.
1. Address suggestions from the connected TUN path.
1. Automatically allocated local IPv4 and IPv6 addresses.

Enter one or more IP addresses in **Local addrs**, separated by spaces, commas,
semicolons, or new lines.
[CIDR](https://en.wikipedia.org/wiki/Classless_Inter-Domain_Routing) values are
accepted, but VTun uses only their IP address. The prefix does not create a
route. Automatically allocated addresses appear in **Effective** and are not
saved as menu configuration.

By default, the stack also has `127.0.0.1` and `::1`. Enable **No loopback
addrs** when the packet path must not select loopback addresses. With this
option enabled, the configured or suggested addresses determine whether the
stack supports IPv4, IPv6, or both. The address families in **Effective** show
what the stack can use; they do not confirm that the TUN path can reach the
Internet with those families.

The TUN path can suggest routes and source routes. A source route selects the
local address for a destination prefix. The most-specific prefix has priority.
For a plain route without an explicit source, VTun uses the first suggested
local address of the same IP family.

When a family has at least one source route but no default source route, VTun
adds `0.0.0.0/0` or `::/0` with the first source address for that family. This
keeps other destinations reachable while the more-specific routes keep priority.

Setting **Local addrs** disables all suggested source routes because their
source addresses can conflict with the explicit address list. Leave **Local
addrs** empty when a controller or TUN node must supply address and source route
policy together.

## MTU

**MTU ceiling** is `0` by default. This automatic mode uses the positive MTU
suggested by the TUN path. If there is no positive suggestion, VTun uses 1500.

A positive menu value is a ceiling, not a forced MTU. VTun uses the smaller of
the menu value and the suggested MTU. Keep a positive ceiling when the carrier
cannot report a safe limit. The **MTU source** field explains which limit VTun
selected.

MTU-only changes apply to the active stack without closing its Network when the
active stack supports the update. A TUN path that reports an MTU dependency
cycle is ignored to prevent an invalid self-dependent value.

## DNS

DNS server selection uses this priority:

1. **DNS servers** in the menu.
1. A DNS server suggested by the TUN path.
1. `8.8.8.8`.

Enter IP addresses only in **DNS servers**. Hostnames and CIDR prefixes are not
valid. You can configure more than one address. A TUN suggestion contains one
address.

The DNS suggestion is intended for a simple direct path, such as VTun to
[WireGuard](@docs/node/almagest/WireGuard). It cannot describe resolver routing,
composition, or policy. Use DNS nodes and DNS connections for those
configurations.

## Advanced settings

**Endpoint channel** sets the packet queue capacity. The default is 1024.
**Event channel** sets the event queue capacity. The default is 10. Larger
queues can absorb longer bursts but can use more memory. Values that are zero or
negative reset to the defaults.

Enable **Disable
[TCP SACK](https://en.wikipedia.org/wiki/Transmission_Control_Protocol#Selective_acknowledgments)**
only when the peer or carrier has a compatibility problem with TCP Selective
Acknowledgment. Disabling it can reduce TCP performance when packets are lost.

## Configuration changes

Applying a valid configuration updates the node and saves the values. An invalid
address or negative MTU changes the label to `invalid config` and does not
replace the working configuration.

A change to the name, effective addresses, DNS servers, queue sizes, loopback
setting, or TCP SACK setting replaces the active stack. This can close active
connections and listeners. An effective MTU change or a change that affects only
source routes is applied without replacement. Repeating an equivalent TUN
suggestion does not replace the stack.

Removing the [Tun](@port/right/Tun) connection clears all address, route, MTU,
and DNS suggestions that came from that connection. VTun then recalculates its
effective configuration from the menu values and defaults.
