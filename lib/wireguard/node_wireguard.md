Runs one embedded `github.com/asciimoth/wgo` WireGuard device.

The left `Tun` port consumes a TUN endpoint. The right `Network` port supplies
the UDP network used by WireGuard peer endpoints. The node creates the embedded
device lazily once runtime links exist, then keeps that device while links are
rewired, replacing only the active TUN or UDP bind.

The device private key is read from the daemon secrets storage. The config
field `privateKeySecret` stores only the secret name; the private key value is
never saved back into node config or shown in the node menu. If the field is
empty, or if this node cannot read the named secret, WireGuard generates a new
random private key, stores it with `SecretsStorage.NewSecret`, and replaces
`privateKeySecret` with the returned secret name. Old configs that still contain
`privateKey` are migrated once by storing that value as a new secret; future
saves write only `privateKeySecret`.

When the configured secret is changed, renamed, deleted, or has its node access
changed, the daemon sends `SecretValueChanged` to the node. The node refetches
the secret and applies the key with the same runtime generation checks used for
form submissions. Invalid secret values are rejected as invalid config; missing
or inaccessible secrets are replaced by a newly generated private-key secret.

`ownedIPs` are emitted upstream as `TunConfig` address suggestions for the
attached TUN provider. Peer `allowedIPs` are installed into the WireGuard
device as crypto-routing rules and are also emitted upstream as `TunConfig`
route suggestions for providers that install host routes.

The config menu also exposes device-level AmneziaWG obfuscation controls:
Jc/Jmin/Jmax, S1-S4 padding values, H1-H4 header ranges, and I1-I5
initiation-packet obfuscation chains. Leaving those values at defaults keeps
plain WireGuard behavior.

`Batch size` is passed to both `wgo.DeviceOptions.BatchSize` and
`batchudp.StdNetBindOptions.BatchSize`. Use `0` to keep library defaults.
Smaller positive values reduce retained packet buffers and may reduce peak
throughput.

Peer entries expose the same AmneziaWG controls behind `AWG override`. When the
override is off, the peer inherits the device-level AmneziaWG profile. When it
is on, the peer's J/S/H/I values are applied as a peer-local override.

The status menu subscribes to the embedded device's runtime stats and shows
live readonly counters: runtime peers, active peers, connected peers, RX/TX
bytes, RX/TX packets, and the latest handshake time.

When WireGuard peers use fixed endpoint ports through a SOCKS client network,
standard SOCKS5 UDP ASSOCIATE is not enough: the SOCKS server assigns the UDP
relay port, and the protocol does not let the client request the relay listen port.
Use a transport that preserves a fixed UDP listening port, such as the SOCKS
client URL option `?gost`, or configure the WireGuard topology so peer endpoints
can be learned dynamically.
