# killswitch_default_tun_exclude

Runs the real `killswitch` daemon and the real native Linux `almagestd`
integration in one privileged Docker container.

The case builds a routed test network:

- root namespace `almks0` veth, managed by killswitch
- router namespace forwarding to the target namespace
- HTTP target at `203.0.114.2:18095`

Assertions:

- the target is reachable before killswitch starts
- killswitch blocks unmarked outgoing traffic on `almks0` by default
- `DefaultTun` makes `almagestd` publish temporary killswitch allow rules for
  the daemon app mark and the user exclude mark
- `DefaultTun` `exclude` rule for `comm=curl` lets `curl` go directly to the
  outer network while killswitch remains active
- unexcluded Python traffic is blocked by the TUN-disconnected `DefaultTun`, then
  succeeds through `DefaultTun -> Spoofer -> LogNetwork -> OutNetwork`
- after `almagestd` exits, direct `curl` traffic is blocked again by killswitch

Run with privileged Docker networking:

```sh
E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun" ./e2e/run.sh killswitch_default_tun_exclude
```
