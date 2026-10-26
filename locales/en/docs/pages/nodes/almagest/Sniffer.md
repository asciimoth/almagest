Sniffer routes Network operations to as many as 16 backend slots. It can make an
immediate routing decision, or it can inspect the first bytes that a client
sends on an outgoing TCP connection before it selects a backend.

Connect consumers to [Network](@port/left/Network), and connect backend paths to
the numbered slots. The node supplies [Network](@port/left/Network) only when
every slot referenced by the rules is connected and ready. There is no automatic
fallback. An operation is rejected if no rule matches, a rule uses `DROP`, or
sniffing fails. Empty or comment-only rules reject all operations.

Sniffer has two routing stages:

1. Normal predicates examine the operation, network, and address. `SLOT N`,
   `ROUTE N`, and `DROP` complete the decision without traffic inspection.
1. `INTERCEPT` delays the backend connection and starts traffic inspection. Use
   it only for outgoing TCP dials. After inspection, `SNIFF` and `SNIFF_NONE`
   predicates select the final action.

Rules are evaluated from top to bottom. Each segment ends with `INTERCEPT`,
`SLOT N`, `ROUTE N`, or `DROP`. The first matching action in each stage stops
evaluation. Normal predicates and postfix Boolean operators work in the same way
as [NetworkRouter](@docs/node/almagest/NetworkRouter) rules.

`SNIFF_NONE` matches when none of the configured classifiers matches. This
example blocks HTTP requests whose target starts with `/admin`, sends TLS
connections that offer `h2` to [slot 2](@port/right/slot%202), and sends other
inspected traffic to [slot 1](@port/right/slot%201):

```rulev1
DIAL
TCP
AND
INTERCEPT

SNIFF HTTP URL_PATTERN:/admin*
DROP

SNIFF TLS ALPN:h2
SLOT 2

SNIFF_NONE
SLOT 1
```

The client can send data before Sniffer opens the selected backend connection.
Sniffer puts the inspected bytes back at the start of the stream, so the backend
receives them unchanged. Sniffer does not inspect server responses or later
traffic in the connection.

## Protocol classifiers

The built-in classifiers are:

- `HTTP`, `TLS`, `SSH`, `HTTP2`, `AMQP`, `MQTT`, `PostgreSQL`, `MongoDB`, and
  `Redis`
- `ProxyProtocolV1`, `ProxyProtocolV2`, and `ProxyProtocol`
- `SOCKS4`, `SOCKS5`, and `SOCKS`
- `DNSOverTCP`, `RTSP`, `SIP`, `STUN`, `RDP`, `SMB`, `LDAP`, and `Cassandra`
- `MemcachedBinary`, `MemcachedASCII`, and `Memcached`

Classifier names are case-sensitive. The combined `ProxyProtocol`, `SOCKS`, and
`Memcached` classifiers match either of their protocol variants. `HTTP2` matches
the cleartext HTTP/2 client preface. To match HTTP/2 in TLS, use
`SNIFF TLS ALPN:h2`.

Only `HTTP` and `TLS` accept options. Options use `KEY:VALUE` tokens without
spaces.

An HTTP classifier can match `METHOD`, `URL`, `URL_PATTERN`, `VERSION`, `HOST`,
or `HOST_PATTERN`. `URL` is the request target as sent by the client, including
a query string when present. Host matching is not case-sensitive and ignores an
optional port. It uses the hostname in an absolute request target or the `Host`
header.

A TLS classifier examines the ClientHello. It can match `VERSION`, `SNI`,
`SNI_PATTERN`, `ALPN`, `ALPN_PATTERN`, `SNI_AVAILABLE`, or `SNI_ENCRYPTED`. SNI
matching is not case-sensitive. ALPN matching is case-sensitive. Sniffer does
not decrypt TLS. With Encrypted Client Hello, it can detect the ECH signal but
cannot read the encrypted inner hostname; an SNI rule sees only a visible outer
hostname.

Patterns match the complete value. `*` matches any byte sequence, `?` matches
one byte, and `\` escapes the next byte. When an option appears more than once,
any value in that option group can match. Different option groups must all
match.

By default, HTTP inspection is limited to a 4,096-byte request line and an
8,192-byte header section when a host rule needs headers. TLS inspection is
limited to a 65,536-byte ClientHello. Change these limits with
`MAX_REQUEST_LINE_BYTES`, `MAX_HEADER_BYTES`, and `MAX_CLIENT_HELLO_BYTES`. Data
that does not form a match within a classifier's limit does not match that
classifier. A read error rejects the connection. Sniffer does not add an
inspection timeout, so use client deadlines when a peer can open a connection
without sending a complete prefix.

## Route changes

`ROUTE` selects a slot and changes fields before the backend receives the
operation. For example:

```rulev1
SNIFF HTTP URL:/backend-a
ROUTE 1 DST:127.0.0.1:8080
```

The supported fields are `NETWORK`, `SRC`, `DST`, `SRC_ADDR`, `SRC_PORT`,
`DST_ADDR`, `DST_PORT`, `HOST`, `SERVICE`, and `PROTO`.

Rules add the slot ports that they reference. A connected slot stays visible
after rules stop referencing it, and disappears after you disconnect it. If no
rule or connection needs a slot, [slot 1](@port/right/slot%201) remains visible,
but it is not an implicit route. Invalid rules are not applied. A valid rule
change or a slot change can replace [Network](@port/left/Network), and existing
connections through the previous Network can close.

## Storage, logging, and sensitive data

Sniffer does not capture packets, save inspected payloads, or provide traffic
logs. It only supplies the routed [Network](@port/left/Network). Inspection
occurs in memory. HTTP rules can examine request targets that contain sensitive
query data, so use only the fields that the routing decision needs.

**DNS cache storage** is not capture storage. Select it only when `ADDR_S` or
`ADDR_RE` rules must match a cached reverse name for a numeric IP address. A
[DnsCache](@docs/node/almagest/DnsCache) node must populate the same storage.
Sniffer does not make a live reverse lookup for these matches and does not write
inspected traffic to the cache.
