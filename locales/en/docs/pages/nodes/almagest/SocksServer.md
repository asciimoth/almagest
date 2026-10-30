Runs a [SOCKS4, SOCKS4a, and SOCKS5](https://en.wikipedia.org/wiki/SOCKS) proxy.
The [listen](@port/right/listen) Network accepts client connections. The
[proxy](@port/right/proxy) Network performs the connections, listeners, UDP
operations, and name lookups that clients request.

The [listen](@port/right/listen) Network is required. The server can start
without a [proxy](@port/right/proxy) Network, but client commands then fail.
This lets clients receive a SOCKS protocol error instead of only a connection
error.

## Use with a GOST client

### SOCKS over [WebSocket](https://en.wikipedia.org/wiki/WebSocket)

Set **SOCKS+WS host** and a nonzero **SOCKS+WS port**. The default Almagest
WebSocket path is `/socks`, but GOST defaults to `/ws`. Therefore, specify the
Almagest path in the GOST forwarding URL:

```sh
gost -L "http://127.0.0.1:8080" \
  -F "socks5+ws://user:pass@almagest.example:1081?path=/socks&notls=true"
```

This command provides a local HTTP proxy at `127.0.0.1:8080` and sends its
traffic through this node. Configure the same `user` and `pass` in **Users**.
Remove `user:pass@` when the node has no users.

Alternatively, set **SOCKS+WS path** to `/ws`. GOST can then use its default
path, so remove `path=/socks` from the forwarding URL. Keep `notls=true`; it
disables GOST's extra SOCKS negotiation encryption, which this node does not
implement.

For WSS, route the [listen](@port/right/listen) Network through a
[TlsWrapper](@docs/node/almagest/TlsWrapper) and use this GOST chain:

```sh
gost -L "http://127.0.0.1:8080" \
  -F "socks5+wss://user:pass@almagest.example:1081?path=/socks&notls=true"
```

Configure GOST to trust the wrapper's certificate. Keep the explicit
`path=/socks` unless you changed **SOCKS+WS path** to `/ws`.

### Plain SOCKS5

For the default plain listener at port 1080, use this GOST chain:

```sh
gost -L "http://127.0.0.1:8080" \
  -F "socks5://user:pass@almagest.example:1080?notls=true"
```

Keep **CONNECT** enabled for normal proxy traffic. Keep **Gost UDPTUN** enabled
for GOST UDP-over-TCP forwarding and **Gost MBIND** enabled for GOST multiplexed
remote forwarding. See the
[GOST SOCKS documentation](https://gost.run/en/tutorials/protocols/socks/) for
GOST listener and forwarding options.

## Listeners

The plain SOCKS listener defaults to `127.0.0.1:1080`. An empty host also uses
`127.0.0.1`. Set **SOCKS port** to `0` to disable it.

The optional SOCKS-over-WebSocket listener is disabled by default. Set
**SOCKS+WS port** to a nonzero value to enable it. Its default path is `/socks`;
the node adds a leading slash if necessary. The plain and WebSocket listeners
can run together, but they cannot use the same host and port.

The node does not add TLS. Route the [listen](@port/right/listen) Network
through a [TlsWrapper](@docs/node/almagest/TlsWrapper) when clients need SOCKS
over TLS or WSS. The WebSocket listener does not restrict the browser `Origin`,
so do not rely on origin checks for access control.

The listener address exists in the connected [listen](@port/right/listen)
Network. With an [OutNetwork](@docs/node/almagest/OutNetwork), `127.0.0.1`
limits access to the daemon host. A wildcard or LAN address can expose the proxy
to other devices, subject to the operating-system firewall. A virtual or
tunneled [listen](@port/right/listen) Network follows that Network's address
rules. Platform behavior comes from the attached [listen](@port/right/listen)
Network.

## Authentication

With no entries in **Users**, the server permits clients without authentication.
Add users to require credentials. Empty usernames are ignored. Multiple entries
can use the same username with different passwords.

SOCKS5 supports username and password authentication. SOCKS4 and SOCKS4a send a
username but no password. When users are configured, a SOCKS4 or SOCKS4a client
can authenticate only if a matching user has an empty password. Use SOCKS5 when
password authentication is required.

Each user can select a **Password secret**. If the secret is available to this
node, its value replaces the inline **Password** while the server runs. The
secret value is not copied into the menu or saved node configuration. If the
secret is unavailable or deleted, the inline password becomes active again. The
inline password itself is saved. If you leave it empty, an empty password
becomes valid when the secret is unavailable, so choose the fallback
deliberately.

A secret update or a valid menu change restarts both listeners and closes active
client sessions.

## Commands

All command options are enabled by default. Disable commands that clients do not
need:

- **CONNECT** opens a TCP connection through the [proxy](@port/right/proxy)
  Network. SOCKS4, SOCKS4a, and SOCKS5 support it.
- **BIND** opens a one-connection TCP listener through the
  [proxy](@port/right/proxy) Network. SOCKS4, SOCKS4a, and SOCKS5 support it.
- **UDP ASSOC** relays UDP for SOCKS5 clients.
- **Tor RESOLVE** performs the
  [Tor forward-lookup extension](https://spec.torproject.org/socks-extensions.html)
  through the [proxy](@port/right/proxy) Network. SOCKS4, SOCKS4a, and SOCKS5
  support it.
- **Tor RESOLVE_PTR** performs the Tor reverse-lookup extension for SOCKS5.
- **Gost MBIND** opens a multiplexed TCP listener through the
  [proxy](@port/right/proxy) Network. SOCKS4, SOCKS4a, and SOCKS5 support it.
- **Gost UDPTUN** carries UDP in a
  [Gost](https://gost.run/en/tutorials/protocols/socks/) tunnel for SOCKS5.

The exposure of **BIND** and **Gost MBIND** listeners depends on the
[proxy](@port/right/proxy) Network. Disable them unless clients need inbound
listeners.

## UDP behavior

For **UDP ASSOC**, the server creates the client-facing UDP relay in the
[listen](@port/right/listen) Network. It uses the host of the SOCKS control
connection and an available port. The client receives that selected port in the
SOCKS5 reply; the protocol does not let the client reserve a fixed relay port.
Destination UDP traffic uses the [proxy](@port/right/proxy) Network. The
association closes when its control connection closes or after three minutes
without activity.

A plain SOCKS-over-WebSocket client still needs network access to this separate
UDP relay. Use **Gost UDPTUN** when the client must carry UDP in the SOCKS
connection, or when an application needs a stable externally configured UDP
endpoint, such as a [WireGuard](@docs/node/almagest/WireGuard) peer with a fixed
endpoint. **UDP ASSOC** and **Gost UDPTUN** are not available on WSS
connections.

## Operation

The server uses a fixed 10-second SOCKS handshake timeout. The **Worker** log
shows listener addresses, restarts, and startup failures.

Changing or replacing either [listen](@port/right/listen) or
[proxy](@port/right/proxy) Network restarts the server and closes active client
sessions. If a listener cannot start, the server waits briefly and tries again.

## Graph example

The listener Network carries connections between SocksClient and SocksServer.
The proxy Network carries the operations that SocksServer makes for the client.
These Networks can be different providers.

```graph
{
  "Application": {
    "Class": "almagest/HttpClient",
    "Pos": "{\"x\":31,\"y\":53}",
    "Links": ["Network -> [SOCKS client] Network"],
  },
  "SOCKS client": {
    "Class": "almagest/SocksClient",
    "Pos": "{\"x\":245,\"y\":53}",
    "Links": ["out -> [SOCKS carrier] Network"],
  },
  "SOCKS carrier": {
    "Class": "almagest/MockNetwork",
    "Pos": "{\"x\":493,\"y\":53}",
  },
  "SOCKS server": {
    "Class": "almagest/SocksServer",
    "Pos": "{\"x\":245,\"y\":187}",
    "Links": [
      "listen -> [SOCKS carrier] Network",
      "proxy -> [Outside network] Network",
    ],
  },
  "Outside network": {
    "Class": "almagest/OutNetwork",
    "Pos": "{\"x\":493,\"y\":187}",
  },
}
```
