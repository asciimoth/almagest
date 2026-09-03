# Amnesia

Amnesia combines an Amnezia API client with one private userspace WireGuard
device. Connect a TUN provider to the left **Tun** port and an outgoing network
to the right **Network** port. The outgoing network carries both API traffic
and WireGuard UDP traffic.

The node owns the complete WireGuard runtime. It does not store a WireGuard
private key or any other WireGuard configuration. For an API-v2 service key,
the Amnezia client generates a new WireGuard identity when the node creates a
connection. Rebuilding the connection can generate another identity. Static
self-hosted and native profiles use the private key contained in the selected
connection secret because their server already associates that key with the
profile.

The installation UUID and encrypted proxy-discovery cache use the node's KV
namespace. These client values survive daemon restarts. WireGuard device state,
keys, peers, counters, and handshakes do not.

The node sends profile tunnel addresses, allowed-IP routes, and the first DNS
server to the attached TUN provider. This gives a simple TUN setup the same
address and DNS configuration that an AmnesiaCtrl and WireGuard pair provides.
The connection event is always enabled for service imports.

## Troubleshooting

An Amnezia service endpoint for a selected country can be temporarily
unavailable even when the API returns a valid profile. A typical symptom is a
WireGuard handshake that retries continuously while DNS and other tunnel
traffic time out. If this occurs, select another server country and try the
connection again first. Do not delete the installation UUID or recreate the
node only to test this condition; these actions do not repair an unavailable
country endpoint and can consume the service's client limit.
