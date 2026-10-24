TailscaleCtrl connects a [WireGuard](@docs/node/almagest/WireGuard) node to
Tailscale or to a compatible control server such as Headscale. It uses the
private key of the connected [WireGuard](@docs/node/almagest/WireGuard) node. It
adds and removes only the peers and relay path that it manages, so
[WireGuard](@docs/node/almagest/WireGuard) peers from other sources can use the
same device.

## Connections

Connect [WireGuard](@port/right/WireGuard) to the control input. TailscaleCtrl
sends the current Tailscale addresses, routes, and MTU to
[WireGuard](@docs/node/almagest/WireGuard).
[WireGuard](@docs/node/almagest/WireGuard) includes these values in the
configuration that it sends to its TUN. The node reaches the `up` state only
after [WireGuard](@docs/node/almagest/WireGuard) and its TUN accept this
configuration.

Connect [Network](@port/right/Network) to the path that must carry these
connections:

- control-server requests and control-server name lookups
- STUN and DISCO discovery traffic
- DERP relay traffic over TLS

Direct peer UDP traffic does not use this Network input. It uses the default
Network connected to the [WireGuard](@docs/node/almagest/WireGuard) node. For
example, a proxy on the TailscaleCtrl Network input carries control and DERP
traffic, but it does not carry a direct
[WireGuard](@docs/node/almagest/WireGuard) connection to a peer. Configure the
WireGuard Network path separately when direct peer traffic must use a specific
route.

## Configuration

**Control URL** accepts an `http` or `https` URL. The default is
`https://controlplane.tailscale.com`. Set a Headscale URL here when you use a
self-hosted control server. The selected **TLS config** applies to outgoing TLS
connections, including HTTPS control connections and DERP.

If **Hostname** is empty, the node uses its node name as the advertised
hostname.

**Auth key secret** is optional. Select a secret that contains a reusable or
one-time pre-authentication key. If you do not select a secret and the machine
needs approval, the node shows an authorization link and a warning. Complete
authorization in the browser, then use **Check authorization** if the status
does not update. The node also checks authorization periodically while it waits.

**Ephemeral node** asks the control server to remove the node after it goes
offline. The exact removal time is controlled by the server.

**Require peer confirmation** is off by default. When it is off, TailscaleCtrl
automatically adds each eligible peer received from the control server to the
connected [WireGuard](@docs/node/almagest/WireGuard) node.

When **Require peer confirmation** is on, a new control-provided peer stays
outside WireGuard until a local administrator confirms it. Open **Peer
confirmation** in the node menu and select **Confirm peer**. The peer entry
shows its stable ID, confirmation state, and whether it is currently applied to
WireGuard. This local confirmation is separate from control-server machine
authorization and tailnet ACL rules.

Use **Revoke confirmation** to remove a previously confirmed peer from
WireGuard. Revocation also withdraws the routes owned by that peer. The peer
continues to appear in the menu as awaiting confirmation while the control
server continues to advertise it, and you can confirm it again.

The peer confirmation controls are hidden when **Require peer confirmation** is
off. Turning the option off restores automatic handling of all eligible peers.
It does not erase saved confirmations. If you enable the option again,
previously confirmed stable peer IDs remain confirmed. Persistent daemon storage
keeps confirmations across restarts; without persistent storage, they last only
until the daemon stops.

**Discovery UDP port** selects the local UDP port for endpoint discovery. The
value must be from `0` through `65535`. Use `0` to let the connected Network
select a port.

**Disable DERP** removes the TLS relay fallback. Direct UDP remains enabled. Use
this option only when peers can establish a direct path.

**Disable discovery** disables local endpoint advertisement, STUN, and DISCO.
Direct endpoints supplied by the control server can still be used. DERP can
still carry traffic unless you also disable DERP.

## DNS output

The [DNS](@port/left/DNS) output serves the current MagicDNS records received
from the control server. It supports `A`, `AAAA`, `CNAME`, `PTR`, `NS`, `MX`,
`SRV`, and `TXT` queries. Reverse lookups are made from the received address
records. A single-label name also uses the MagicDNS search domains. A name that
contains a dot is looked up as written.

Successful answers have a TTL of 60 seconds. The output does not query the
nameservers advertised by the control server, the host resolver, or another DNS
source. A missing name returns `NXDOMAIN`. Add
[DnsFallback](@docs/node/almagest/DnsFallback) if other names must continue to
another resolver.

The DNS output follows control-server updates. When TailscaleCtrl is disabled,
disconnected, or in an error state, the output stays connected but has no
records.

## Firewall output

The **Firewall** output converts the current tailnet packet filter to an
incoming allow policy for [NetworkFirewall](@docs/node/almagest/NetworkFirewall)
or [TunFirewall](@docs/node/almagest/TunFirewall). Rules can match the remote
source, local destination, protocol, and destination port. The policy does not
restrict outgoing traffic. Tailscale capability grants are not included because
they do not have an equivalent firewall rule.

The output follows packet-filter updates from the control server. When the
controller is not active, it sends an empty policy. Do not use this output as a
persistent copy of the control-server policy.

## Stored identity

The daemon stores the machine identity, discovery identity, and peer
confirmation data as hidden private data for this node. Persistent daemon
storage keeps this identity after a restart and after you rename the node. If
persistent storage is unavailable, the identity is kept only until the daemon
stops. The node does not copy the WireGuard private key into this data.
