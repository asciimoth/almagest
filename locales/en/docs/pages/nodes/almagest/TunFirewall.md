Filters IP packets between one [TUN](@docs/almagest/types) provider on
[Tun](@port/right/Tun) and one TUN consumer on [Tun](@port/left/Tun).

The default policy is stateful and has these actions:

- Outgoing packets are allowed unless they match an `exclude` rule.
- Unsolicited incoming packets are denied unless they match an `include` rule.
- Replies to allowed outgoing packets are accepted for a limited time.

Denied packets are silently dropped. The firewall also drops malformed and
non-IP packets that it cannot parse.

## Policy format

Enter **Firewall rules** as one JSON object. Use `{}` for the default policy.
The node rejects empty input, invalid JSON, unknown fields, invalid port values,
and non-positive response times.

Each entry in `exclude` or `include` can contain these fields:

- `network` selects a protocol. Common values are `tcp`, `tcp4`, `tcp6`, `udp`,
  `udp4`, `udp6`, `icmp`, `icmp4`, `icmp6`, `ip`, `ip4`, and `ip6`. `tcp`,
  `udp`, and `icmp` match both IP versions. An empty or omitted value matches
  all parsed IP protocols.
- `hosts` selects the remote peer. Use an IP address,
  [CIDR](https://en.wikipedia.org/wiki/Classless_Inter-Domain_Routing) prefix,
  or hostname pattern. For outgoing packets, this is the destination. For
  incoming packets, this is the source. An omitted list matches every peer.
- `localHosts` selects the local destination of an incoming packet. It has no
  effect in an `exclude` rule. An omitted list matches every local address.
- `ports` lists individual service ports. An `exclude` rule checks the remote
  destination port. An `include` rule checks the local destination port.
- `portRanges` lists inclusive ranges. Each range has `first` and `last` values.
  The order of those two values does not matter.

All configured fields in one rule must match. Values in one list are
alternatives. Rules in a list are also alternatives.

This policy blocks outgoing TCP packets to port 443 in `203.0.113.0/24`. It also
permits unsolicited incoming UDP packets to local port 51820:

```json
{
  "exclude": [
    {"network": "tcp", "hosts": ["203.0.113.0/24"], "ports": [443]}
  ],
  "include": [
    {"network": "udp", "ports": [51820]}
  ]
}
```

Host-name patterns are not case-sensitive. `*` matches any sequence, `?` matches
one character, and bracket expressions such as `[0-9]` match one character from
a set. The firewall does not make DNS queries. TUN packets contain numeric IP
addresses, and this node does not have a DNS cache input. Use IP addresses or
CIDR prefixes for reliable TUN filtering.

For another IP protocol, use its decimal protocol number. For example, `ip:47`
matches GRE in both IP versions, while `ip4:47` matches only IPv4 GRE.

## Reply tracking

Each allowed outgoing packet records its reverse flow. A matching incoming
packet is accepted without an `include` rule. Related
[ICMP](https://en.wikipedia.org/wiki/Internet_Control_Message_Protocol) and
ICMPv6 error packets are also accepted when they contain a recorded outgoing
flow.

`responseTTL` controls how long reply state is kept. Use a positive duration
such as `"30s"` or `"5m"`. The default is two minutes. A policy change applies
to new packet decisions immediately, but existing reply entries keep their
original expiration time.

Reply tracking matches addresses, protocol, and ports. It does not validate a
TCP handshake or TCP flags.

## Fragments and protocol parsing

The firewall parses IPv4, IPv6, TCP, UDP, ICMP, ICMPv6, and numeric IP
protocols. It follows IPv6 hop-by-hop, routing, destination-options, fragment,
and authentication headers. It can read transport ports from an initial
fragment. A later fragment has no transport header, so the firewall treats its
ports as zero.

A port-specific rule does not match a later fragment. Use protocol and address
selectors when fragmented traffic must be controlled. Incoming later fragments
still require an `include` rule that can match without a port, or a matching
reply entry created for fragmented outgoing traffic.

## Combine policies

You can connect multiple **Object** values to [Config](@port/left/Config). Each
Object uses the same JSON format. The effective policy is the union of the menu
policy and all valid connected Objects. The node combines all `exclude` and
`include` rules. If policies specify different `responseTTL` values, the longest
effective duration is used. An omitted duration counts as the two-minute
default.

**Effective config** shows the combined and normalized policy. Object changes
apply to an active TUN without replacing it. Disconnecting an Object removes
only that Object's contribution.

An invalid menu edit is rejected, the previous effective policy stays active,
and the label changes to `invalid config`. An invalid Object is omitted, any
previous policy from that Object is removed, and the label changes to
`invalid object config`.

The firewall passes complete TUN configuration suggestions, including addresses,
routes, source routes, MTU, and DNS, back to the [Tun](@port/right/Tun)
provider. It does not use these suggestions as firewall rules.

The label is `waiting for TUN` until the input is available. During normal
operation, it shows the number of effective rules after normalization.
