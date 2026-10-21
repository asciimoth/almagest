Runs a SOCKS4, SOCKS4a, and SOCKS5 proxy. The **listen** Network accepts client connections. The **proxy** Network performs the connections, listeners, UDP operations, and name lookups that clients request.

The listen Network is required. The server can start without a proxy Network, but client commands then fail. This lets clients receive a SOCKS protocol error instead of only a connection error.

## Listeners

The plain SOCKS listener defaults to `127.0.0.1:1080`. An empty host also uses `127.0.0.1`. Set **SOCKS port** to `0` to disable it.

The optional SOCKS-over-WebSocket listener is disabled by default. Set **SOCKS+WS port** to a nonzero value to enable it. Its default path is `/socks`; the node adds a leading slash if necessary. The plain and WebSocket listeners can run together, but they cannot use the same host and port.

The node does not add TLS. Route the listen Network through a TlsWrapper when clients need SOCKS over TLS or WSS. The WebSocket listener does not restrict the browser `Origin`, so do not rely on origin checks for access control.

The listener address exists in the connected listen Network. With an OutNetwork, `127.0.0.1` limits access to the daemon host. A wildcard or LAN address can expose the proxy to other devices, subject to the operating-system firewall. A virtual or tunneled listen Network follows that Network's address rules. Platform behavior comes from the attached listen Network.

## Authentication

With no entries in **Users**, the server permits clients without authentication. Add users to require credentials. Empty usernames are ignored. Multiple entries can use the same username with different passwords.

SOCKS5 supports username and password authentication. SOCKS4 and SOCKS4a send a username but no password. When users are configured, a SOCKS4 or SOCKS4a client can authenticate only if a matching user has an empty password. Use SOCKS5 when password authentication is required.

Each user can select a **Password secret**. If the secret is available to this node, its value replaces the inline **Password** while the server runs. The secret value is not copied into the menu or saved node configuration. If the secret is unavailable or deleted, the inline password becomes active again. The inline password itself is saved. If you leave it empty, an empty password becomes valid when the secret is unavailable, so choose the fallback deliberately.

A secret update or a valid menu change restarts both listeners and closes active client sessions.

## Commands

All command options are enabled by default. Disable commands that clients do not need:

- **CONNECT** opens a TCP connection through the proxy Network. SOCKS4, SOCKS4a, and SOCKS5 support it.
- **BIND** opens a one-connection TCP listener through the proxy Network. SOCKS4, SOCKS4a, and SOCKS5 support it.
- **UDP ASSOC** relays UDP for SOCKS5 clients.
- **Tor RESOLVE** performs the Tor forward-lookup extension through the proxy Network. SOCKS4, SOCKS4a, and SOCKS5 support it.
- **Tor RESOLVE_PTR** performs the Tor reverse-lookup extension for SOCKS5.
- **Gost MBIND** opens a multiplexed TCP listener through the proxy Network. SOCKS4, SOCKS4a, and SOCKS5 support it.
- **Gost UDPTUN** carries UDP in a [Gost](https://gost.run/en/tutorials/protocols/socks/) tunnel for SOCKS5.

The exposure of **BIND** and **Gost MBIND** listeners depends on the proxy Network. Disable them unless clients need inbound listeners.

## UDP behavior

For **UDP ASSOC**, the server creates the client-facing UDP relay in the listen Network. It uses the host of the SOCKS control connection and an available port. The client receives that selected port in the SOCKS5 reply; the protocol does not let the client reserve a fixed relay port. Destination UDP traffic uses the proxy Network. The association closes when its control connection closes or after three minutes without activity.

A plain SOCKS-over-WebSocket client still needs network access to this separate UDP relay. Use **Gost UDPTUN** when the client must carry UDP in the SOCKS connection, or when an application needs a stable externally configured UDP endpoint, such as a WireGuard peer with a fixed endpoint. **UDP ASSOC** and **Gost UDPTUN** are not available on WSS connections.

## Operation

The server uses a fixed 10-second SOCKS handshake timeout. The **Worker** log shows listener addresses, restarts, and startup failures.

Changing or replacing either Network restarts the server and closes active client sessions. If a listener cannot start, the server waits briefly and tries again.
