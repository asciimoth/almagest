# TUN Firewall with curl and TUN-backed HTTP services

This case creates a real Linux TUN interface inside the Docker container. Two
Almagest HTTP services run behind a `MockNetwork`, and a real `curl` process
uses the kernel route through this graph:

```text
curl -> Linux route -> Tun -> TunFirewall -> Spoofer -> MockNetwork
                                ^                         |
                                |                         +-> HTTP :18111
                           ObjectConstant                 +-> HTTP :18112
```

The TUN interface is `almfw0` with `10.213.17.1/24`. Both HTTP services use
`10.213.17.2`, with ports `18111` and `18112`. Thus, success requires packets
from the real `curl` process to pass through the Tun Firewall in both
directions.

## Policy changes and assertions

1. Empty menu and object policies allow both services.
1. The attached `ObjectConstant` blocks outgoing TCP packets to port `18111`.
   Port `18112` stays reachable.
1. The firewall menu blocks port `18112` too. Both services are then blocked,
   which proves that menu and object policies are merged.
1. Clearing only the object value allows port `18111`; the menu still blocks
   port `18112`.
1. Clearing the menu allows both services again.

The workspace stores `rules` as a native HJSON object. The admin menu still
accepts JSON text and converts it to that structured form when the workspace is
saved.

The case needs privileged Docker networking because it creates a TUN device and
an IP route:

```sh
E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun" ./e2e/run.sh tun_firewall_curl
```
