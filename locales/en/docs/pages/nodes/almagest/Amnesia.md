# Amnesia

Amnesia combines an Amnezia API client with one private userspace WireGuard
device. Connect a TUN provider to the left **Tun** port and an outgoing network
to the right **Network** port. The outgoing network carries both API traffic
and WireGuard UDP traffic. Connect DNS consumers to the left **DNS** port.

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

If the API server requests a captcha during a service import, the node pauses
the import and shows a warning popup. Open the node menu and select **Complete
captcha** at the top. The dialog shows the server-provided image and message.
Canceling the dialog keeps the import paused so you can open it again. A
submitted answer resumes the same import and keeps its generated WireGuard
identity. An invalid answer can replace the dialog with a refreshed challenge.

The **DNS** port exposes a resolver that uses the usable DNS server IP addresses
from the active profile in profile order. A server is usable when the profile
has a local address of the same IP family and an allowed-IP route to the server.
DNS requests use raw UDP packets on the same TUN data path as other WireGuard
traffic. The node does not create an internal IP stack, VTun, or TUN splitter.
Incoming packets are forwarded unchanged. Outgoing graph TUN packets use one
additional bounded buffer copy so runtime shutdown and profile replacement can
stop blocked reads safely.

The DNS interface is available only while the current WireGuard runtime is up.
If a TUN or Network link is removed, or a menu change selects another country,
the node closes the old per-link DNS wrappers before it starts replacement.
When the new runtime is ready, the node sends a fresh wrapper on every attached
DNS link. Removing and reattaching only a DNS link does not rebuild the VPN.

The built-in tunnel DNS client supports UDP only. It returns truncated DNS
responses as received and does not make a TCP fallback connection. It also
does not reassemble fragmented IP packets. These limits keep the data path
small and avoid an internal network stack. Use another DNS transport when a
resolver must support large responses that require TCP.

## Troubleshooting

An Amnezia service endpoint for a selected country can be temporarily
unavailable even when the API returns a valid profile. A typical symptom is a
WireGuard handshake that retries continuously while DNS and other tunnel
traffic time out. If this occurs, select another server country and try the
connection again first. Do not delete the installation UUID or recreate the
node only to test this condition; these actions do not repair an unavailable
country endpoint and can consume the service's client limit.
