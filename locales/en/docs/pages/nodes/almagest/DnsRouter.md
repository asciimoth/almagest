Routes each DNS request to one selected DNS service. Rules are evaluated from
top to bottom against the first question in the DNS message. The first matching
`BACKEND` or `DROP` action stops evaluation.

Each rule segment ends with `BACKEND slot N` or `DROP`. `BACKEND` sends the
request to the specified slot. `DROP` rejects the request with a `no upstream`
error. The node supports slots 1 through 16, and each slot accepts one DNS
connection.

This example rejects one name, sends A queries for an internal domain to slot
2, and sends all other queries to slot 1:

```rulev1
ADDR_S blocked.example.
DROP

ADDR_RE (^|.*\.)internal\.test\.$
QTYPE A
AND
BACKEND slot 2

TRUE
BACKEND slot 1
```

`ADDR_S` matches an exact question name and is not case-sensitive. `ADDR_RE`
uses a regular expression against the question name. DNS names commonly end
with a dot, as shown in the example. `QTYPE` accepts a number or a common name
such as `A`, `AAAA`, `PTR`, `TXT`, `MX`, `SRV`, `CNAME`, `NS`, or `SOA`.
`QCLASS` accepts a number or `IN`, and `OPCODE` accepts a number or `QUERY`.
`NET4` matches A questions, and `NET6` matches AAAA questions. `FQDN` matches
a question name that is not an IP address. `ADDR4`, `ADDR6`, `SNET4`, and
`SNET6` match literal IP question names and subnets.

Write combined conditions in postfix order: put both predicates first, and
then put `AND` or `OR` after them. `NOT` reverses the condition before it.
Blank lines and lines that start with `#` are ignored.

Empty or comment-only rules send all requests to slot 1. A nonempty rule set
has no automatic fallback. Add these lines at the end if unmatched requests
must use a default service:

```text
TRUE
BACKEND slot 1
```

The router does not provide DNS failover. If a rule selects a disconnected
slot, or if the selected service returns an error, the router does not try a
different slot. Connect a [DnsFallback](@docs/node/almagest/DnsFallback) node when you need ordered retries.

Applying rules creates the slots that they reference. A referenced slot stays
visible without a connection. A connected slot stays visible after rules stop
referencing it, and disappears after you disconnect it. If no rule or
connection needs a slot, only slot 1 remains. An invalid rule edit is not
applied, so the last valid rules continue to route requests.
