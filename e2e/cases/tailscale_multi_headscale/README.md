# tailscale_multi_headscale

## Setup

The case starts two independent Headscale servers and one `almagestd` process.
The daemon contains four fresh TailscaleCtrl nodes. Two nodes connect to each
Headscale server, and every controller has its own WireGuard and Network nodes.

The two servers use separate databases, TLS authorities, address ranges, and
MagicDNS domains. This makes accidental sharing between controller instances
visible.

## Expected behavior

- All four TailscaleCtrl nodes and all four WireGuard nodes reach `up`.
- Each Headscale server registers only its two configured clients.
- Each controller sees only the peer from its own Headscale server.
- MagicDNS and HTTP traffic work independently in both tailnets.
- The four controller identity caches use four separate KV subtrees.
- After direct discovery, daemon CPU use and goroutine count stay bounded for
  two peer-probe intervals.
