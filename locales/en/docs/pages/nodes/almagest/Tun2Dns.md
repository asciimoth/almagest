Tun2Dns receives raw DNS packets from **Tun**, sends their DNS messages to the
connected **DNS** resolver, and writes raw response packets back to the same
TUN.

Use Tun2Dns as the DNS endpoint of a routed TUN path. A common layout sends UDP
destination port 53 from [TunSplitter](@docs/node/almagest/TunSplitter) to Tun2Dns and sends other traffic to
different TUN nodes. Tun2Dns consumes the TUN packets that it receives. It
does not pass ignored packets to another TUN.

Tun2Dns does not supply TUN addresses, routes, or a DNS-server address. The
TUN path before this node must select which packets reach it.

## Accepted packets

Tun2Dns accepts packets with all these properties:

- The packet is a complete IPv4 or IPv6 packet, not an Ethernet frame.
- The IP payload is UDP.
- The UDP destination port is `53`.
- The UDP payload is a valid DNS query, not a DNS response.
- The packet is not fragmented and its length fits the read buffer. The buffer
  is 1500 bytes, or the TUN-reported MTU when that MTU is larger.
- The UDP checksum is valid. IPv4 can omit the UDP checksum, but IPv6 cannot.

Some IPv6 extension headers are accepted, but an IPv6 Fragment header is not.
TCP DNS, DNS over TLS, DNS over HTTPS, and QUIC do not match these rules.
Tun2Dns does not reassemble requests or fragment responses. Keep DNS packets
within the usable TUN MTU.

Malformed packets, fragments, packets for another port, DNS responses, and
invalid DNS messages are ignored. Tun2Dns does not send an error packet for
them.

## Response addresses and ports

The request can use any IPv4 or IPv6 source and destination address. Tun2Dns
does not require a fixed virtual DNS address and does not check that the
destination belongs to the local host. This permits interception of DNS sent
to an arbitrary server address.

For each answer, Tun2Dns exchanges the request endpoints:

- The response source address is the request destination address.
- The response destination address is the request source address.
- The response source port is `53`.
- The response destination port is the request source port.

The response uses the original DNS transaction ID. Tun2Dns creates new IP and
UDP headers and calculates their checksums.

## Request timeout and errors

**Request timeout (ms)** limits one request to the connected DNS resolver. Set
the value to `0` to use the default timeout of 5 seconds. A negative value
is invalid.

If the resolver returns an error before the timeout, Tun2Dns replies with
`SERVFAIL`. If the request reaches its timeout, Tun2Dns sends no response. The
DNS client can then retry according to its own policy.

Tun2Dns can forward up to 256 requests at the same time. When all request
slots are in use, TUN reading waits for a free slot. The timeout for the
request that is waiting continues during this time.

The node processes packets only when both inputs are ready and its timeout is
valid. Without a ready DNS resolver, received TUN packets get no response.
Changing the DNS resolver or timeout cancels requests that are still waiting.

If the TUN stops working, the node shows `TUN error` and requests a replacement
from the connected TUN provider. It resumes packet handling when a replacement
becomes ready.
