Runs one embedded `github.com/asciimoth/wgo` WireGuard device.

The left `Tun` port consumes a TUN endpoint. The right `Network` port supplies
the UDP network used by WireGuard peer endpoints. The node keeps the same
device while links are rewired, replacing only the active TUN or UDP bind.

`ownedIPs` are emitted upstream as `TunConfig` address suggestions for the
attached TUN provider. Peer `allowedIPs` are installed into the WireGuard
device and determine which tunneled packets are sent to each peer.

The config menu also exposes device-level AmneziaWG obfuscation controls:
Jc/Jmin/Jmax, S1-S4 padding values, H1-H4 header ranges, and I1-I5
initiation-packet obfuscation chains. Leaving those values at defaults keeps
plain WireGuard behavior.

Peer entries expose the same AmneziaWG controls behind `AWG override`. When the
override is off, the peer inherits the device-level AmneziaWG profile. When it
is on, the peer's J/S/H/I values are applied as a peer-local override.

When WireGuard peers use fixed endpoint ports through a SOCKS client network,
standard SOCKS5 UDP ASSOCIATE is not enough: the SOCKS server assigns the UDP
relay port, and the protocol does not let the client request the relay listen port.
Use a transport that preserves a fixed UDP listening port, such as the SOCKS
client URL option `?gost`, or configure the WireGuard topology so peer endpoints
can be learned dynamically.
