# DnsServer

`almagest/DnsServer` exposes DNS requests received on a UDP listener to an
`almagest/dns` resolver chain.

Ports:

- `Network` is a right-directed `almagest/network` port. The node listens for
  UDP DNS packets through the received network.
- `DNS` is a right-directed `almagest/dns` port. Incoming wire DNS requests are
  forwarded to the received resolver.

Menu configuration:

- `Network` selects `udp`, `udp4`, or `udp6`.
- `Host` is the local bind host.
- `Port` is the local bind port.

Changing listen configuration or the network input restarts the packet
listener. Changing the DNS input attaches or detaches the upstream resolver on
the live `gonnect/dns.Server` without reopening the listener.
