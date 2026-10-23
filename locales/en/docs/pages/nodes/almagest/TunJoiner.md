TunJoiner combines multiple TUN paths into one [Tun](@port/right/Tun)
connection. Connect one fallback path to [Default](@port/left/Default). Connect
any number of additional paths to the shared [Secondary](@port/left/Secondary)
port.

[Default](@port/left/Default) and [Tun](@port/right/Tun) each accept one
connection. [Secondary](@port/left/Secondary) accepts multiple connections on
the same port. It does not create numbered slots, and the order of the secondary
connections does not set a priority. TunJoiner has no menu settings.

Use TunJoiner when independent TUN paths must share the next processing stage.
For example, several [VTun](@docs/node/almagest/VTun) clients can share one
[Spoofer](@docs/node/almagest/Spoofer) or one outbound TUN path:

```text
VTun main --------> Default
VTun application -> Secondary -> TunJoiner -> Spoofer
VTun overlay -----> Secondary
```

## Packet routing

Packets from all connected paths go to [Tun](@port/right/Tun). For packets that
return from [Tun](@port/right/Tun), TunJoiner learns which connected path owns
the destination.

It first uses the learned flow for TCP, UDP, ICMP echo, and IP fragments. This
lets different paths use the same local IPv4 or IPv6 address when their flow
details are different. If there is no learned flow, it uses the path that most
recently sent a packet from the destination IP address. If there is no learned
address, it sends the packet to [Default](@port/left/Default).

Malformed packets and packets with an unknown destination also go to
[Default](@port/left/Default). If no default path is available, TunJoiner drops
these packets. A secondary path can still exchange packets before the default
path connects when TunJoiner has learned the applicable flow or address.

TunJoiner does not translate addresses or ports. If two paths use the same
complete flow details, the path that TunJoiner learned first owns the return
traffic. A learned flow expires after two minutes without matching traffic. The
bounded flow table can also remove older entries. Disconnecting a path removes
all of its learned entries. Use unique source ports or addresses when
independent paths can contact the same destination at the same time.

## TUN configuration

Configuration suggestions travel from the consumer of [Tun](@port/right/Tun) to
every connected default and secondary path. TunJoiner sends the same complete
set of suggested addresses, routes, source routes, MTU, and DNS address to each
path. A path that connects later receives the current suggestions.

When the [Tun](@port/right/Tun) connection is removed, TunJoiner clears its
stored suggestions and sends an empty configuration to the remaining paths. It
does not combine configuration from the default and secondary paths in the other
direction.

The effective packet MTU is the smallest available MTU of the connected paths.
This value updates when paths connect, disconnect, or report an MTU change.

## Connection changes and failures

Removing a default or secondary connection removes its learned flow and address
ownership. A replacement path can then claim those flows and addresses.
Replacing the consumer on [Tun](@port/right/Tun) does not disconnect the joined
paths or remove their learned ownership.

If an attached path has a terminal read, write, or event error, TunJoiner
removes that failed path and requests a replacement through the same graph
connection. Other attached paths continue to work. Recoverable I/O errors do not
remove a path.

The node label shows `waiting for default` when no default path is ready. This
label does not mean that ready secondary paths are stopped. With a ready
default, the label shows `joined default` and the number of ready secondary
paths.
