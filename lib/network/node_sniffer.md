# Network Sniffer

Wraps backend `almagest/network` slots with `gonnect/sniffer.Sniffer` and
publishes the wrapper as one `almagest/network` value.

The node inspects client-first bytes for outgoing TCP `Dial` and `DialTCP` calls
when rules return `INTERCEPT`. After sniffing, inspected bytes are put back
before the selected backend sees the connection.

Rules use `github.com/asciimoth/gonnect/routing` sniffer bytecode syntax. Slots
are one-based and map to right-side `slot N` ports. Slot `0` rejects the call.
This node supports slots `1` through `16`.

Example: block one HTTP URL, remap another URL to a fixed backend address, and
route all other HTTP traffic to slot 1.

```text
DIAL
TCP
AND
INTERCEPT

SNIFF HTTP URL:/blocked
DROP

SNIFF HTTP URL:/backend-a
ROUTE 1 DST:127.0.0.1:8080

SNIFF HTTP
SLOT 1

TRUE
DROP
```

Example: route HTTPS traffic by TLS ALPN.

```text
DIAL
TCP
AND
PORT 443
AND
INTERCEPT

SNIFF TLS ALPN:h2
SLOT 2

SNIFF TLS ALPN:http/1.1
SLOT 1

SNIFF_NONE
DROP
```

Inline classifier options use `KEY:VALUE` tokens with no spaces. Built-in
constructors include `HTTP` and `TLS`. Useful HTTP options include `METHOD`,
`URL`, `URL_PATTERN`, `HOST`, and `HOST_PATTERN`. Useful TLS options include
`VERSION`, `SNI`, `SNI_PATTERN`, `ALPN`, and `ALPN_PATTERN`.

`ROUTE` can change call fields before it selects a slot:

```text
ROUTE 1 DST:127.0.0.1:8443
ROUTE 2 DST_PORT:8080
```

Supported route fields are `NETWORK`, `SRC`, `DST`, `SRC_ADDR`, `SRC_PORT`,
`DST_ADDR`, `DST_PORT`, `HOST`, `SERVICE`, and `PROTO`.

The optional DNS cache storage name is used only by bytecode address string and
regexp rules for reverse-DNS lookups. Leave it empty unless a DNS cache node
populates that storage.
