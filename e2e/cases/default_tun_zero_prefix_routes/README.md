# DefaultTun route isolation

## Setup

This case runs in a privileged Docker container. It creates a native TUN
interface and an IPv6 gateway only in the container network namespace. Run it
with:

```sh
E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun" \
  ./e2e/run.sh default_tun_zero_prefix_routes
```

## Coverage

The initial `DefaultTun` config has dual-stack subnet addresses and these
explicit route suggestions:

- IPv4 and IPv6 default routes.
- IPv4 and IPv6 host routes.

The test verifies these behaviors:

- `DefaultTun` does not add explicit or connected TUN routes to the main table.
- The original IPv4 and IPv6 main-table defaults stay unchanged.
- Unmarked IPv4 and IPv6 traffic selects the TUN policy table.
- Traffic with an application-bypass mark selects the original main route.
- Route-only and address-only live updates do not leak routes into the main
  table.
- Deleting and recreating `DefaultTun` restores the same route isolation.
- Daemon shutdown removes policy routing and keeps the original defaults.

This is a regression test for `sysnet-linux` handling of routes that the
kernel creates when it assigns TUN addresses.
