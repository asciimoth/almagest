AmnesiaCtrl is deprecated. Use the [Amnesia](@docs/node/almagest/Amnesia) node
for new configurations.

AmnesiaCtrl imports an Amnezia VPN connection and adds its peer to an attached
WireGuard node. The WireGuard node continues to control the device private key,
TUN connection, listen settings, and UDP transport. AmnesiaCtrl removes only the
peer that it added when you disable or disconnect the node.

Connect the [WireGuard](@port/right/WireGuard) port to the control port of a
WireGuard node. Store one of these values in a secret, and select the secret in
**Connection secret**:

- an Amnezia `vpn://` service key
- a native WireGuard or AmneziaWG configuration

A native configuration must contain the same client identity as the attached
WireGuard node. If the identities do not match, the import fails. For a service
key, AmnesiaCtrl sends the public key of the attached WireGuard device to the
service. The node does not generate a separate client key. Reconnects and daemon
restarts continue to use the same identity.

Connect the [Network](@port/right/Network) port for a service-key import or for
a native profile that has pre-connect actions. This connection carries only
service API and pre-connect traffic. The VPN peer always uses the default UDP
network of the WireGuard node. A native profile without pre-connect actions does
not need the [Network](@port/right/Network) connection.

AmnesiaCtrl sends these profile settings to the WireGuard node before it adds
the peer:

- tunnel addresses
- allowed-IP routes
- the first non-empty DNS server
- the profile MTU limit

The WireGuard node combines these settings with its other controller and menu
settings, and then sends the result to its TUN provider.

## Service options

**Server country** selects a country for a service import. Use a two-letter ISO
3166-1 country code, such as `de`. Leave the field empty to let the service
select the country.

Enable **Send connect event** if the service must receive its connect event
during import. **Reconnect** imports the profile again and replaces the peer,
but it does not change the WireGuard identity.

For a custom service deployment, set **Gateway URL** and, if required, select
the secret that contains the gateway RSA public key. Select a TLS configuration
for an HTTPS gateway.

The node keeps its Amnezia installation UUID and encrypted proxy cache in KV
storage when that storage is available. This data survives daemon restarts and
node renames. The status menu shows the installation UUID and whether it is
persistent.

AmnesiaCtrl cannot complete interactive imports. If the service requests a
CAPTCHA or another interaction, the import fails. Use the
[Amnesia](@docs/node/almagest/Amnesia) node if the service requires a CAPTCHA.

## Troubleshooting

Some services can return a valid profile but reject handshakes from an old
client public key. In this condition, the status can show **handshaking**, TX
bytes can increase, RX bytes can stay at zero, and **Latest handshake** can stay
empty. **Reconnect** does not correct this condition because it uses the same
WireGuard identity.

To create a new identity, clear **Private key secret** on the attached WireGuard
node and apply the configuration. The WireGuard node generates and stores a new
private key. Then select **Reconnect** on AmnesiaCtrl so that the service
receives the new public key.

This procedure changes the identity for all peers and controllers that use the
WireGuard node. Use a dedicated WireGuard node if this VPN connection must
rotate its identity independently.
