# DefaultTun nested WireGuard bypass

## Setup

This case runs in a privileged Docker container. It replaces the container's
default route with a network-namespace router and starts a Linux kernel
WireGuard peer behind that router. Run it with:

```sh
E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun" \
  ./e2e/run.sh default_tun_nested_wireguard_bypass
```

## Graph

The graph joins a native `DefaultTun` and an application `VTun`. It sends both
inputs through a `TunSplitter` and a userspace WireGuard tunnel. WireGuard uses
`LogNetwork` and `OutNetwork` for its public UDP transport. A separate HTTP
client also uses its own `OutNetwork` path.

## Coverage

The test verifies these behaviors:

- Downstream address, route, and MTU data can rebuild an attached native TUN
  without blocking startup or leaving competing readers on the device.
- The WireGuard endpoint remains reachable through the original main-table
  default route while unmarked traffic selects `DefaultTun`.
- Host HTTP, application-VTun HTTP, and application-VTun DNS traffic work
  through one shared WireGuard address.
- Concurrent host flows do not break flow-aware TUN joining.
- A separate `OutNetwork` HTTP request bypasses `DefaultTun`.
- Removing `DefaultTun` does not stop the secondary application VTun.
- Recreating and reattaching `DefaultTun` restores host traffic without adding
  a TUN route to the main table.
- Daemon shutdown keeps the original container default route.

The topology reproduces the route-recursion condition that occurs when an
application-bypass lookup finds a main-table default route through the same
TUN.
