# default_tun_zero_prefix_routes

## Setup

This case runs in a privileged Docker container. It creates a native TUN
interface and changes routes only in the container network namespace. Run it
with:

```sh
E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun" \
  ./e2e/run.sh default_tun_zero_prefix_routes
```

## Initial daemon config

The config creates one `DefaultTun` with these explicit TUN routes:

- `0.0.0.0/0`
- `::/0`

These prefixes exercise the zero-prefix route conversion in `sysnet-linux`.

## Expected behavior

- The `DefaultTun` node becomes ready.
- The native TUN interface has an IPv4 default route in the main table.
- The native TUN interface has an IPv6 default route in the main table.
- The daemon does not log a `DefaultTun` configuration failure.

The container is disposable because the IPv4 route replaces the container's
ordinary main-table default route during this test.
