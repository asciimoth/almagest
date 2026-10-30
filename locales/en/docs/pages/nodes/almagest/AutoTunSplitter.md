Splits one [Tun](@port/left/Tun) connection between output slots. Use this node
when the nodes on the output slots advertise the destination networks that they
can reach. For example, a VPN branch can advertise its private networks while
[slot 1](@port/right/slot%201) provides the default path.

The node learns IPv4 and IPv6 destination prefixes from each
output slot. The most-specific matching prefix selects the slot. If two slots
advertise the same prefix, an address associated with that route can select its
slot. Otherwise, the lower slot number has priority. An address without a route
does not make its slot handle other destinations.

Packets that do not match an advertised prefix go to
[slot 1](@port/right/slot%201). Connect the fallback path to
[slot 1](@port/right/slot%201), even if that path does not advertise a route.
Invalid or incomplete IP packets are dropped.

Each update from an output slot replaces all previously learned information for
that slot. If the connected node stops advertising a route, the next update
removes that route. Disconnecting or replacing a slot connection also removes
its learned routes. The node keeps one unused output slot available as you add
connections, up to 16 slots.

The node merges address, route, source route, and MTU suggestions from all
connected output slots and sends the result to its [Tun](@port/left/Tun) input.
For a plain route, it associates the first valid address of the same IP family
from that slot. This lets an upstream userspace network stack select the correct
source address for each route.

DNS suggestions are not sent through this node because different output slots
can use different DNS services. Use DNS links and DNS nodes when the graph needs
DNS routing, multiple resolvers, caching, or policy.

## Graph example

Use this topology when WireGuard advertises private routes. AutoTunSplitter
sends those packets to WireGuard and sends all other packets to the flow
converter through slot 1. Both branches send their configuration suggestions
back through AutoTunSplitter to the client stack.

```graph
{
  "Client IP stack": {
    "Class": "almagest/VTun",
    "Pos": "{\"x\":31,\"y\":120}",
    "Links": ["Tun -> [Automatic packet router] Tun"],
  },
  "Automatic packet router": {
    "Class": "almagest/AutoTunSplitter",
    "Pos": "{\"x\":245,\"y\":120}",
    "Links": [
      "slot 1 -> [Default flow converter] Tun",
      "slot 2 -> [Private tunnel] Tun",
    ],
  },
  "Default flow converter": {
    "Class": "almagest/Spoofer",
    "Pos": "{\"x\":493,\"y\":53}",
    "Links": ["Network -> [Outside network] Network"],
  },
  "Private tunnel": {
    "Class": "almagest/WireGuard",
    "Pos": "{\"x\":493,\"y\":187}",
    "Links": ["Network -> [Outside network] Network"],
  },
  "Outside network": {
    "Class": "almagest/OutNetwork",
    "Pos": "{\"x\":731,\"y\":120}",
  },
}
```
