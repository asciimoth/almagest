The term **kill switch** describes a traffic policy. Different programs can
implement this policy. A kill switch prevents traffic from bypassing a protected
path. It blocks outgoing host traffic by default and permits only traffic that
has an explicit reason to use the direct network.

For example, an application can use a VPN through Almagest while the VPN is
available. If the VPN stops or its route fails, the operating system can send
new connections through the normal network instead. A kill switch blocks this
fallback and prevents the application from leaking traffic to the local network
or Internet provider.

A kill switch is separate from the Almagest graph. Graph nodes control traffic
that enters their paths. The kill switch controls what the host can send when
traffic does not use a permitted path. This separation keeps the default block
active if the graph fails or the Almagest service stops.

[Application and process filtering](@docs/almagest/process-filtering) selects
which host traffic enters an Almagest path. A kill switch supplies the separate
host policy that blocks unapproved fallback traffic.

## Supported Linux implementation

Almagest supports
[asciimoth/killswitch](https://github.com/asciimoth/killswitch) on Linux. This
project is one implementation of the general kill-switch policy. It runs as a
separate system service. Its installation and configuration are outside the
scope of Almagest.

The Almagest integration is enabled by default. When asciimoth/killswitch is
available at its default location, Almagest connects to it automatically and
permits all traffic that the graph sends through these system outputs:

- [OutNetwork](@docs/node/almagest/OutNetwork) for connections through the
  direct host network.
- [OutDns](@docs/node/almagest/OutDns) for requests to the outbound system DNS
  service.

This permission applies to all traffic from these outputs. For example, a VPN
node can reach its server through OutNetwork, and then carry application traffic
inside the VPN. Direct application traffic that does not go through a permitted
Almagest output remains blocked.

Almagest updates its temporary permissions as the system state changes. If the
Almagest service stops, it removes these permissions while the kill switch keeps
its base block policy.

> [!CAUTION]
> The asciimoth/killswitch integration is experimental and has not been
> security-audited. Do not use it as the only security boundary for sensitive
> systems.
