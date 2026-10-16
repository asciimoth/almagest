# tun_splitter_process_routing

## Setup

This case runs inside the shared e2e Docker image and requires privileged Linux
networking because it creates a TUN device, mounts bpffs for p-mark process
tracking, creates network namespaces, and mutates routes. Run it with:

```sh
E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun" ./e2e/run.sh tun_splitter_process_routing
```

`setup.sh` creates a routed public-prefix target at `203.0.115.2:18096` in a
network namespace. This keeps the OutNetwork assertion hermetic while still
exercising ordinary root-namespace routing through an external-looking prefix.

## Initial Daemon Config

`alm.hujson` mirrors the local TunSplitter graph from `local/alm.hujson`:

- `DefaultTun 1501` captures normal process traffic.
- `TunSplitter 1502` applies process rules:
  - `curl` command-line or command-name traffic routes to slot 3.
  - `wget` command-line or command-name traffic routes to slot 5.
  - `e2e-split-exec-client` executable traffic routes to slot 5.
  - User `almrouteuser` traffic routes to slot 3.
  - UID `24011` traffic routes to slot 5.
  - Group `almroutegroup` traffic routes to slot 3.
  - GID `24013` traffic routes to slot 5.
  - `TRUE` routes everything else to slot 6.
- Slot 3 forwards through `Spoofer 1503` to a mock network HTTP server returning
  `Server 1` at `10.210.148.1:8080`.
- Slot 5 forwards through `TunCopy 1505` and `VTun 1506` to a second HTTP
  server returning `Server 2` at the same `10.210.148.1:8080` address.
- Slot 6 forwards through `Spoofer 1508` to `OutNetwork 1509`.

## Expected Behavior

- Before daemon startup, the synthetic public target is reachable through the
  root namespace route.
- After daemon startup, `curl http://10.210.148.1:8080/` returns `Server 1`.
- `wget http://10.210.148.1:8080/` returns `Server 2` for the same IP and port.
- A copied executable client matching the `exec` rule returns `Server 2`.
- Python clients run as the named user, numeric UID, named group, and numeric
  GID route to their configured slots.
- A long-lived Python client added to the rules by PID at runtime routes to
  `Server 1`.
- A non-curl/non-wget Python HTTP client reaches the public-prefix target
  through the fallback OutNetwork path.
- The public target is reachable again through ordinary routing after daemon
  shutdown.
- The complete scenario runs once with explicit automatic isolation and once
  with strict isolation.
- DefaultTun reports its Linux strict, exclude, and include profiles as
  available in both isolation modes. Both pre-filter contexts accept all eight
  sysnet-linux process rule types: `comm`, `exec`, `cmd`, `pid`, `user`, `uid`,
  `group`, and `gid`.
