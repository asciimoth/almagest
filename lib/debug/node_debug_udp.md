Provides debug UDP ping-pong nodes for checking packet connectivity through an
`almagest/network` graph.

Add `almagest/UDPPingServer` and `almagest/UDPPingClient`, connect both
`Network` ports to the network path under test, configure matching addresses,
then press `Send ping` on the client. A successful check returns
`pong:<payload>` and sets the client status to `ok`.

Use `Network` to select `udp4` or `udp6`.

Use `Socket mode` to select the UDP socket API used by each node:

- `listen` opens an unconnected UDP socket with `ListenUDP`.
- `dial` opens a connected UDP socket with `DialUDP`.

For connected server mode, set the server remote host and remote port to the
client local address. For connected client mode, set the client local port if
the server also needs to dial back to a known client port.

Example IPv4 unconnected server and connected client:

- Server: `udp4`, `listen`, local `127.0.0.1:24000`.
- Client: `udp4`, `dial`, remote `127.0.0.1:24000`.

Example IPv6 connected on both sides:

- Server: `udp6`, `dial`, local `[::1]:24000`, remote `[::1]:24001`.
- Client: `udp6`, `dial`, local `[::1]:24001`, remote `[::1]:24000`.

This is a debug class. Build the engine with debug classes enabled to make these
nodes available.
