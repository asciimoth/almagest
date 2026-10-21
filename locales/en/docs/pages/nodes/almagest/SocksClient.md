Provides a Network that sends supported TCP and UDP operations through a SOCKS
proxy. Connect the network used to reach the proxy to **out**. Connect clients
or tunnel nodes to the **Network** output. The node does not bypass the proxy
for loopback or local addresses.

## Proxy URL

The default **Proxy URL** is `socks5://127.0.0.1:1080`. If the URL does not
contain a port, port 1080 is used. These schemes are supported:

- `socks5://`, `socks://`, and `socks5h://` select SOCKS5.
- `socks4://` selects SOCKS4.
- `socks4a://` selects SOCKS4a.
- Add `+tls` for SOCKS over TLS, `+ws` for SOCKS over WebSocket, or `+wss` for
  secure WebSocket. For example, use `socks5+wss://proxy.example:443/socks`. The
  default WebSocket path is `/ws`. The built-in
  [SocksServer](@docs/node/almagest/SocksServer) and
  [LocalProxy](@docs/node/almagest/LocalProxy) nodes use `/socks` by default, so
  set this path explicitly when you connect to either node.

SOCKS5 supports TCP and UDP for IPv4 and IPv6. SOCKS4 and SOCKS4a support only
TCP operations that use IPv4. TCP listeners use the SOCKS BIND command. A normal
BIND listener accepts one forwarded connection.

## Authentication and TLS

Put SOCKS5 credentials in the URL, for example
`socks5://alice:password@proxy.example:1080`. SOCKS4 and SOCKS4a use only the
username. When **Password secret** is selected and available, its value replaces
the SOCKS5 password from the URL. The URL must still contain a username. If the
secret is unavailable, the URL password remains in use. A selected secret has no
effect when the URL has no username.

For `+tls` and `+wss`, **TLS config** selects the certificate authorities,
server name, client certificate, and other TLS policy. The default TLS config is
used when no other config is selected. An invalid TLS config prevents the
encrypted **Network** output, but does not prevent a plain SOCKS connection. The
`secure` URL option does not replace the selected TLS config.

## DNS

The network connected to **out** resolves the proxy server name. SOCKS5 and
SOCKS4a send target hostnames to the proxy, so the proxy resolves them. SOCKS4
resolves target hostnames with the local process resolver before it sends the
request. It does not use graph DNS or the resolver from **out** for this step.

Explicit forward and reverse lookups are disabled by default. Add `?tor` to use
the Tor `RESOLVE` and `RESOLVE_PTR` extensions through a compatible SOCKS5
server. Other record lookups, such as MX, SRV, TXT, and CNAME, are not
supported. The `tor` option is not required for normal SOCKS5 connections to a
hostname.

## UDP and listeners

Normal SOCKS5 UDP uses UDP ASSOCIATE. The proxy selects the relay address, and a
requested local listen address is not preserved. Add `?gost` to use the GOST
UDP-over-TCP tunnel and multiplexed BIND extensions. Use this mode when a UDP
application, such as WireGuard, needs a stable configured endpoint port.

TLS and secure WebSocket proxy URLs disable UDP by default. Add `?insecureudp`
to enable it. With normal UDP ASSOCIATE, UDP packets use a separate unencrypted
relay even though the control connection uses TLS. Prefer `?gost&insecureudp`
when UDP must stay in the encrypted proxy stream. The `insecureudp` option is
currently also required to enable GOST UDP with a TLS or secure WebSocket proxy
URL.

Add `?assocprob=false` only when a proxy is incompatible with the normal UDP
control-connection monitor. Disabling the monitor can delay detection when the
proxy closes an association. The `pass` URL option has no effect because this
node always sends supported destination operations through the proxy.

## Dynamic URL input

An optional **Proxy URL** connection can change the URL at run time. While it is
connected, the **Proxy URL** field is read-only. An empty value restores the
default URL. The **Network** output is available only when **out** is ready and
the URL and applicable **TLS config** are valid.
