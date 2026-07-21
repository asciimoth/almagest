# tun_routing

## Setup

This case mirrors the manual graph in `local/alm.hujson`: two regular TUN
devices route private prefixes into separate `Spoofer -> MockNetwork` chains,
and one `DefaultTun` with no downstream TUN consumer captures ordinary
default-routed traffic.

It requires privileged Linux networking because it creates real TUN devices and
mutates routes:

```sh
E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun" ./e2e/run.sh tun_routing
```

## Initial Daemon Config

- `Tun 1504` creates `tun1` with `10.7.160.1/24`.
- `Tun 1508` creates `tun2` with `10.210.148.1/24`.
- Each TUN is connected through a `Spoofer` to its own `MockNetwork`.
- `HttpServer 1525` listens on `10.7.160.2:8080` and returns `Server 1`.
- `HttpServer 1531` listens on `10.210.148.2:8080` and returns `Server 2`.
- `DefaultTun 1543` is present but has no downstream TUN consumer. Its DNS port
  is linked to `OutDns 1542` so name resolution keeps the old system-provider
  behavior while the TUN path remains disconnected.

## Expected Behavior

- `curl http://example.com/` fails because default-routed traffic is captured by
  the disconnected `DefaultTun`.
- `curl http://10.7.160.2:8080/` reaches Server 1 through `tun1`.
- `curl http://10.210.148.2:8080/` reaches Server 2 through `tun2`.
- Removing `tun1` blocks only Server 1; Server 2 and the TUN-disconnected
  `DefaultTun` behavior remain unchanged.
- Removing the initial TUN-disconnected `DefaultTun` restores ordinary default-route
  access to `example.com`.
- Re-adding `tun1` dynamically and linking it back to the first `Spoofer`
  restores Server 1 while `example.com` remains reachable.
- Removing `tun2` blocks only Server 2 while Server 1 and `example.com` remain
  reachable.
- Re-adding `tun2` dynamically and linking it back to the second `Spoofer`
  restores Server 2.
- Re-adding a TUN-disconnected `DefaultTun` after both dynamic TUN routes exist
  blocks `example.com` again without affecting either TUN-backed server.
- Removing and re-adding another TUN-disconnected `DefaultTun` repeats the same
  default-route block/restore behavior without affecting either TUN-backed
  server.
