# SOCKS third-party compatibility

This case ports the external compatibility checks from `socksgo` to the real
`almagestd` SOCKS server node. It uses local targets so it does not depend on
public Internet access.

The graph starts one anonymous SOCKS server with plain TCP and WebSocket
listeners, one username/password SOCKS server, and one anonymous SOCKS server
with TLS-wrapped TCP and WebSocket listeners. The test verifies:

- curl CONNECT requests with SOCKS4, SOCKS4a, SOCKS5, and remote-name SOCKS5;
- curl username/password authentication and rejection of a wrong password;
- Tor `RESOLVE` and `RESOLVE_PTR` commands through `tor-resolve`;
- Gost TCP forwarding through the plain SOCKS5 listener;
- Gost TCP forwarding through the SOCKS5-over-WebSocket listener with Gost's
  default `/ws` path;
- Gost TCP forwarding through TLS and secure WebSocket listeners, including
  Almagest's default `/socks` path;
- Gost UDP-over-TCP forwarding to a local DNS server;
- Gost multiplexed remote BIND forwarding to a local HTTP server.

These checks match the curl, Gost, and Tor compatibility surfaces in
`github.com/asciimoth/socksgo` v0.4.12 while using daemon config and nodes. Gost
and Almagest use different default WebSocket paths: Gost uses `/ws`, while
Almagest uses `/socks`. The plain WebSocket listener is explicitly set to `/ws`;
the secure WebSocket listener keeps the Almagest default and the Gost client
selects it with `path=/socks`.
