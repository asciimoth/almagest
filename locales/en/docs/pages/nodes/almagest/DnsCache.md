Caches DNS responses from an optional upstream DNS service. On a cache hit, the
node returns the stored response. On a cache miss, it queries the upstream
service and stores a successful response.

**CacheStorage name** selects the shared cache. An empty name uses `default`.
DnsCache nodes that use the same name read and write the same entries. Other
nodes can also use this storage for reverse-DNS matching. Use separate names
when DNS paths must not share lookup history.

The daemon's persistent cache storage keeps entries between node changes and
daemon restarts. Entries expire according to their stored DNS expiration time
unless cache expiration is disabled for the daemon.

**Reverse lookups** is enabled by default. When an upstream response contains a
successful A or AAAA answer, the node also stores a PTR record that maps the IP
address back to the name. This permits reverse-DNS queries and lets routing
rules match a cached name for an IP address. These PTR records are visible to
all nodes that use the same storage name. Disabling this option stops the node
from adding new PTR records. It does not remove records that are already in the
shared storage.

Enable **Write only** to warm shared storage without reading from it. In this
mode, every query goes to the upstream service, even when the storage already
has a matching response. Successful responses are still stored, so another
DnsCache node with **Write only** disabled can serve them.

The upstream DNS connection is optional in normal mode. If you disconnect it,
connected consumers can continue to receive cached responses. A cache miss fails
until an upstream service is connected again. In write-only mode, all queries
need the upstream service, so they fail while it is disconnected.

Changing the storage name or either option replaces the DNS service supplied to
connected consumers. The stored entries remain in their named storage.
