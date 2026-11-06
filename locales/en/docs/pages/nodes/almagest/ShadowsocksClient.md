# Shadowsocks client

This node provides a Network that sends TCP connections and UDP packets through
one Shadowsocks server. Connect the transport Network to [out](@port/right/out)
and connect users of the proxy to [Network](@port/left/Network). The transport
Network must be able to reach the Shadowsocks server. For a local example, load
**Shadowsocks client and server** from **Snapshots → Examples**.

The node opens its connection to the Shadowsocks server only through `out`.
When `out` has no Network, the node does not publish its Network or open a host
socket. A destination such as `127.0.0.1` also goes through the Shadowsocks
server. To use the host network as transport, connect an
[OutNetwork](@docs/node/almagest/OutNetwork) node to `out` yourself.

## Configuration

- **Server address:** Server `host:port` reached through `out`. Default:
  `127.0.0.1:8388`.
- **Cipher:** Classic AEAD cipher. It must match the server. Default:
  `chacha20-ietf-poly1305`.
- **Password:** Shared password used when the selected secret is unavailable.
- **Password secret:** Optional stored secret. Its value takes priority over
  **Password** when available.
- **TCP:** Allow TCP connections through the server. Enabled by default.
- **UDP:** Allow UDP packets through the server. Enabled by default.

Supported ciphers are `chacha20-ietf-poly1305`, `aes-128-gcm`, `aes-192-gcm`,
and `aes-256-gcm`. The client does not support Shadowsocks 2022 ciphers or
SIP003 plugins. The effective password must be nonempty. Enable TCP, UDP, or
both. The client does not publish a Network when its config is invalid or its
effective password is empty.

TCP needs TCP dialing on `out`; UDP needs UDP packet dialing on `out`. The
client sends destination hostnames inside Shadowsocks requests so that the
server's destination Network can resolve them. The client Network does not
provide a separate DNS lookup service. It also does not accept incoming TCP
connections, bind a fixed UDP listening address, or provide multicast UDP.
