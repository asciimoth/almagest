# DNS Client Node

`almagest/DnsClient` publishes an `almagest/dns` provider backed by
`github.com/asciimoth/gonnect/dns.Client`.

## Ports

- `DNS`, left-directed `almagest/dns`: emits detached DNS wrappers to
  consumers.
- `Network`, right-directed `almagest/network`: required. The node uses this
  network's `Dial` method for upstream DNS transport connections.
- `Bootstrap DNS`, right-directed `almagest/dns`: optional. The node passes this
  DNS interface to `dns.NewClient` so hostname-based upstream server URLs can be
  resolved before dialing.

## Configuration

The node menu exposes a `DNS servers` list. Saved config uses the lower-case
`servers` key:

```hujson
{
  "Class": "almagest/DnsClient",
  "servers": [
    "udp://127.0.0.1:53",
    "tcp://127.0.0.1:53",
    "dot://dns.example.test:853"
  ]
}
```

Supported schemes are `udp`, `tcp`, and `dot`. Entries without a scheme are
normalized to `udp://...` during apply/save.

## Lifecycle

The node owns exactly one `dns.Client` for the current effective Network,
Bootstrap DNS, and server list. When any of those inputs changes materially, it
closes previously emitted wrappers, closes the old client, builds a fresh
`dns.Client`, and re-emits detached DNS wrappers to every connected consumer.
Equivalent server-list applies do not rebuild the client.
