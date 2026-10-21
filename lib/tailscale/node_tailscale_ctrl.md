# TailscaleCtrl

TailscaleCtrl connects a shared **WireGuard** node to a Tailscale-compatible
control server. It uses the WireGuard node's existing private key and owns only
the Tailscale peers and named relay transport that it adds.

The controller sends its current interface addresses and applied peer routes to
the WireGuard node. WireGuard forwards them as TUN configuration. This lets a
userspace TUN select the Tailscale address for Tailscale destinations when the
TUN combines more than one upstream route.

The **Network** input carries control API requests, DNS lookup for control
hosts, STUN and DISCO discovery, and DERP-over-TLS relay traffic. Direct peer
UDP traffic uses the WireGuard node's default transport and therefore uses the
network attached to the WireGuard node. This separation lets you apply a proxy
or TLS tunnel only to Tailscale control and relay traffic.

The **DNS** output resolves live MagicDNS host records in memory. Connect it to
a DNS consumer, such as DefaultTun, DNSRouter, or DNSQuery. It does not change
the host resolver and it does not forward misses to system DNS.

The **Firewall** object output contains the packet filter received for this
node, converted to the firewall JSON format used by NetworkFirewall and
TunFirewall. It is an incoming allow list. The conversion does not restrict
outgoing traffic.

When registration needs approval, the node adds a warning popup and puts a
clickable authorization link at the top of its menu. The client continues to
poll, so approval does not block node shutdown or graph edits.

Machine and discovery identity data is sensitive. The node stores it in its
private KV subtree when daemon storage is available. The attached WireGuard
private key is not copied into that cache.
