# DNS Cache Node

`almagest/DnsCache` publishes an `almagest/dns` middleware backed by
`github.com/asciimoth/gonnect/dns.Cache`.

## Ports

- `DNS`, left-directed `almagest/dns`: emits detached wrappers around the
  cache to consumers.
- `DNS`, right-directed `almagest/dns`: optional upstream DNS source for cache
  misses. Cached hits can still be served after this input is detached.

## Configuration

The node menu exposes three fields. Saved config uses the same config keys:

```hujson
{
  "Class": "almagest/DnsCache",
  "storageName": "default",
  "reverseLookups": false,
  "writeOnly": false
}
```

`storageName` selects the `dns.CacheStorage` returned by the engine's
`DnsCacheManager`. Nodes using the same name share storage.

`reverseLookups` makes successful A and AAAA answers populate synthetic PTR
entries. Enable it only when every consumer sharing the same storage can observe
that lookup history.

`writeOnly` keeps writing successful upstream answers into shared storage while
refusing to serve entries already present there.

## Lifecycle

The node owns one `dns.Cache` for the current storage and toggles. DNS input
attach and detach operations call `Attach` and `Detach` on that cache, so
existing output wrappers remain usable and cached answers survive upstream
removal. Changing the storage name or either toggle closes emitted wrappers,
closes the old cache, and publishes a fresh cache instance.
