Runs one embedded `github.com/asciimoth/wgo` WireGuard device.

The left `Tun` port consumes a TUN endpoint. The left `Peers` port accepts any
number of `pasta/object` links. The left `WGCtrl` port provides detached
controller APIs to WGConfig nodes and typed middleware. The right `Network`
port supplies the default UDP network used by menu and object peer endpoints.
The node keeps one embedded device while links are rewired and replaces only
the active TUN or default UDP bind.

After the device becomes active, removing or replacing its TUN does not stop
the WireGuard peers or UDP transport. The node reports `waiting for TUN` and
keeps the device alive until a replacement TUN arrives. TUN `EventUp` and
`EventDown` values do not control the shared device; the node controls those
state changes. TUN MTU updates still reach the embedded device.

Each object input must have the same `{"peers": [...]}` structure used by the
WireGuard node config. The node keeps the latest object received from each
attached link and merges all peer lists into the `wgo` device. A normalized
WireGuard public key identifies a conflict. Peers configured in the node menu
always win conflicts with object peers. Object links use stable link-ID order
for conflicts between object sources. Detaching a link removes only its peer
contribution; reattaching requests the producer's current value. Object values
stay owned by their producer and are not copied into the WireGuard node's saved
config.

WGConfig peers coexist with menu and object peers. WireGuard reconciles only
the peer public keys owned by its menu and object inputs; it does not use a
global replace-all operation that would delete controller-owned peers. Each WG
control link gets an independently closeable API. Removing a direct link or
switching a `pasta/Select` path releases only the peers and named transports
tracked through that API.

WG control clients can also suggest owned interface IPs and routes. The node
keeps the latest suggestion from each attached WG control link and merges all
of them with addresses and peer routes from the node config. Menu values come
first. Controller values follow in stable link-ID order, and duplicate
normalized prefixes are removed. Controller suggestions are runtime state:
they are not saved in the WireGuard node config, and they are removed when
their link detaches or when a controller sends an empty suggestion.

WG control clients can request an acknowledged interface update. The provider
validates the addresses and routes and waits for the resulting TUN generation
to be attached before the client applies peer or transport changes that depend
on them.

The device private key is read from the daemon secrets storage. The config
field `privateKeySecret` stores only the secret name; the private key value is
never saved back into node config or shown in the node menu. If the field is
empty, or if this node cannot read the named secret, WireGuard generates a new
random private key, stores it with `SecretsStorage.NewSecret`, and replaces
`privateKeySecret` with the returned secret name. Old configs that still contain
`privateKey` are migrated once by storing that value as a new secret; future
saves write only `privateKeySecret`.

Peer preshared keys use the same storage model. The peer field
`presharedKeySecret` stores only the secret name; the preshared key value is not
saved into node config or shown in the node menu. Old configs and old form
clients that still send `presharedKey` are migrated once by storing that value
as a new secret; future saves write only `presharedKeySecret`. A missing or
inaccessible peer preshared-key secret is rejected as invalid config instead of
silently removing the peer PSK.

When the configured secret is changed, renamed, deleted, or has its node access
changed, the daemon sends `SecretValueChanged` to the node. The node refetches
the private-key and peer preshared-key secrets and applies them with the same
runtime generation checks used for form submissions. Invalid secret values are
rejected as invalid config. Missing or inaccessible private-key secrets are
replaced by a newly generated private-key secret; missing or inaccessible peer
preshared-key secrets are reported as invalid config.

The effective menu and WG control owned IPs are emitted upstream as `TunConfig`
address suggestions for the attached TUN provider. Effective menu and object
peer `allowedIPs`, together with WG control routes, are emitted as `TunConfig`
route suggestions. The peer `allowedIPs` are also installed into the WireGuard
device as crypto-routing rules.

The optional menu field `dns` accepts one DNS server IPv4 or IPv6 address.
WG control links can also suggest one DNS server. The last current suggestion
received from these links wins, but a value in the menu field always has
priority. WireGuard emits the effective value upstream as `TunConfig.DNS`.
This setting is for a simple TUN path, such as WireGuard connected directly to
VTun. It does not configure the WireGuard protocol or the WireGuard UDP
transport. Use the `almagest/dns` link type and DNS nodes when the graph needs
DNS routing, multiple resolvers, caching, or policy.

The `AWG obfuscation` checkbox in the config menu controls the device-level
AmneziaWG profile. The checkbox is off by default. When it is off, the menu
hides the Jc/Jmin/Jmax, S1-S4 padding, H1-H4 header, and I1-I5 initiation-packet
fields. The backend uses the plain WireGuard-compatible defaults while the
checkbox is off, even if hidden fields contain values from an earlier profile.
The values remain in the saved config so they are available if obfuscation is
enabled again. A legacy config that has a non-default AWG profile and no toggle
keeps that profile enabled during migration.

`Batch size` is passed to both `wgo.DeviceOptions.BatchSize` and
`batchudp.StdNetBindOptions.BatchSize`. Use `0` to keep library defaults.
Smaller positive values reduce retained packet buffers and may reduce peak
throughput.

Peer entries expose the same controls behind `AWG override` and `AWG
obfuscation`. When the override is off, the peer inherits the device-level
profile. When the override is on and obfuscation is on, the peer's J/S/H/I
values are a peer-local profile. When the override is on and obfuscation is
off, the backend applies the plain WireGuard-compatible defaults to that peer.
This last combination lets one peer disable obfuscation when the device profile
is enabled. Legacy peers with `amneziaWGOverride` and no obfuscation toggle keep
their override enabled.

The status menu subscribes to the embedded device's runtime stats and shows
live readonly counters: runtime peers, active peers, connected peers, RX/TX
bytes, RX/TX packets, and the latest handshake time.

When WireGuard peers use fixed endpoint ports through a SOCKS client network,
standard SOCKS5 UDP ASSOCIATE is not enough: the SOCKS server assigns the UDP
relay port, and the protocol does not let the client request the relay listen port.
Use a transport that preserves a fixed UDP listening port, such as the SOCKS
client URL option `?gost`, or configure the WireGuard topology so peer endpoints
can be learned dynamically.
