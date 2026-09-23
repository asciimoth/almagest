# DNS Graph Lifecycle

This hermetic case exercises DNS graph composition through the daemon and
admin API. It does not use the container network for DNS requests.

The graph contains three static DNS providers, an ordered `DnsFallback`, a
`DnsCache`, a `DnsRouter`, and query nodes attached to each layer.

Assertions cover these situations:

- fallback skips an `NXDOMAIN` provider and uses providers in slot order;
- removing and restoring a fallback input changes the selected answer;
- rebuilding fallback after a live timeout edit keeps its inputs usable;
- cache-only mode serves a cached answer after its upstream is detached;
- an uncached request fails while the cache has no upstream and recovers after
  the upstream is restored;
- router input detach and reattach affect only the selected backend;
- an invalid router rule edit reports invalid config but keeps the last valid
  route active;
- changing rules adds a new numbered router port and permits a live rewire;
- a query notices when its DNS link is removed and recovers after relinking;
- an invalid query type fails without affecting other DNS consumers; and
- a router port disappears after both its rule reference and link are removed.

The last assertion protects the documented dynamic-slot contract: only slots
used by rules, slots that still have links, and the default slot remain
visible.
