TlsTerminator accepts an outgoing [TLS](@docs/almagest/tls) connection from a
client, presents a locally generated certificate, and sends the decrypted TCP
stream through the connected [Network](@port/right/Network). Place it between a
[Network](@port/left/Network) consumer and the [Network](@port/right/Network)
that leads to the plaintext service.

The client must trust Almagest's local TLS CA. TlsTerminator uses that CA to
generate a certificate for the
[SNI](https://en.wikipedia.org/wiki/Server_Name_Indication) hostname in each
connection. It supplies its [Network](@port/left/Network) only when the outbound
[Network](@port/right/Network), local CA, and menu values are valid.

The connection between TlsTerminator and the destination is plaintext. Use this
node only when the destination expects plaintext, such as an HTTP service behind
an HTTPS entry point. Protect the upstream path because it no longer has TLS
confidentiality or server authentication.

## Supported traffic

TlsTerminator supports outgoing TCP connections only. The client must start the
connection with a valid TLS ClientHello that contains a visible SNI hostname. It
rejects the connection in these cases:

- The first client data is not TLS.
- The ClientHello has no visible SNI hostname.
- The ClientHello signals
  [Encrypted Client Hello (ECH)](https://en.wikipedia.org/wiki/Encrypted_Client_Hello).
- The ClientHello is malformed or exceeds the sniff buffer limit.

Rejected data is not passed through, and TlsTerminator does not open an upstream
connection for it. UDP, QUIC, listeners, DNS lookups, and other Network
operations are not supported. TlsTerminator does not set a timeout while it
waits for the ClientHello. Set a connection deadline when a client can connect
without sending data.

## Certificate and ALPN settings

**Leaf TTL seconds** sets the validity period of generated certificates. Zero
uses 24 hours. A generated certificate never remains valid after the local CA
expires. A negative value is invalid.

**Sniff buffer bytes** limits how much of the first ClientHello TlsTerminator
can inspect. Zero uses 65,536 bytes. A smaller value reduces memory use but can
reject a valid ClientHello with many extensions. A negative value is invalid.

**Client-facing
[ALPN](https://en.wikipedia.org/wiki/Application-Layer_Protocol_Negotiation)**
lists the protocols that TlsTerminator can select for the TLS connection from
the client. Separate values with spaces, commas, semicolons, or new lines. For
example:

```text
h2
http/1.1
```

An empty list disables ALPN selection. This setting does not change the
plaintext protocol or destination. Configure only protocols that the upstream
service understands.

## Destination selection

By default, TlsTerminator sends plaintext TCP to the original requested
destination. Enable **Use SNI hostname** to use the SNI hostname with the
original destination port instead. A matching destination-remap rule takes
priority over both defaults.

**Destination remaps JSON** is an array of rules. Rules are checked in order,
and the first matching rule selects its `dst`. In one rule, all configured
fields must match. Multiple values in one field are alternatives. An omitted or
empty match field matches any value.

This example sends TLS for `example.test` to a local plaintext service:

```json
[
  {
    "sni_hosts": ["example.test"],
    "dst": "127.0.0.1:8080"
  }
]
```

Each rule supports these fields:

- `original_dsts` matches the destination that the client requested. Values can
  be a host, `host:port`, IP address, `IP:port`,
  [CIDR](https://en.wikipedia.org/wiki/Classless_Inter-Domain_Routing) subnet,
  or host pattern.
- `sni_hosts` matches the visible SNI hostname and is not case-sensitive.
- `alpns` matches any protocol that the client offers. Matching is
  case-sensitive and does not depend on the protocol finally selected.
- `dst` is required and must use `host:port` syntax. It is the plaintext TCP
  destination for the matching rule.

SNI and ALPN patterns match the complete value. `*` matches any byte sequence,
`?` matches one byte, and bracket expressions such as `[0-9]` match one byte
from a set.

Changing the outbound [Network](@port/right/Network), local CA, or any setting
replaces the supplied [Network](@port/left/Network). Active connections through
the previous configuration can close. An invalid negative value or malformed
JSON changes the node label to `invalid config` and makes its Network
unavailable. Other invalid remap values prevent the
[Network](@port/left/Network) output and report an error. A missing local CA
changes the label to `local TLS CA unavailable`. The label is
`waiting for network` until an outbound [Network](@port/right/Network) is ready
and `terminating TLS` during normal operation.

## Graph example

Sniffer lets an HTTP service share one address and port for HTTP and HTTPS. It
sends plain HTTP directly to the service Network. It sends TLS to TlsTerminator,
which removes TLS before it sends the connection to the same service Network.

```graph
{
  "Clients": {
    "Class": "almagest/LocalProxy",
    "Pos": "{\"x\":31,\"y\":120}",
    "Links": ["proxy -> [Protocol sniffer] Network"],
  },
  "Protocol sniffer": {
    "Class": "almagest/Sniffer",
    "Pos": "{\"x\":245,\"y\":120}",
    "Links": [
      "slot 1 -> [Service network] Network",
      "slot 2 -> [TLS terminator] Network",
    ],
    "rules": "DIAL\nTCP\nAND\nINTERCEPT\n\nSNIFF HTTP\nROUTE 1 DST:127.0.0.1:8080\n\nSNIFF TLS\nROUTE 2 DST:127.0.0.1:8080\n\nTRUE\nDROP",
  },
  "TLS terminator": {
    "Class": "almagest/TlsTerminator",
    "Pos": "{\"x\":493,\"y\":187}",
    "Links": ["Network -> [Service network] Network"],
  },
  "Service network": {
    "Class": "almagest/MockNetwork",
    "Pos": "{\"x\":731,\"y\":120}",
  },
  "HTTP service": {
    "Class": "almagest/HttpServer",
    "Pos": "{\"x\":493,\"y\":53}",
    "Links": ["Network -> [Service network] Network"],
  },
}
```
