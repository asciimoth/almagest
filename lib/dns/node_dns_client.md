# DNS Client Node

`almagest/DnsClient` publishes an `almagest/dns` provider backed by
`github.com/asciimoth/gonnect/dns.Client`.

## Ports

- `DNS`, left-directed `almagest/dns`: emits detached DNS wrappers to consumers.
- `Network`, right-directed `almagest/network`: required. The node uses this
  network's `Dial` method for upstream DNS transport connections.
- `Bootstrap DNS`, right-directed `almagest/dns`: optional. The node passes this
  DNS interface to `dns.NewClient` so hostname-based upstream server URLs can be
  resolved before dialing.

## Configuration

The node menu exposes a `DNS servers` list and a `Request timeout (ms)` field.
The timeout limits one request, including bootstrap lookup, dial, TLS handshake,
and network I/O. Set it to `0` to use the gonnect default. Saved config uses the
`servers` and `requestTimeoutMS` keys:

```hujson
{
  "Class": "almagest/DnsClient",
  "servers": [
    "udp://127.0.0.1:53",
    "tcp://127.0.0.1:53",
    "dot://dns.example.test:853"
  ],
  "requestTimeoutMS": 0
}
```

Supported schemes are `udp`, `tcp`, and `dot`. Entries without a scheme are
normalized to `udp://...` during apply/save.

## Lifecycle

The node owns exactly one `dns.Client` for the current effective Network,
Bootstrap DNS, server list, and timeout. When one of those inputs changes
materially, it closes previously emitted wrappers, closes the old client, builds
a fresh `dns.Client`, and re-emits detached DNS wrappers to every connected
consumer. Equivalent configuration applies do not rebuild the client.
