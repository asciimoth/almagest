# Shadowsocks reference compatibility

This case runs inside the normal Docker e2e container. Debian's independent
`shadowsocks-libev` implementation provides `ss-local` and `ss-server`.

It sends a local HTTP request from `ss-local` through the Almagest Shadowsocks
server, then sends another request from the Almagest client through `ss-server`.
Both requests use AES-256-GCM and local TCP targets. All proxy and target
addresses remain inside the container. It also sends one first-attempt request
through an Almagest client and server linked in the same graph after their nodes
report ready.

For UDP, `ss-tunnel` sends a DNS request through the Almagest server. Gost
forwards another local DNS request through Almagest's SOCKS and Shadowsocks
clients to the reference `ss-server`.
