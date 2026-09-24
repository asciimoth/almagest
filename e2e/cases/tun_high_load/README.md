# tun_high_load

## Setup

This case requires privileged Linux networking because it creates three native
TUN devices and installs routes in the container network namespace:

```sh
E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun" ./e2e/run.sh tun_high_load
```

The graph contains these independent paths:

- `Load direct TUN -> Load direct Spoofer -> Load direct network`;
- `Load middleware TUN -> TunConfigStripper -> TunFirewall -> Spoofer`; and
- `Load split TUN -> TunSplitter -> two Spoofer and MockNetwork branches`.

The split path routes two services that use the same IP address by TCP port.
This makes each load round exercise both splitter output queues and return-flow
routing.

## Load

The test starts many TCP clients at the same time. Each client opens a new HTTP
connection, reads the complete response, and checks the status and exact body.
The default is 4,000 requests per round with 32 concurrent workers. To change
these values, pass `ALMAGEST_E2E_TUN_LOAD_REQUESTS` and
`ALMAGEST_E2E_TUN_LOAD_CONCURRENCY` to the container. For example:

```sh
E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun \
  -e ALMAGEST_E2E_TUN_LOAD_REQUESTS=16000 \
  -e ALMAGEST_E2E_TUN_LOAD_CONCURRENCY=128" \
  ./e2e/run.sh tun_high_load
```

The test runs the same load in three scenarios:

1. on the initial graph;
2. while live `TunFirewall` and `TunSplitter` configuration updates occur; and
3. after a daemon restart restores the graph and all native TUN devices.

Every round sends an equal number of requests through all four HTTP targets.
The test fails on a connection error, a non-200 response, a wrong body, an
invalid load setting, or a TUN node that does not return to its ready state.
