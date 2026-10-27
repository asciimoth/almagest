LocalProxy provides a [SOCKS](https://www.rfc-editor.org/rfc/rfc1928) proxy to
applications on the daemon host. Client connections use the system-local
network. The requested connections, name lookups, and remote listeners use the
connected [proxy](@port/right/proxy). Use this node to give a local application
access to a VPN, tunnel, proxy chain, or test network from an ordinary SOCKS
client.

The plain SOCKS listener supports SOCKS4, SOCKS4a, and SOCKS5. Its default
address is `127.0.0.1:1080`. An empty host also uses `127.0.0.1`. Set the port
to `0` to disable this listener.

The optional SOCKS-over-WebSocket listener uses plain
[WebSocket](https://www.rfc-editor.org/rfc/rfc6455) transport. Set **SOCKS+WS
port** to a nonzero value to enable it. The default path is `/socks`; a path
without a leading slash gets one automatically. This listener does not add TLS,
so it provides `ws`, not `wss`. The plain SOCKS and WebSocket listeners can run
together, but they cannot use the same host and port.

## Authentication and access

With no entries in **Users**, the proxy permits connections without
authentication. Add users to require username and password authentication. Empty
usernames are ignored. More than one password can use the same username.

Use **Include clients** or **Exclude clients** to restrict which local
applications and users can connect. The two rule lists are mutually exclusive:

- With include rules, LocalProxy accepts a client when at least one rule
  matches. It rejects all other clients.
- With exclude rules, LocalProxy rejects a client when at least one rule
  matches. It accepts all other clients.
- With both lists empty or absent, LocalProxy accepts clients as it did before
  client filtering was available.

The available rule types depend on the operating system and its current Sysnet
capabilities. Use the completion controls in the node menu to select a supported
type and value. For example, Linux can match process command names, executable
paths, users, and user IDs. Windows can match executable paths and process IDs.
The rules apply before the SOCKS or WebSocket handshake. SOCKS username and
password authentication, when configured, applies after the client rule.

Client ownership lookup is best effort. It can fail when a process exits during
lookup, when the operating system does not expose enough socket ownership data,
or when access to process information is restricted. LocalProxy rejects the
connection when a lookup fails while client filtering is configured. Do not use
client rules as the only security control for an untrusted listener. Keep the
listener on loopback and use SOCKS authentication where appropriate.

Each user can select a **Password secret**. If the secret is available to this
node, its value replaces the inline **Password** while the proxy runs. The
secret value is not copied into the menu or saved node configuration. If the
secret is unavailable or deleted, the inline password becomes active again. The
inline password itself is saved. If you leave it empty, an empty password
becomes valid when the secret is unavailable, so choose the fallback
deliberately. A secret update or rename restarts the listeners and closes active
client sessions.

Do not expose an unauthenticated listener to an untrusted network.

On Linux, the listener uses the configured host address. `127.0.0.1` limits
access to the daemon host. A wildcard or LAN address can make the proxy
reachable from other devices, subject to the host firewall and routing.

On Windows, the system-local network is loopback-only. A wildcard address is
changed to a loopback address, and a non-loopback host is rejected. Thus,
LocalProxy cannot provide a LAN-facing listener on Windows.

## Proxy commands

All command checkboxes are enabled by default. Disable commands that clients do
not need:

- **CONNECT** opens a TCP connection through the connected
  [proxy](@port/right/proxy).
- **BIND** opens a TCP listener through the connected
  [proxy](@port/right/proxy).
- **UDP ASSOC** relays SOCKS5 UDP traffic. The local UDP relay uses a port
  selected by the system; a client cannot request a fixed relay port.
- **Tor RESOLVE** and **Tor RESOLVE_PTR** provide the
  [Tor name-resolution extensions](https://spec.torproject.org/socks-extensions.html).
- **Gost MBIND** and **Gost UDPTUN** provide the corresponding
  [Gost](https://gost.run/en/tutorials/protocols/socks/) extensions.

For UDP operations, destination traffic uses the connected
[proxy](@port/right/proxy). The local UDP relay for **UDP ASSOC** stays on the
system-local network. Use **Gost UDPTUN** when an application needs a stable,
externally configured UDP port, such as a
[WireGuard](@docs/node/almagest/WireGuard) peer with a fixed endpoint.

The proxy listeners can start without a connected [proxy](@port/right/proxy),
but all proxy requests then fail. Connecting, disconnecting, or replacing the
[proxy](@port/right/proxy) restarts the listeners. Applying menu changes,
including client rules, also restarts them. A restart closes active client
sessions. If configured matchers cannot be built, the listener stays unavailable
instead of starting without the requested filter. The **Worker** log shows the
active listener addresses and startup errors.
