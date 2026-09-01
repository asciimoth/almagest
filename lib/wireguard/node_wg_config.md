Owns a list of WireGuard peers without owning a WireGuard device.

Connect the right `WireGuard` port to the left `WGCtrl` port of a WireGuard
node. Each WGConfig uses a detached controller API. Detaching this link removes
only the peers and UDP transport owned by this WGConfig. Several WGConfig nodes
can control one WireGuard node directly or through typed middleware such as
`pasta/Select`.

The right `Network` port supplies UDP access for this node's peers. WGConfig
creates one custom named wgo transport. Its name is derived from the WGConfig
class and workspace node ID, so several WGConfig nodes do not share a transport.
Every configured fixed peer endpoint uses this transport. When the network link
is absent, WGConfig removes the transport and applies peers without fixed
endpoints. Outbound traffic for those peers is then dropped until a network is
attached again.

Peer menu fields have the same format and validation as WireGuard node peers.
Preshared keys are loaded from `presharedKeySecret`; raw key material is not
saved or shown in the menu. Legacy raw `presharedKey` values are migrated to the
daemon secrets storage on load or form apply.
