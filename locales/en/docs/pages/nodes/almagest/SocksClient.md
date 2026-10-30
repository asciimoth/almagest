Provides a Network that sends supported TCP and UDP operations through a
[SOCKS](https://en.wikipedia.org/wiki/SOCKS) proxy. Connect the network used to
reach the proxy to [out](@port/right/out). Connect clients or tunnel nodes to
the [Network](@port/left/Network) output. The node does not bypass the proxy for
loopback or local addresses.

## Use with a GOST server

### SOCKS over [WebSocket](https://en.wikipedia.org/wiki/WebSocket)

Start a [GOST](https://gost.run/en/) SOCKS5-over-WebSocket server:

```sh
gost -L "socks5+ws://user:pass@:8080?notls=true"
```

Set this node's [Proxy URL](@port/left/Proxy%20URL) to:

```text
socks5+ws://user:pass@gost.example:8080/ws
```

Connect a Network that can reach `gost.example` to [out](@port/right/out). GOST
and this client both use `/ws` as their default WebSocket path. If the GOST
listener uses another path, put the same path after the port in
[Proxy URL](@port/left/Proxy%20URL).

For secure WebSocket, start GOST and set [Proxy URL](@port/left/Proxy%20URL) as
follows:

```sh
gost -L "socks5+wss://user:pass@:8443?notls=true"
```

```text
socks5+wss://user:pass@gost.example:8443/ws
```

Select a **TLS config** that trusts the GOST server certificate and verifies its
server name. The GOST `notls=true` option disables its extra SOCKS negotiation
encryption; it does not disable WS or WSS transport encryption.

### Plain SOCKS5

Start a plain GOST SOCKS5 server:

```sh
gost -L "socks5://user:pass@:1080?notls=true"
```

Set [Proxy URL](@port/left/Proxy%20URL) to
`socks5://user:pass@gost.example:1080`. Remove `user:pass@` from both values to
use the server without authentication. Add `?gost` to the Almagest URL when you
need the GOST UDP-over-TCP or multiplexed BIND extensions. Standard CONNECT,
BIND, and UDP ASSOCIATE do not require this option. See the
[GOST SOCKS documentation](https://gost.run/en/tutorials/protocols/socks/) for
the corresponding GOST server options.

## [Proxy URL](@port/left/Proxy%20URL)

The default [Proxy URL](@port/left/Proxy%20URL) is `socks5://127.0.0.1:1080`. If
the URL does not contain a port, port 1080 is used. These schemes are supported:

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
encrypted [Network](@port/left/Network) output, but does not prevent a plain
SOCKS connection. The `secure` URL option does not replace the selected TLS
config.

## DNS

The network connected to [out](@port/right/out) resolves the proxy server name.
SOCKS5 and SOCKS4a send target hostnames to the proxy, so the proxy resolves
them. SOCKS4 resolves target hostnames with the local process resolver before it
sends the request. It does not use graph DNS or the resolver from
[out](@port/right/out) for this step.

Explicit forward and reverse lookups are disabled by default. Add `?tor` to use
the
[Tor RESOLVE and RESOLVE_PTR extensions](https://spec.torproject.org/socks-extensions.html)
through a compatible SOCKS5 server. Other record lookups, such as MX, SRV, TXT,
and CNAME, are not supported. The `tor` option is not required for normal SOCKS5
connections to a hostname.

## UDP and listeners

Normal SOCKS5 UDP uses UDP ASSOCIATE. The proxy selects the relay address, and a
requested local listen address is not preserved. Add `?gost` to use the GOST
UDP-over-TCP tunnel and multiplexed BIND extensions. Use this mode when a UDP
application, such as [WireGuard](@docs/node/almagest/WireGuard), needs a stable
configured endpoint port.

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

An optional [Proxy URL](@port/left/Proxy%20URL) connection can change the URL at
run time. While it is connected, the [Proxy URL](@port/left/Proxy%20URL) field
is read-only. An empty value restores the default URL. The
[Network](@port/left/Network) output is available only when
[out](@port/right/out) is ready and the URL and applicable **TLS config** are
valid.
