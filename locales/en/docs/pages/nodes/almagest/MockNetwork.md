Provides a private, in-memory network for tests and diagnostics. Connections and
listeners in this network do not use the operating-system network and cannot
reach the internet or other external hosts.

Connect this [Network](@port/left/Network) to the client and server nodes that
you want to test together. TCP and UDP communication can use IPv4 or IPv6
loopback. Each MockNetwork is separate, so nodes that use different MockNetwork
nodes cannot communicate with each other.

## Host addresses

**Allow any host** is enabled by default. With this option, connection and
listener addresses that contain a non-loopback hostname or IP address are mapped
to loopback. The port stays the same. For example, a server that listens on
`service.example:8080` can receive a connection to that address inside the same
MockNetwork. No traffic goes to `service.example`.

Disable **Allow any host** when a test must accept only `localhost` and loopback
IP addresses. The change applies to new connections and listeners.

The option does not provide DNS records. Built-in name lookup resolves
`localhost` and loopback IP addresses only. Use DNS nodes when a test needs
other DNS responses.

## Limits

Virtual port numbers are private to this MockNetwork and do not conflict with
operating-system ports. A listener port value of `0` selects an available
virtual port.

The node does not simulate latency, packet loss, bandwidth limits, or access to
a real network.
