Filters TCP and UDP traffic from a source [Network](@port/right/Network).
Connect clients or servers to its filtered [Network](@port/left/Network).

The default policy has these actions:

- Outgoing traffic is allowed unless it matches an `exclude` rule.
- Unsolicited incoming traffic is denied unless it matches an `include` rule.
- Replies to allowed outgoing traffic do not need an `include` rule.

A denied outgoing operation returns a `firewall denied` error. Denied incoming
traffic is not delivered to the client or server.

## Policy format

Enter the policy in **Firewall rules** as a JSON object. Use `{}` for the
default policy. The node rejects invalid JSON, unknown fields, invalid port
values, and non-positive response times.

Each `exclude` or `include` entry can contain these fields:

- `network`: A protocol such as `tcp`, `tcp4`, `tcp6`, `udp`, `udp4`, or `udp6`.
  `tcp` and `udp` match both IP versions. An omitted value matches all
  protocols.
- `hosts`: Remote hostnames, IP addresses, or CIDR prefixes. Hostname matching
  is not case-sensitive. You can use patterns such as `*.example.test`. An
  omitted value matches all remote hosts.
- `localHosts`: Local destination hostnames, IP addresses, or CIDR prefixes.
  This field applies only to `include` rules. An omitted value matches all local
  destinations.
- `ports`: Individual service ports. These are remote destination ports for
  outgoing traffic and local destination ports for incoming traffic.
- `portRanges`: Inclusive service-port ranges. Each range has `first` and `last`
  values.

Fields in one rule are combined. For example, a rule with `network`, `hosts`,
and `ports` matches traffic only when all three fields match. Values in an array
are alternatives.

This example blocks outgoing TCP connections to port 443 on `*.example.test`. It
also permits incoming TCP connections from any remote host to local port 8080:

```json
{
  "exclude": [
    {"network": "tcp", "hosts": ["*.example.test"], "ports": [443]}
  ],
  "include": [
    {"network": "tcp", "ports": [8080]}
  ]
}
```

The firewall does not make DNS queries to match a hostname rule. An outgoing
connection that uses a hostname can match that name directly. If the operation
uses an IP address, use an IP address or CIDR rule.

## Reply tracking

Use `responseTTL` to set how long incoming traffic from a peer is accepted as a
reply after an allowed outgoing write. The value is a positive duration such as
`"30s"` or `"5m"`. The default is two minutes.

## Combine policies

You can connect multiple **Object** values to [Config](@port/left/Config). Each
Object must use the same policy format. The effective policy is the union of the
menu rules and all valid connected Objects. All `exclude` rules and all
`include` rules remain active. If policies specify different `responseTTL`
values, the longest effective duration is used. An omitted duration counts as
the two-minute default.

**Effective config** shows the combined policy. A change to the menu policy or a
connected Object applies immediately. Disconnecting an Object removes only its
contribution.

An invalid menu edit is rejected, and the current policy stays active. An
invalid connected Object is omitted from the effective policy, and the node
reports `invalid object config`.

The node label is `waiting for network` until its source
[Network](@port/right/Network) is available. When it is active, the label shows
the number of effective rules.
