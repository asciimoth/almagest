TunCopy makes a point-to-point packet path between two TUN endpoints. A packet
read from **TunA** is written once to **TunB**. A packet read from **TunB** is
written once to **TunA**.

Use TunCopy to connect two TUN providers directly, such as two
[VTun](@docs/node/almagest/VTun) nodes. It is not a packet-mirroring node and
does not make an additional copy for a monitor.

## Attachments and packet handling

Connect one TUN provider to **TunA** and one to **TunB**. Each input accepts one
TUN link. Both providers must supply an endpoint before there is a complete
packet path. The node shows `waiting for TunA`, `waiting for TunB`, or `copying`
to identify its current state. The `copying` label confirms that both endpoints
are present. It does not test end-to-end reachability.

TunA and TunB have the same packet behavior. The names do not set a client,
server, upstream, or downstream role.

TunCopy does not inspect or change IP packets. It preserves the packet bytes and
supports all packet protocols that the attached TUN endpoints can carry.

TunCopy does not fragment packets or negotiate an MTU. Configure compatible MTUs
on both sides. A packet that the destination endpoint cannot accept can cause a
write failure.

## TUN configuration and events

TunCopy transfers packets only. It does not send addresses, routes, source
routes, MTU, or DNS suggestions from one side to the other. Configure each TUN
provider separately, or put configuration-aware TUN nodes elsewhere in the
graph.

The node also does not copy one endpoint's TUN up/down events to the other
endpoint. Its own label reports whether both packet endpoints are available.
TunCopy has no menu settings.

## Detach and error behavior

Removing or replacing one attachment closes its old endpoint and pauses the
complete path. The other attachment stays connected. Packet transfer resumes
when both endpoints are available again.

If an endpoint has a terminal read or write failure, TunCopy removes that
endpoint and requests a replacement through the existing link. The label changes
to the applicable waiting state. A provider that can create a new endpoint can
recover the path without removing and adding the graph link.
