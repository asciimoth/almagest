Spoofer converts [IPv4](https://www.rfc-editor.org/rfc/rfc791) and
[IPv6](https://www.rfc-editor.org/rfc/rfc8200)
[TCP](https://www.rfc-editor.org/rfc/rfc9293) or
[UDP](https://www.rfc-editor.org/rfc/rfc768) flows from [Tun](@port/left/Tun)
into new connections through [Network](@port/right/Network). It acts as the
requested destination on the TUN side and returns replies as if they came from
that destination.

Connect the intercepted packet path to [Tun](@port/left/Tun) and the required
outbound path to [Network](@port/right/Network). Spoofer starts only when both
are ready. It has no separate traffic output because replies go back through the
same TUN connection.

This is a transparent proxy, not IP packet forwarding. Spoofer terminates each
TCP or UDP flow and creates a new flow to the destination IP address and port
from the intercepted packet. The Network path selects the source address and
source port of that new flow. It does not receive the client's original source
address or source port.

There is no spoofing rule syntax and no address rewrite field. To select or
change the outbound path, put a routing node in the
[Network](@port/right/Network) path. That router sees a numeric destination
because TUN packets do not contain the hostname that the client resolved.

## Protocol behavior

Spoofer supports TCP and UDP over IPv4 and IPv6.

For TCP, it first opens the connection through [Network](@port/right/Network).
It completes the intercepted client's handshake only after the outbound
connection is ready. A failed outbound connection resets the intercepted flow.
Connection setup has a 30-second limit.

For UDP, Spoofer opens an unconnected packet socket for each flow. An inactive
UDP flow closes after two minutes. A reply is accepted when it comes from the
target IP address, even if it comes from a different source port. Replies from a
different IP address are discarded.

Other IP protocols are not forwarded through [Network](@port/right/Network). In
particular, the [ICMP](https://www.rfc-editor.org/rfc/rfc792) settings control
messages made by the TUN-facing network stack; they do not make ICMP an outbound
Network flow. Use a different TUN transport when the backend must carry
arbitrary IP packets.

Spoofer does not decrypt or inspect application payloads. TCP and UDP payloads
are copied between the two flows.

## Settings

The default **MTU** is 1,500 bytes. Set it to a value that the connected TUN
path supports. **Queue length** is the packet queue capacity and defaults to
1,024. A larger queue can absorb short traffic bursts but uses more memory and
can add latency.

The remaining network and TCP settings apply to the TUN-facing connection. They
do not change the separate connection made by the selected Network:

- **TTL** sets the default IPv4 TTL and IPv6 hop limit. Zero uses the built-in
  default. Values from 1 through 255 set an explicit value.
- **ICMP burst** is the number of ICMP messages that the rate limiter can send
  at once. **ICMP limit** is the sustained rate in messages per second. Zero
  keeps the built-in defaults.
- **Disable [TCP SACK](https://www.rfc-editor.org/rfc/rfc2018)**, **Disable
  Nagle**, and **Disable TCP receive autotune** change the named TCP behaviors.
  Disable them only for a tested compatibility or latency requirement.
- **TCP send buffer**, **TCP receive buffer**, and **TCP forward window** are
  byte counts. Zero uses the built-in value.
- **TCP congestion** selects the congestion-control algorithm, such as `cubic`
  or `reno`. An empty value uses the built-in algorithm. An unsupported name
  prevents Spoofer from starting.
- **TCP keepalive** enables probes on the intercepted TCP connection. **TCP
  keepalive idle** and **TCP keepalive interval** are in seconds. **TCP
  keepalive count** is the maximum number of unanswered probes. Zero values use
  the built-in values.
- **TCP forward attempts** limits simultaneous TCP connection attempts that are
  waiting for an outbound connection. Zero uses the built-in limit.

MTU and queue length must be positive. TTL must be from 0 through 255. ICMP and
TCP numeric settings cannot be negative. Applying valid settings restarts
Spoofer, so active TCP and UDP flows can close.

## Safety

Spoofer accepts TCP and UDP traffic for all destination addresses that arrive on
[Tun](@port/left/Tun). Restrict traffic before it reaches this node, or apply
policy on the Network path. Do not use the original TUN source address as an
authentication signal on the backend because that address is not preserved.

The separate outbound connections can enter the host's routing system again.
Make sure that they do not return to the same TUN path, or a routing loop can
occur. Also account for the UDP reply rule: any source port on the target host
can send a reply into the intercepted flow.
