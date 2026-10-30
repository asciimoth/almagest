[WireGuard](https://www.wireguard.com/#conceptual-overview) connects a
[Tun](@port/left/Tun) packet path to an encrypted UDP tunnel. Connect the local
IP stack or system TUN to [Tun](@port/left/Tun). Connect a UDP-capable Network
to [Network](@port/right/Network). The node uses that
[Network](@port/right/Network) for the listen socket and for the endpoints of
peers configured in the menu or through [Peers](@port/left/Peers).

Both connections are required for the `up` state. The node does not open its UDP
socket when only [Network](@port/right/Network) is ready. If
[Tun](@port/left/Tun) disconnects after startup, the node keeps its peer
configuration and UDP transport, shows `waiting for TUN`, and resumes when a
replacement [Tun](@port/left/Tun) connects. [Network](@port/right/Network)
accepts one connection.

**Listen port** accepts values from 0 through 65535. Zero lets the
[Network](@port/right/Network) select a port. The **Status** section shows the
actual active port.

## Interface key and addresses

**Private key secret** selects the secret that contains the interface private
key. The key value is never shown in the menu or saved in the node
configuration. If the field is empty, WireGuard generates a private key and
saves it as a new secret. If the selected secret is missing or this node cannot
read it, WireGuard also generates a replacement key. Check **Public key** after
you change secret access because a replacement changes the interface identity.

An invalid private key changes the node state to `invalid config`. Changes to an
accessible key secret apply to the active device. Old configurations that
contain a raw private key are moved to secret storage when they are loaded.

Enter the interface address prefixes in **Owned IPs**. A bare IP address becomes
a host prefix, such as `/32` for IPv4 or `/128` for IPv6. Duplicate prefixes are
removed. These values are sent to the connected [Tun](@port/left/Tun) provider
as address suggestions. They do not assign an address to the UDP
[Network](@port/right/Network).

Controllers connected to [WGCtrl](@port/left/WGCtrl) can also suggest owned IPs.
Menu values come first, followed by controller values in stable graph order.
Disconnecting a controller removes only its suggestions. Controller values are
not saved in this node's configuration.

## [Peers](@port/left/Peers)

Add static peers in the [Peers](@port/left/Peers) section. Each peer supports:

- **Public key**, which is required and identifies the peer.
- **Preshared key secret**, which optionally adds a second shared key.
- **Endpoint**, usually a hostname or IP address with a UDP port. Leave it empty
  when the endpoint must be learned from authenticated traffic.
- **Allowed IPs**, which selects the peer for matching tunnel destinations.
- **Keepalive**, in seconds. Zero disables persistent keepalive.

Allowed IPs accept prefixes or bare addresses. They are both WireGuard
crypto-routing rules and route suggestions for the connected
[Tun](@port/left/Tun) provider. Do not give two peers overlapping destination
ranges unless the resulting longest-prefix selection is intentional.

The preshared key value stays in secret storage. A missing, inaccessible, or
invalid preshared key secret makes the configuration invalid; WireGuard does not
silently remove that protection. Old raw preshared keys are moved to secret
storage when they are loaded.

[Peers](@port/left/Peers) also accepts multiple peer-list objects from other
nodes. Each object must contain a `peers` array with the same peer fields.
WireGuard uses the latest list from each connected source. A public key
identifies conflicts:

- A peer in this node's menu has priority over the same public key from an
  object.
- A stable graph order resolves the same key from two object sources. Give one
  source clear ownership of each public key instead of depending on this rule.
- Removing an object connection removes only the peers supplied by that source.
  Object peers are not copied into the saved menu configuration.

[WGConfig](@docs/node/almagest/WGConfig) nodes can manage additional peers and
UDP transports through [WGCtrl](@port/left/WGCtrl). Menu peers, object peers,
and controller peers can coexist. Disconnecting a controller removes only the
peers and transports that it owns; later menu changes do not delete them.

## Routes, DNS, and MTU

WireGuard sends one combined configuration to the [Tun](@port/left/Tun)
provider:

- Addresses come from **Owned IPs** and controller address suggestions.
- Routes come from the Allowed IPs of menu and object peers, followed by
  controller route suggestions.
- DNS comes from **DNS** or from a controller.
- MTU comes from controller ceilings and the UDP carrier limit when available.

Exact duplicate addresses and routes are removed. A controller can withdraw its
contribution by sending an empty configuration or by disconnecting.

**DNS** accepts one IPv4 or IPv6 address. A menu value always has priority. If
the menu field is empty, the most recently received current controller DNS
suggestion applies. This is only a suggestion for a simple TUN path, such as a
direct connection to [VTun](@docs/node/almagest/VTun). It does not configure the
UDP transport. Use DNS nodes when you need multiple resolvers, routing, caching,
or policy.

For peers that use [Network](@port/right/Network), WireGuard asks the carrier
for its safe maximum UDP payload and subtracts WireGuard overhead and effective
AWG transport padding. It then selects the smallest positive result and
controller MTU ceiling. The result is the **Effective MTU** suggestion for the
[Tun](@port/left/Tun) provider. If the carrier cannot report a limit and no
controller supplies one, the MTU suggestion is zero and the
[Tun](@port/left/Tun) provider chooses its fallback. An IPv6-capable
configuration reports an error if the calculated inner MTU is less than 1280.

The status shows **Carrier UDP payload**, **Carrier MTU cap**, **MTU source**,
and **MTU calculation error**. An unknown carrier limit can produce a
calculation message while another valid ceiling still keeps the tunnel usable.

## AmneziaWG settings

**[AmneziaWG](https://docs.amnezia.org/documentation/amnezia-wg/) obfuscation**
is off by default. When it is off, WireGuard uses the plain WireGuard-compatible
profile. When it is on, configure the Jc/Jmin/Jmax junk settings, S1-S4 padding,
H1-H4 headers, and I1-I5 initiation packets to match the remote peer. Incorrect
values prevent communication.

Turning obfuscation off hides these fields but keeps their values. They have no
effect until you enable obfuscation again.

Each peer can use **AWG override**:

- With the override off, the peer inherits the interface profile.
- With the override and peer **AWG obfuscation** on, the peer uses its own
  J/S/H/I values.
- With the override on and peer obfuscation off, the peer uses plain WireGuard.

This permits standard WireGuard and AmneziaWG peers to share one interface. The
applicable profile must still match each remote peer.

## Performance and status

WireGuard performance depends on the Network connected to
[Network](@port/right/Network). Connect this port directly to a System outbound
network node to get full performance. Middleware nodes on this path can reduce
throughput. We are working to remove this limitation.

**Batch size** controls packet batching for both tunnel processing and UDP. Zero
uses the default. A smaller positive value retains fewer packet buffers but can
reduce maximum throughput.

The status reports the state, public key, listen port, attachment state,
effective owned IPs, peer counts, effective MTU, packet and byte totals, and
latest handshake time. Use the **Error** field to diagnose invalid peer objects,
key problems, UDP bind failures, and incompatible MTU values.

Applying a valid menu change updates the active device. Peer, key, address, or
AWG changes can interrupt handshakes and active traffic. An invalid menu change
is rejected and leaves the saved menu configuration unchanged. An invalid
peer-list object removes that source's previous peer contribution so that stale
peers do not remain active. The state changes to `invalid config` or
`invalid object config` and the **Error** field gives the cause.

When a fixed WireGuard endpoint goes through a SOCKS client Network, standard
SOCKS5 UDP association is not sufficient because the proxy chooses the relay
port. Use a transport that preserves a fixed UDP listen port, such as a SOCKS
client URL with `?gost`, or use a topology in which the peer can learn the
endpoint dynamically.

## Graph example

WireGuard has separate packet, UDP transport, and optional control paths. The
VTun supplies client packets. The default Network carries menu peers. WGConfig
adds a peer group that uses its own UDP Network.

```graph
{
  "Client": {
    "Class": "almagest/HttpClient",
    "Pos": "{\"x\":31,\"y\":220}",
    "Links": ["Network -> [Client IP stack] Network"],
  },
  "Client IP stack": {
    "Class": "almagest/VTun",
    "Pos": "{\"x\":245,\"y\":220}",
    "Links": ["Tun -> [WireGuard tunnel] Tun"],
  },
  "Extra peers": {
    "Class": "almagest/WGConfig",
    "Pos": "{\"x\":245,\"y\":20}",
    "Links": [
      "WireGuard -> [WireGuard tunnel] WGCtrl",
      "Network -> [Controller UDP network] Network",
    ],
  },
  "WireGuard tunnel": {
    "Class": "almagest/WireGuard",
    "Pos": "{\"x\":525,\"y\":220}",
    "Links": ["Network -> [Default UDP network] Network"],
  },
  "Controller UDP network": {
    "Class": "almagest/OutNetwork",
    "Pos": "{\"x\":525,\"y\":20}",
  },
  "Default UDP network": {
    "Class": "almagest/OutNetwork",
    "Pos": "{\"x\":755,\"y\":220}",
  },
}
```
