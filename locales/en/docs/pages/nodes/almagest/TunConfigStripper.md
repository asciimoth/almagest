Passes a TUN between one provider and one consumer, but blocks all TUN
configuration suggestions. Place it in a TUN path when the provider's
interface settings must remain independent of the connected consumer.

The node blocks the complete configuration suggestion in either direction.
This includes:

- Interface addresses
- Routes and source routes
- MTU and its diagnostic constraint
- DNS server suggestions

The node does not selectively remove one field. Use a different node when you
must keep part of a configuration suggestion.

## Packet and control behavior

IP packets continue to move through the TUN in both directions without
modification. The node does not filter, route, inspect, or copy packet data.
It changes only configuration messages.

TUN availability and replacement changes pass from the provider to the
consumer. If the provider disconnects or withdraws its TUN, the consumer also
loses access to that TUN. When the provider supplies a replacement, the node
supplies the replacement to the consumer.

Requests for the current TUN pass through the node. This lets either side
request current state after a connection or reconnection. Other
non-configuration control messages also pass in both directions.

Each side accepts one TUN connection. The node has no settings. Its label is
`stripping config`.
