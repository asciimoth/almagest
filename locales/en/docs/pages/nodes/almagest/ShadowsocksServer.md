# Shadowsocks server

This node accepts classic AEAD Shadowsocks TCP and UDP traffic. Connect
[listen](@port/right/listen) to the Network on which clients can reach the
server. Connect [proxy](@port/right/proxy) to the Network used for destination
connections and name resolution. Both links are required. The server does not
start when either link has no Network. For a local example, load **Shadowsocks
client and server** from **Snapshots → Examples**.

The server opens listening sockets only through `listen`. It opens destination
TCP connections and UDP sessions only through `proxy`. Use different Networks
when clients and destinations need different routes. To make either route use
the host network, connect an [OutNetwork](@docs/node/almagest/OutNetwork) node
to that port yourself.

## Configuration

- **Listen address:** `host:port` bound on `listen`. Default: `127.0.0.1:8388`.
  Clients need a reachable address with this port.
- **Cipher:** Classic AEAD cipher. It must match the clients. Default:
  `chacha20-ietf-poly1305`.
- **Password:** Shared password used when the selected secret is unavailable.
- **Password secret:** Optional stored secret. Its value takes priority over
  **Password** when available.
- **TCP:** Open a TCP listener on `listen`. Enabled by default.
- **UDP:** Open a UDP listener on `listen`. Enabled by default.

Supported ciphers are `chacha20-ietf-poly1305`, `aes-128-gcm`, `aes-192-gcm`,
and `aes-256-gcm`. Shadowsocks 2022 ciphers and SIP003 plugins are not
supported. The effective password must be nonempty. Enable TCP, UDP, or both.
The server does not start with an invalid config or an empty effective password.

TCP needs TCP listening on `listen` and TCP dialing on `proxy`. UDP needs UDP
packet listening on `listen` and UDP packet dialing on `proxy`. The destination
Network handles destination names supplied by clients. The server rejects
repeated valid AEAD salts within a bounded replay window.

The server accepts up to 1024 concurrent TCP connections and 1024 UDP sessions.
It closes excess TCP connections and drops excess UDP packets. TCP handshakes
and destination connection attempts have 10-second limits. Idle UDP sessions
close after 60 seconds.
