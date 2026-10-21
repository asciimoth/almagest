TlsWrapper adds TLS to both client and server TCP operations on a Network.
Place it between a Network consumer and the Network that supplies the actual
connections.

- For a TCP dial, TlsWrapper connects to the requested destination and
  completes a TLS client handshake. The consumer uses a normal plaintext
  stream, while the underlying Network carries TLS-encrypted data.
- For a TCP listener, TlsWrapper accepts TLS from each remote client and gives
  the decrypted connection to the listening consumer as a normal TCP stream.

For example, an HTTP client can use an `http://` URL through TlsWrapper to
reach an HTTPS server. A plaintext HTTP server can listen through TlsWrapper
and appear as an HTTPS server to remote clients.

TlsWrapper applies this behavior to every TCP dial and TCP listener. It does
not detect whether a connection already uses TLS, and it has no host filter.
If a consumer also enables TLS, the connection has two TLS layers. Use
TlsMitm or TlsTerminator when you need to inspect or remove an existing TLS
layer.

## Client-side TLS

The selected **TLS config** controls outgoing TLS. Use it to configure trusted
certificate authorities, TLS version limits, and a client certificate for
mutual TLS. If no config is selected, the default TLS config applies. If a
named config is removed, the node uses the default config and shows a warning.

**Client server name** overrides the certificate-verification name and the SNI
name from the selected TLS config. If this field is empty, TlsWrapper uses the
server name from the selected config. If both are empty, it uses the host from
the dial destination.

Use **Client ALPN** to replace the ALPN protocol list from the selected TLS
config. Separate values with spaces, commas, semicolons, or new lines. For
example:

```text
h2
http/1.1
```

An empty **Client ALPN** field keeps the list from the selected TLS config.
The TLS handshake fails if server verification fails or if the peers cannot
agree on required TLS settings.

## Server-side TLS

TlsWrapper uses Almagest's local TLS CA to create a server certificate for
each incoming TLS connection. The client must trust the public certificate of
that CA. TlsWrapper does not use the selected TLS config for incoming
connections.

When the client sends SNI, the generated certificate is valid for that name.
When the client sends no SNI, **Server name** supplies the certificate name.
This field cannot be empty. Its default is `localhost`.

**Server ALPN** lists the protocols that the listener can select. Separate
values with spaces, commas, semicolons, or new lines. An empty list disables
ALPN selection. Configure only protocols that the plaintext server can use.

Incoming connections require TLS 1.2 or later. The server side does not
request or verify client certificates, so it does not provide mutual TLS
authentication.

**Leaf TTL seconds** sets the validity period of generated server
certificates. Zero uses 24 hours. A generated certificate never remains valid
after the local CA expires. A negative value is invalid.

Keep the local CA private key protected. Install only its public certificate
in client trust stores. Any person or process that obtains the private key can
create certificates that those clients trust.

## Other Network operations and availability

UDP, packet connections, DNS lookups, interface queries, and other non-TCP
operations pass through without TLS. This means TlsWrapper does not encrypt
UDP or QUIC.

The node supplies its Network only when the connected Network, selected client
TLS config, local CA, server name, and leaf TTL are valid. Both client-side and
server-side settings must be valid even if the consumer uses only dials or
only listeners.

Changing the connected Network, TLS config, local CA, or a menu setting
replaces the supplied Network. Active connections through the previous
configuration can close. TlsWrapper has no operating-system-specific network
integration; platform behavior comes from the connected Network.
