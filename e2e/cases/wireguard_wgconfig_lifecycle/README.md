# wireguard_wgconfig_lifecycle

## Setup

This case runs two userspace WireGuard devices in one daemon. Each device has an
attached `WGConfig` node that owns its only peer and its peer UDP transport. The
graph uses ordinary loopback UDP sockets and userspace `VTun` nodes. It does not
need privileged Docker networking.

## Expected Behavior

- Both `WGConfig` nodes install one peer and pass HTTP traffic through the
  tunnel.
- An invalid peer form reports an error, keeps the last valid runtime peer, and
  does not interrupt traffic.
- Removing a `WGConfig` network link drops its transport. Restoring the link
  recovers the first HTTP request.
- Removing a controller link removes the controller-owned runtime. Restoring the
  link reinstalls the peer and recovers the first HTTP request.
- Repeated removal and restoration of both links converges when the links are
  restored in either order.
- All peer ownership and link state survive a daemon restart.
