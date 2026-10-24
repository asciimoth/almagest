# Out network migration

This privileged case verifies that a running `almagestd` follows host network
changes. It runs the complete case in two containers: one with IPv6 enabled and
one with IPv6 disabled at the network-namespace level. Each run models a laptop
that moves between ethernet, no network, Wi-Fi, and no network. Both simulated
physical networks provide the same public and LAN target addresses from separate
network namespaces. Their response bodies identify the active network.

The test has two ordered phases:

1. A small `LocalProxy -> OutNetwork` graph sends new SOCKS requests through
   each active physical network. Requests must fail while both networks are
   disconnected and must recover without a daemon restart.

1. The test adds this more realistic path to the running daemon:

   ```text
   DefaultTun -> TunSplitter -> TunJoiner -> Spoofer
              -> LogNetwork -> OutNetwork
   ```

   New direct requests to the public target must pass through the complete graph
   after each network transition. In non-strict mode, the active LAN route must
   bypass `DefaultTun` and follow the current physical network. In strict mode,
   both public and LAN requests must pass through the graph and recover after
   each transition.

Temporary failures during a transition are expected. Every success assertion
uses bounded polling and requires the system to converge. Existing transport
connections are not expected to migrate.

Run this case with:

```sh
./e2e/run.sh out_network_migration
```

The case runner supplies privileged mode, `/dev/net/tun`, and the IPv6 sysctls.
Additional `E2E_DOCKER_RUN_ARGS` apply to both profile containers.
