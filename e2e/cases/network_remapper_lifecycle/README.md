# Network Remapper Lifecycle

This hermetic case runs two local SOCKS proxies through one `Remapper`. The
remapper can use either of two isolated `MockNetwork` providers. Each provider
has HTTP servers that make the selected network and destination port visible in
the response body.

Assertions cover these situations:

- two consumers receive independent wrappers from one remapper output;
- a live rule change replaces both wrappers and changes the destination;
- an invalid rule reports invalid config but keeps the last valid wrappers
  usable;
- repeated valid rule changes converge on the latest destination;
- removing one output link does not affect the other consumer;
- the removed output recovers after its link is restored;
- removing the input makes all existing outputs unusable; and
- attaching a different input rebuilds both outputs on the new provider.

The case uses only userspace mock networks and does not require privileged
Docker networking.
