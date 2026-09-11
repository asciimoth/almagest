# AmnesiaCtrl self-host controller

This privileged case runs the upstream `amneziawg-go` service and the mock
encrypted API-v2 gateway from `wgo` v0.7.0. The gateway is exposed through a
locally generated HTTPS certificate. An Almagest AmnesiaCtrl node imports the
service key, owns a tracked peer on a WireGuard node, and sends tunnel traffic
to the real upstream service.

The case verifies:

- the API network and WireGuard controller links can detach and reattach;
- the imported peer uses the WireGuard default UDP transport;
- an activation-key secret value change causes a new import;
- a selected TLS policy change removes the peer on failure and restores it;
- a country form change causes a new import and updates status;
- the persisted installation UUID survives a daemon restart;
- HTTP and ICMP traffic pass through the real AmneziaWG data plane after each
  recovery.

The service profile disables random trailers. An unbounded random handshake
trailer can exceed the IPv4 UDP payload limit and make this lifecycle test
probabilistic. The `wireguard_amneziawg_go_compat` case tests random-trailer
interoperability separately.

Run it with a privileged container and `/dev/net/tun`, for example:

```sh
E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun" \
  ./e2e/run.sh amnesia_selfhost_controller
```
