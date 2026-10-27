Tries connected [DNS](@docs/almagest/dns) services in
ascending slot order. [slot 1](@port/right/slot%201) has the highest priority.
Empty slots are skipped.

The node returns the first response with the `NOERROR` status. It tries the next
connected slot when a service returns an error, returns no response, exceeds the
attempt timeout, or returns a DNS error status such as `NXDOMAIN` or `SERVFAIL`.
Thus, an `NXDOMAIN` response from an early slot does not stop the search. If all
connected services fail, the node returns the response and error from the last
service that it tried. A query fails with a `no upstream` error when no slot has
a ready DNS service.

**Attempt timeout (ms)** is the maximum wait for one service, not for the
complete query. Set it to `0` to use the default of 5 seconds per service. For
example, three unresponsive services can take approximately three times the
configured timeout before the query completes. A DNS client or caller can apply
a shorter timeout to the complete query.

Each slot accepts one DNS connection. The node supports slots 1 through 16 and
keeps one unused slot available while you add connections. When you disconnect a
service, extra empty slots are removed. Connected slots keep their numbers, so a
sparse layout such as [slot 2](@port/right/slot%202) and
[slot 5](@port/right/slot%205) keeps that priority order.

You can connect, disconnect, or replace services while the node is in use. New
queries use the current slot layout. If a change affects the service that an
active query is using, the node cancels that attempt and continues with later
connected slots. It does not retry the replacement in the same slot during that
query.

Changing **Attempt timeout (ms)** cancels active queries and supplies consumers
with a replacement fallback service. New queries use the new timeout and keep
the currently connected slots.

## Graph example

This resolver path tries the local records first. It sends an unanswered
question to outbound DNS. DnsCache stores successful answers from either path.

```graph
{
  "DNS consumer": {
    "Class": "almagest/DnsQuery",
    "Pos": "{\"x\":31,\"y\":120}",
    "Links": ["DNS -> [DNS cache] DNS"],
  },
  "DNS cache": {
    "Class": "almagest/DnsCache",
    "Pos": "{\"x\":230,\"y\":120}",
    "Links": ["DNS -> [Fallback resolver] DNS"],
  },
  "Fallback resolver": {
    "Class": "almagest/DnsFallback",
    "Pos": "{\"x\":430,\"y\":120}",
    "Links": [
      "slot 1 -> [Local records] DNS",
      "slot 2 -> [Outbound DNS] DNS",
    ],
  },
  "Local records": {
    "Class": "almagest/StaticDns",
    "Pos": "{\"x\":681,\"y\":53}",
    "records": "service.internal A 192.0.2.10",
  },
  "Outbound DNS": {
    "Class": "almagest/OutDns",
    "Pos": "{\"x\":681,\"y\":187}",
  },
}
```
