# default_tun_routing

## Setup

This case runs inside the shared e2e Docker image and requires privileged Linux
networking because it creates network namespaces, creates a TUN device, and
mutates routes. Run it with:

```sh
E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun" ./e2e/run.sh default_tun_routing
```

`setup.sh` creates three network namespaces:

- `alm-e2e-local`: a directly connected private target at `10.213.1.2:18093`.
- `alm-e2e-router`: a router between the root namespace and the public target.
- `alm-e2e-public`: a public-prefix target at `203.0.113.2:18094`.

The root namespace has a direct private route to the local target and a public
route to `203.0.113.0/24` through a gateway. This mirrors the old
`local/e2e/case3` manual scenario without depending on public internet access.

## Initial Daemon Config

`alm.hujson` starts `DefaultTun 1301` in non-strict mode with its DNS port
linked to `OutDns 1300` and no downstream TUN consumer. The daemon is started
with debug nodes enabled so the test can attach `LogNetwork` later and assert
bytes flowing through the recovered outbound chain.

## Expected Behavior

- Before daemon startup, both namespace HTTP targets are reachable.
- After `DefaultTun` starts without any downstream TUN consumer:
  - the directly connected private target remains reachable in non-strict mode;
  - the public-prefix target is blocked because it routes through DefaultTun.
- Enabling strict mode through `graph apply-menu` blocks the private target too.
- Disabling strict mode restores the private target while the public target
  remains blocked.
- Dynamically adding two `TunSplitter` and two `TunJoiner` nodes before the
  `Spoofer`, `LogNetwork`, and `OutNetwork` nodes restores public target access
  through a deep TUN wrapper graph.
- The public request through the attached chain is visible in `LogNetwork`.
- Same-file MTU changes through 1280, 1420, 1428, 1500, and 9000 keep large
  HTTP requests working. Carrier MTUs 1280, 1500, and 9000 are also tested.
- The deep graph stays attached after the native TUN is deleted and recreated.
- After daemon shutdown, the public target is reachable through ordinary root
  namespace routing again.
