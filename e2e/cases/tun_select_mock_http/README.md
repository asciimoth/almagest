# tun_select_mock_http

## Setup

This case requires privileged Docker networking and `/dev/net/tun`:

```sh
E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun" ./e2e/run.sh tun_select_mock_http
```

It mirrors `select_mock_switch_http`, but the inputs are real `almagest/Tun`
nodes and the selected payload is an `almagest/tun` link. Two TUN devices feed
two `SelectOut` nodes, which are cross-wired through two `Select` nodes into two
`Spoofer` + `MockNetwork` server networks.

Each mock network has HTTP bindings for both target TUN subnets. The bindings
use different ports because `MockNetwork` maps all non-loopback hosts to one
internal loopback address. This keeps both bindings active in each mock network
while the selector decides whether traffic reaches Server A or Server B.

## Expected Behavior

Initially:

- `curl --noproxy '*' http://10.71.160.2:8080` enters `tun-select-a` and returns
  `Server A`.
- `curl --noproxy '*' http://10.72.160.2:8081` enters `tun-select-b` and returns
  `Server B`.

After updating `BoolConstant 1815` to `true` through the admin API:

- `curl --noproxy '*' http://10.71.160.2:8080` returns `Server B`.
- `curl --noproxy '*' http://10.72.160.2:8081` returns `Server A`.
