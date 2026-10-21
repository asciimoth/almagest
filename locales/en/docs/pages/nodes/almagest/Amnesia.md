Creates an Amnezia VPN connection and owns its WireGuard device. Use this node
when you want one node to import the connection, create the tunnel, and provide
tunnel DNS.

Connect a TUN provider to **Tun** and an outgoing network to **Network**. The
node uses the outgoing network for Amnezia API requests, profile pre-connect
traffic, and WireGuard UDP traffic. Connect DNS consumers to **DNS** if they
must resolve names through the VPN.

## Connection configuration

Store one of these values in a secret, then select the secret in **Connection
secret**:

- an Amnezia service `vpn://` key;
- a self-hosted guest `vpn://` profile;
- a native WireGuard or AmneziaWG client configuration.

**Server country** selects the service endpoint for a service key. Use a
two-letter ISO 3166-1 country code, such as `de` or `nl`. Static self-hosted and
native profiles ignore this setting.

For a custom service deployment, set **Gateway URL** and select a **Gateway
public key secret**. The gateway URL must use HTTP or HTTPS. Select a TLS config
when the HTTPS gateway needs custom trust or client authentication. These
gateway and TLS settings apply only to service-key imports.

Service-key imports always notify the service that the connection is active. The
node generates a WireGuard identity when it imports a new service connection.
For a service key, changing the country or other connection settings can
register a new identity. Detaching and later reattaching **Tun** or **Network**
can also register a new identity. Self-hosted and native profiles use the
private key in the selected connection secret.

The node does not save WireGuard keys, peers, counters, or handshake state. It
does save its Amnezia installation UUID and encrypted proxy-discovery cache when
persistent storage is available. This UUID stays the same after daemon restarts
and node renames. The status menu shows the current UUID.

## Tunnel configuration and MTU

The node sends the profile addresses, allowed-IP routes, effective MTU, and the
first DNS server to the TUN provider. The node limits the effective MTU to the
smaller of these values:

- the MTU ceiling in the Amnezia profile;
- the safe tunnel payload size reported by the outgoing network.

The status menu shows the effective MTU, the profile ceiling, the outgoing
network's UDP payload limit, and the selected constraint. If the outgoing
network does not report a UDP payload limit, the node uses the profile MTU and
logs a warning. The node rejects an IPv6 profile if the calculated safe MTU is
less than the IPv6 minimum.

## Tunnel DNS

The **DNS** output is available while the VPN is up. It sends UDP DNS requests
through the WireGuard tunnel. The node tries usable profile DNS servers in
profile order. A DNS server is usable only when the profile has a tunnel address
of the same IP family and an allowed-IP route to that server.

The tunnel DNS client does not retry a truncated response over TCP and does not
reassemble fragmented IP packets. Use a different DNS transport if the resolver
must support responses that require TCP.

If the VPN connection changes, existing DNS outputs close until the replacement
connection is ready. Removing or adding only a DNS link does not reconnect the
VPN.

## CAPTCHA requests

If the service requests a CAPTCHA, the node pauses the import and shows a
warning. Open the node menu and select **Complete captcha**. The dialog shows
the image and message from the service. If you cancel the dialog, the import
stays paused and you can open the dialog again. A submitted answer continues the
same import. If the answer is not accepted, the service can send a new
challenge.

## Troubleshooting

The selected service endpoint can be unavailable even if the service returned a
valid profile. The node can show **up**, **TX bytes** can increase, and **RX
bytes** can stay at zero while tunnel requests time out. Select a different
server country and try again. Do not delete the installation UUID or recreate
the node for this test. These actions do not repair the endpoint and can use
another service client slot.
