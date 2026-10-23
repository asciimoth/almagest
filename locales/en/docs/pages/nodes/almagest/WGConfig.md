Adds a separately managed peer list to an existing WireGuard node. WGConfig does
not create a WireGuard interface. It also does not configure the device private
key, listen port, interface addresses, or system routes.

Connect [WireGuard](@port/right/WireGuard) to the **WGCtrl** connector of a
WireGuard node. Connect [Network](@port/right/Network) to give these peers a UDP
path. You can connect multiple WGConfig nodes to one WireGuard node. Each
WGConfig manages only its own peers and UDP path.

Do not configure the same public key in more than one peer source on the same
WireGuard node. The public key identifies the peer, so independent sources can
replace or remove each other's settings if they use the same key.

## Peer settings

Each peer supports these settings:

- **Public key** is required. Enter the peer's standard WireGuard public key.
- **Preshared key secret** is optional. Select a secret that contains a valid
  WireGuard preshared key. WGConfig saves and shows only the secret name. It
  does not save or show the key value. If the secret changes or is renamed,
  WGConfig reloads it. Old configurations that contain an inline preshared key
  are moved to secret storage when they are loaded.
- **Endpoint** is optional. For a fixed endpoint, use an IP address and UDP
  port, such as `192.0.2.10:51820` or `[2001:db8::10]:51820`. A peer without an
  endpoint cannot start outgoing communication until WireGuard learns an
  endpoint from authenticated incoming traffic.
- **Allowed IPs** controls which destination prefixes WireGuard sends to this
  peer. Enter IPv4 or IPv6 prefixes, separated by spaces, commas, semicolons, or
  new lines. A bare IP address becomes a host prefix. This setting changes
  WireGuard crypto-routing, but WGConfig does not add routes to the operating
  system.
- **Keepalive** is the persistent keepalive interval in seconds. Use `0` to
  disable it. Values below 0 or above 65535 are changed to `0`.

Invalid keys, duplicate public keys, invalid prefixes, invalid endpoints, and
invalid AWG profiles are rejected. A missing or inaccessible preshared-key
secret is also rejected. The previous valid peer list stays active after a
rejected edit.

## Network and detach behavior

WGConfig creates one UDP transport for all peers in its list. This transport is
separate from the WireGuard node's default UDP transport and from the transports
of other WGConfig nodes. All fixed peer endpoints in this WGConfig use the
connected [Network](@port/right/Network).

If [Network](@port/right/Network) is disconnected, WGConfig removes its UDP
transport and removes fixed endpoints from its active peer settings. The peers
stay installed on the WireGuard device, but their traffic is dropped. The label
reports `network detached; peer traffic is dropped`. Reconnecting **Network**
creates the transport again and restores the configured endpoints.

Disconnecting [WireGuard](@port/right/WireGuard) removes this WGConfig's peers
and UDP transport from that WireGuard node. It does not remove menu peers,
Object peers, or peers from other WGConfig nodes. Reconnecting applies the peer
list again. The same ownership rule applies when a selector changes the active
WireGuard connection.

## AmneziaWG peer overrides

By default, a peer inherits the AmneziaWG profile of the WireGuard device.
Enable **AWG override** to give the peer its own profile.

With the override enabled, **AWG obfuscation** selects the effective profile:

- When obfuscation is enabled, the peer uses its Jc/Jmin/Jmax, S1-S4, H1-H4, and
  I1-I5 values.
- When obfuscation is disabled, the peer uses plain WireGuard-compatible values.
  This lets one peer disable obfuscation while the WireGuard device uses it.

Disabling obfuscation hides but preserves the peer profile values. Enabling it
again restores those values. An old peer configuration that has an AWG override
but no obfuscation switch keeps the override enabled.
