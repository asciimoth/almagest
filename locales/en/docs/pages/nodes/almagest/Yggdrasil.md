# Yggdrasil

Connect a TUN to the Yggdrasil IPv6 mesh. This node embeds the
[asciimoth/ygg library](https://github.com/asciimoth/ygg).

## Connections

- Connect the left **Tun** port to a VTun or another TUN provider.
- Connect the right **default** port to an upstream Network.
- Add more upstream ports with **Upstreams**, one `name=host-pattern` entry per
  line. For example, `onion=*.onion` creates an **onion** port. Connect it to a
  Tor network. A disconnected mapped port blocks matching hosts; it does not use
  the default network.

The node sends its IPv6 address, the `200::/7` route, and an MTU suggestion to
its TUN provider. Use the VTun Network port for applications or another
Yggdrasil node.

## Settings

**Enabled** starts or stops the mesh runtime. **Peer URLs** accepts persistent
TCP or TLS peers, one URL per line, such as `tls://peer.example:443`. **Listener
URLs** accepts local TCP or TLS listener addresses. Listeners are optional for
outbound peers. The initial configuration does not select public peers or access
the host network automatically.

**Link-local interfaces** enables multicast discovery on the attached default
Network. Enter a JSON array such as
`[{"regex":"^eth0$","beacon":true,"listen":true}]`. The regular expression
selects network interfaces; `beacon` announces this node and `listen` accepts
announcements. Optional `port` and `priority` values set beacon parameters. An
empty array disables discovery. The Network provider must expose usable
link-local interfaces and multicast. With the current Gonnect release, native
and daemon Network wrappers lose the IPv6 interface zone during TCP bind. Use a
provider that preserves scoped link-local addresses until that upstream issue is
fixed.

**Public-peer autopeering** fetches peer lists and adds a matching peer when the
connected peer counts fall below the configured minimums. Enter one HTTP or
HTTPS URL per line in **Public-peer sources** or use `BUILTIN` for the library's
built-in list. Add at least one **Public-peer country** and one **Public-peer
scheme** (`tcp` or `tls`); selection stays idle without both. The **Peer-list
fetch interval** and **Autopeer check interval** accept Go durations such as
`1h` and `1m`. Peer-list requests use the Network port for their source host,
including mapped upstream ports. A disconnected matching port blocks that
source.

**NodeInfo jumper** can add a direct peer link after traffic reaches another
node through the mesh. To let other jumper nodes connect here, provide public
TCP or TLS listener URLs in **Jumper addresses**. These URLs are published in
NodeInfo while the jumper is enabled; they must lead to this node's listener.
The jumper check interval and link timeout accept Go durations. **NodeInfo
(JSON)** publishes custom fields from a JSON object. **NodeInfo privacy** omits
the library's build details. When the jumper is disabled, existing direct links
may remain until they close.

**Identity key secret** selects a node-accessible secret with a hexadecimal
Ed25519 seed (32 bytes) or private key (64 bytes). A new node creates its own
secret. Saved configuration contains the secret name, not the key. Link changes
and disable/enable keep the identity. An inaccessible configured secret reports
an error instead of generating a replacement identity.

**State** reports local runtime readiness. `up` means that the TUN is attached
and an upstream network is available. It does not guarantee that a remote peer
is connected or reachable. The status menu also shows the IPv6 address,
connected peer count, loaded public-peer candidates, active link-local interface
count, and jumper state.
