# DefaultTun preferred source routes

## Setup

This case runs in a privileged Docker container. It creates two Linux kernel
WireGuard peers behind a network-namespace router. Each peer accepts a different
IPv4 and IPv6 source identity.

Run it with:

```sh
E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun" \
  ./e2e/run.sh default_tun_source_routes
```

## Graph

One native `DefaultTun` sends traffic to a two-slot `TunSplitter`. The default
slot uses one WireGuard tunnel. The more-specific IPv4 and IPv6 prefixes use a
second WireGuard tunnel. Both tunnels use independent `OutNetwork` transports.

Each splitter output supplies different addresses and routes. The splitter
converts those values to a destination-to-source policy before it merges them
for `DefaultTun`.

## Coverage

The test verifies these behaviors:

- Linux selects the default source for ordinary IPv4 and IPv6 destinations.
- Linux selects the overlay source for more-specific IPv4 and IPv6 prefixes.
- Host TCP and UDP traffic works through both WireGuard branches.
- Concurrent traffic works through all four address and protocol combinations.
- Both WireGuard transports bypass `DefaultTun` through the original main
  routing table.
- Preferred-source routes stay out of the main table.
- Removing the overlay link removes its addresses and source routes without
  replacing or stopping the native TUN.
- Reattaching the overlay link restores its policy and traffic with a new link
  ID, so source selection does not depend on merge or address order.
- Daemon shutdown restores the original routing state.
