# DefaultTun nested WireGuard bypass

## Setup

This case runs in a privileged Docker container. It replaces the container's
default route with a network-namespace router and starts a Linux kernel
WireGuard peer behind that router. Run it with:

```sh
E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun" \
  ./e2e/run.sh default_tun_nested_wireguard_bypass
```

The inner WireGuard link uses `10.250.0.1/32`. The case fixes the system random
seed so the DefaultTun allocator selects the same host address from its
`10.250.0.0/24` pool. The native TUN must keep only the explicit `/32` address.
This is a regression test for a fallback address that has the same host IP with
a different prefix length.

## Graph

The graph joins a native `DefaultTun` and an application `VTun`. It sends both
inputs through a `TunSplitter` and a userspace WireGuard tunnel. WireGuard uses
`LogNetwork` and `OutNetwork` for its public UDP transport. A separate HTTP
client also uses its own `OutNetwork` path.

## Coverage

The test verifies these behaviors:

- Downstream address, route, and MTU data can rebuild an attached native TUN
  without blocking startup or leaving competing readers on the device.
- An allocator-selected `/24` fallback does not conflict with an explicit
  address for the same host IP that uses a `/32` prefix.
- The WireGuard endpoint remains reachable through the original main-table
  default route while unmarked traffic selects `DefaultTun`.
- Host HTTP, application-VTun HTTP, and application-VTun DNS traffic work
  through one shared WireGuard address.
- Concurrent host flows do not break flow-aware TUN joining.
- A separate `OutNetwork` HTTP request bypasses `DefaultTun`.
- Removing `DefaultTun` does not stop the secondary application VTun.
- Recreating and reattaching `DefaultTun` restores host traffic without adding a
  TUN route to the main table.
- Daemon shutdown keeps the original container default route.

The topology reproduces the route-recursion condition that occurs when an
application-bypass lookup finds a main-table default route through the same TUN.
