# Amnesia

Imports an Amnezia VPN connection and manages its peer on an attached
WireGuard node.

The **WireGuard** port supplies the shared WireGuard device. Amnesia owns only
the peer that it adds through this port. The WireGuard node continues to own
the device private key, TUN attachment, listen settings, and UDP transports.
Amnesia also sends the profile interface addresses as owned-IP suggestions.
The WireGuard node merges them with its other controller suggestions and menu
owned IPs, then sends the effective addresses to its TUN provider.
Amnesia waits for the WireGuard node to accept these addresses and attach the
resulting TUN generation before it adds or activates the profile peer.

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
