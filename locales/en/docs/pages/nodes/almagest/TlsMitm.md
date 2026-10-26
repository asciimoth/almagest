TlsMitm intercepts selected client-first TLS connections. It presents a locally
generated certificate to the client, decrypts the connection, and creates a
separate TLS connection to the original server through the connected
[Network](@port/right/Network). Place it between a [Network](@port/left/Network)
consumer and its outbound [Network](@port/right/Network) path.

The client must trust Almagest's local TLS CA. TlsMitm supplies its
[Network](@port/left/Network) only when the outbound
[Network](@port/right/Network), local CA, selected TLS config, and menu values
are valid.

TlsMitm applies only to outgoing TCP connections. It passes non-TLS TCP data
through unchanged after it reads the first client bytes. UDP, including QUIC,
listeners, DNS lookups, and other Network operations also pass through
unchanged. It does not change the destination address or select a different
Network path.

## TLS behavior

For an intercepted connection, TlsMitm reads the ClientHello and uses its
visible SNI hostname to generate a server certificate signed by the local CA. It
then makes a new TLS connection to that hostname at the original network
destination. The generated certificate is valid for the SNI hostname.

The selected **TLS config** controls the connection to the real server. Use it
to configure trusted roots, TLS version limits, and a client certificate. The
SNI hostname and the client's offered ALPN protocols replace the server name and
ALPN list in that config. If you do not select a config, the default TLS config
applies. TlsMitm completes the server connection first and then offers its
selected ALPN protocol to the client.

The client-facing and server-facing connections are separate TLS sessions. They
can use different TLS versions, cipher suites, and session state. A client
certificate that an application sends cannot pass through the interception.
Configure an upstream client certificate in **TLS config** when the real server
requires mutual TLS.

The client connection fails when interception is selected and one of these
conditions applies:

- The ClientHello has no visible SNI hostname.
- The ClientHello signals Encrypted Client Hello (ECH). TlsMitm cannot decrypt
  the inner hostname.
- The ClientHello is malformed or exceeds the sniff buffer limit.
- The real server's TLS handshake fails, including certificate verification.
- The client does not trust the local CA or uses certificate pinning.

A connection with a readable ClientHello that the filter excludes passes through
unchanged. This permits ECH, pinned applications, mutual TLS managed by the
application, or other traffic that must keep its original end-to-end TLS
session.

TlsMitm opens the outbound TCP connection before it can inspect the first client
bytes. It does not set an inspection timeout. A server-first protocol, or a
client that does not send data, can remain waiting. Set a connection deadline
and close the connection after the timeout when this traffic is possible.

## Certificate settings

**Leaf TTL seconds** sets the validity period of generated server certificates.
Zero uses 24 hours. A generated certificate never remains valid after the local
CA expires. A negative value is invalid.

**Sniff buffer bytes** limits how much of the first ClientHello TlsMitm can
inspect. Zero uses 65,536 bytes. A smaller value reduces memory use but can
reject a valid ClientHello with many extensions. A negative value is invalid.

Keep the local CA private key protected. Install only its public certificate in
client trust stores. Any person or process that obtains the private key can
create certificates that those clients trust. Limit CA trust to the devices and
applications that require interception.

## Interception filter

**Filter mode** controls which TLS connections are intercepted:

- `off` intercepts every TLS connection that has visible SNI and no ECH.
  **Filter rules JSON** must be `[]` in this mode.
- `inclusive` intercepts a connection when at least one rule matches. With no
  rules, it passes all connections through unchanged.
- `exclusive` passes a connection through unchanged when at least one rule
  matches. With no rules, it intercepts all eligible TLS connections.

Rules are JSON objects in an array. Rules are alternatives: a connection matches
when any object matches. In one object, configured fields must all match.
Multiple values in one field are alternatives. An omitted or empty field matches
any value.

This configuration intercepts only `example.com` and its subdomains:

```json
[
  {
    "sni_hosts": ["example.com", "*.example.com"],
    "sni_available": "required",
    "sni_encrypted": "forbidden"
  }
]
```

Use this rule with `inclusive` mode. The available fields are:

- `networks` matches the connection type, such as `tcp`, `tcp4`, `tcp6`, or a
  whole-value pattern such as `tcp*`.
- `conn_srcs` and `conn_dsts` match the requested or actual socket address.
  Values can be a host, `host:port`, IP address, `IP:port`, CIDR subnet, or host
  pattern.
- `sni_hosts` matches the visible SNI hostname and is not case-sensitive.
- `alpns` matches any protocol offered by the client. ALPN matching is
  case-sensitive.
- `sni_available` and `sni_encrypted` accept `any`, `required`, or `forbidden`.
- `tls_versions` contains numeric TLS version values: `769` for TLS 1.0, `770`
  for TLS 1.1, `771` for TLS 1.2, and `772` for TLS 1.3. It matches any version
  offered by the client, not the version that the server finally selects.

Network, SNI, and ALPN patterns match the complete value. `*` matches any byte
sequence, `?` matches one byte, and bracket expressions such as `[0-9]` match
one byte from a set. SNI matching ignores case. Matching for Network and ALPN is
case-sensitive.

Filtering is a choice between interception and unchanged pass-through. It is not
an access-control rule. Put a firewall or routing policy in the Network path
when traffic must be blocked or sent to a different destination.

## Privacy and operational risks

TlsMitm handles decrypted application data in memory while it copies data
between the two TLS sessions. It does not record or expose that plaintext, but
the interception removes the end-to-end TLS boundary inside the Almagest
process. Use it only where you have permission to inspect the traffic and can
protect the host.

Do not disable server certificate verification in the selected TLS config unless
you accept the resulting impersonation risk. The client trusts the locally
generated certificate and cannot independently detect an untrusted real server
behind TlsMitm.

Changes to the outbound [Network](@port/right/Network), local CA, TLS config,
leaf TTL, sniff limit, or filter replace the supplied
[Network](@port/left/Network). Active connections through the previous
configuration can close.
