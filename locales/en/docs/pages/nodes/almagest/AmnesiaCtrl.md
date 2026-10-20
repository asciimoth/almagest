# AmnesiaCtrl

Imports an Amnezia VPN connection and manages its peer on an attached
WireGuard node.

The **WireGuard** port supplies the shared WireGuard device. AmnesiaCtrl owns
only the peer that it adds through this port. The WireGuard node continues to
own the device private key, TUN attachment, listen settings, and UDP
transports. For an API-v2 service key, AmnesiaCtrl sends the attached WireGuard
device's existing public key to the service. It does not generate a new
WireGuard client key. The key remains the same across imports, **Reconnect**
requests, and daemon restarts. This differs from a standalone wgo import
without an attached device, which generates a new client key for each import.

Some service backends can return a valid profile but stop accepting handshakes
for an old client public key. In this state, the AmnesiaCtrl status can show
**up** and **handshaking**, transmit bytes can increase, receive bytes remain
zero, and the latest handshake stays empty. **Reconnect** alone can reuse the
same unusable identity.

To recover, clear **Private key secret** on the attached WireGuard node and
apply its configuration. The WireGuard node generates and stores a new private
key. Then press **Reconnect** on this AmnesiaCtrl node so the service receives
the new public key. You do not need to delete either node or detach their
links. Rotating the private key changes the identity of the complete WireGuard
device and affects all peers and controllers that share it. Use a dedicated
WireGuard node when a service connection must rotate its identity
independently.

AmnesiaCtrl also sends the profile interface addresses as owned-IP suggestions
and the first non-empty profile DNS server as a DNS suggestion. The WireGuard
node merges the addresses with its other controller suggestions and menu owned
IPs. It uses the last DNS suggestion received from its controller links unless
its menu DNS field is set. It then sends the effective interface config to its
TUN provider. AmnesiaCtrl waits for the WireGuard node to accept this config
and attach the resulting TUN generation before it adds or activates the
profile peer.

The **Network** port is used only for Amnezia service API and profile
pre-connect traffic. It is needed for API-v2 service keys and profiles that
declare pre-connect actions. It is not installed as the peer UDP transport.
The peer always uses the WireGuard node's default UDP transport.

Store the `vpn://` URL or native WireGuard/AmneziaWG configuration in a secret,
then select that secret in the configuration form. For a custom API-v2
deployment, you can also set its gateway URL and a secret that contains its RSA
public key. Select a TLS config for HTTPS gateway connections.

The node stores its Amnezia installation UUID and encrypted proxy cache in a
stable KV subtree when KV storage is available. It saves the subtree identifier
in the node config, so workspace ID changes and node renames do not discard the
session. The status menu shows the UUID and reports when it uses persistent
storage.

Interactive import is disabled. If the service requests a CAPTCHA or another
interaction, the node enters the error state. Change the connection settings or
press **Reconnect** after you resolve the cause.
