# tailscale_headscale

## Setup

The case starts one local Headscale server and two real `almagestd` processes
in the shared e2e container. It does not need privileged Docker networking.
Headscale uses reusable pre-authentication keys and allocates addresses from
`100.64.0.0/10`.

Each daemon has separate `OutNetwork` nodes for the Tailscale control path and
the WireGuard default UDP path. DERP is disabled, so the data-plane check must
use a direct UDP peer path.

## Graphs

Each daemon contains a `TailscaleCtrl`, `WireGuard`, and `VTun` node. The
controller sends peers and interface addresses to WireGuard through `WGCtrl`.
Daemon A also sends MagicDNS to a `DnsQuery` and a `DnsInject` node. Daemon B
sends its ACL-derived object to a `NetworkFirewall` node.

## Expected behavior

- Both Tailscale and WireGuard nodes reach `up`.
- The Tailscale menu reports a direct UDP peer path and the Headscale ACL.
- `DnsQuery` resolves `beta.e2e.internal` through the Tailscale DNS port.
- An HTTP client on daemon A resolves that same name through `DnsInject` and
  reaches an HTTP server behind daemon B's WireGuard tunnel.
- Daemon B's `NetworkFirewall` accepts the controller's generated policy.
