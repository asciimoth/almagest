# Almagestd Low-Memory Tuning Experiments

Follow-up to `MEMORY_CONSUMPTION_REPORT.md`.

## Scope

These checks looked for practical configuration and runtime options that can be
used on resource-limited devices without changing code. All daemon runs were
inside Docker with `--network none`, debug system integration, loopback-only
admin pprof, and a freshly built static `almagestd` binary.

The most useful knobs were:

- `almagest/WireGuard.batchSize`
- container CPU visibility, especially `--cpuset-cpus=0`
- Go runtime memory settings: `GOMEMLIMIT`, `GOGC`, and
  `GODEBUG=madvdontneed=1`
- `VTun.endpointCh` and `Spoofer.queueLength` only as secondary backlog knobs

## Tested Workloads

WireGuard workload:

- graph: `examples/socks2wireguard.hujson`
- request path: SOCKS5 `127.0.0.1:1081` to `http://10.198.20.1:8080/`
- load: 500 requests, parallelism 20

DefaultTun/Spoofer workload:

- graph: `examples/tun2net.hujson`
- debug DefaultTun builder: `vtun-socks`
- request path: SOCKS5 `127.0.0.1:1081` to `http://10.199.20.1:8080/`
- load: 1,000 requests, parallelism 40

`After GC RSS` was measured after requesting
`/debug/pprof/heap?debug=1&gc=1`. Values are MiB, converted from `/proc`.

## WireGuard Results

| Case | Runtime/container options | Idle RSS | Peak RSS | After load RSS | After GC RSS | Duration |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| default `batchSize` | default | 32.4 | 80.1 | 82.0 | 82.9 | 0.68s |
| `batchSize: 1` | default | 28.9 | 62.9 | 63.1 | 62.2 | 0.68s |
| `batchSize: 1` | `--cpuset-cpus=0` | 25.9 | 51.3 | 51.8 | 52.0 | 4.97s |
| `batchSize: 1` | `GOMEMLIMIT=96MiB` | 29.5 | 60.3 | 63.1 | 60.4 | 0.68s |
| `batchSize: 1` | `GOMEMLIMIT=96MiB GOGC=50 GODEBUG=madvdontneed=1` | 28.5 | 58.1 | 58.1 | 58.6 | 0.73s |
| `batchSize: 1` | `GOMEMLIMIT=64MiB GOGC=25 GODEBUG=madvdontneed=1` | 28.4 | 53.7 | 54.8 | 54.2 | 0.84s |

The biggest WireGuard win remains the config-level `batchSize` reduction. In
this smaller workload, changing from the default effective batch size to
`batchSize: 1` reduced after-GC RSS by about 21 MiB.

CPU pinning reduced RSS further, but it also made the workload roughly 7x
slower in this parallel request test. Treat `--cpuset-cpus=0` or equivalent
process CPU constraints as a memory/throughput tradeoff, not a free default.

Go runtime memory settings helped after `batchSize` was already low, but the
effect was smaller than the batch-size change. `GOMEMLIMIT=64MiB GOGC=25
GODEBUG=madvdontneed=1` reduced the low-batch after-GC RSS by about 8 MiB
versus default runtime settings.

## DefaultTun/Spoofer Results

| Case | Runtime/config options | Idle RSS | Peak RSS | After load RSS | After GC RSS | Duration |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| default queues | default runtime | 25.1 | 104.7 | 104.7 | 102.7 | 1.26s |
| default queues | `GOMEMLIMIT=96MiB GOGC=50 GODEBUG=madvdontneed=1` | 24.4 | 92.5 | 92.5 | 90.5 | 1.38s |
| `endpointCh: 64`, `queueLength: 64` | `GOMEMLIMIT=96MiB GOGC=50 GODEBUG=madvdontneed=1` | 24.8 | 88.8 | 88.9 | 89.0 | 1.31s |
| default queues | `GOMEMLIMIT=64MiB GOGC=25 GODEBUG=madvdontneed=1` | 24.3 | 77.6 | 72.6 | 72.6 | 1.42s |
| `endpointCh: 64`, `queueLength: 64` | `GOMEMLIMIT=64MiB GOGC=25 GODEBUG=madvdontneed=1` | 24.4 | 77.8 | 72.5 | 72.6 | 1.43s |

The Go runtime knobs helped the non-WireGuard `DefaultTun -> Spoofer` workload
more than the queue-size knobs did. The tighter `64MiB`/`GOGC=25` profile cut
after-GC RSS from about 103 MiB to about 73 MiB, with all requests completing
in this debug workload.

Lowering `VTun.endpointCh` and `Spoofer.queueLength` from 1024 to 64 still did
not materially change the result once the Go runtime memory profile was tight.
It is useful as a backlog/backpressure cap, but it is not the main RSS control.

## Recommended Low-Memory Profiles

### WireGuard Graphs

Start with explicit WireGuard batch sizing:

```hujson
"WireGuard Client": {
  "Class": "almagest/WireGuard",
  "batchSize": 1,
}
```

For a less aggressive throughput tradeoff, use `batchSize: 8` or
`batchSize: 16` and test the target link. The memory report showed roughly
linear buffer-budget reduction as batch size falls.

For very small devices, combine the low batch size with Go runtime pressure:

```sh
GOMEMLIMIT=64MiB GOGC=25 GODEBUG=madvdontneed=1 almagestd ...
```

If the device should also minimize worker/thread footprint and lower throughput
is acceptable, constrain available CPUs:

```sh
docker run --cpuset-cpus=0 ...
```

On a non-Docker service manager, use the equivalent CPU affinity or cgroup CPU
controls.

### DefaultTun/Spoofer Graphs

Use Go runtime pressure first:

```sh
GOMEMLIMIT=64MiB GOGC=25 GODEBUG=madvdontneed=1 almagestd ...
```

Then consider reducing endpoint queues if packet backlog must be bounded:

```hujson
"Spoofer": {
  "Class": "almagest/Spoofer",
  "queueLength": 64,
}
```

For debug DefaultTun experiments, the matching protected config knob is:

```hujson
"System": {
  "Type": "debug",
  "Debug": {
    "DefaultTun": {
      "Builder": "vtun-socks",
      "EndpointCh": 64
    }
  }
}
```

Native DefaultTun implementations may expose this differently; the queue result
should be read as a gVisor channel backlog result, not a universal daemon memory
limit.

## Caveats

`GOMEMLIMIT` is not an RSS cap. In the successful `GOMEMLIMIT=64MiB` runs,
process RSS and cgroup memory still exceeded 64 MiB because RSS includes
non-heap memory, goroutine stacks, mapped binary pages, runtime metadata,
network buffers, gVisor state, and heap spans that may remain resident.

Lower `GOGC` and lower `GOMEMLIMIT` trade CPU for lower retained memory. The
tested values are viable for these Docker debug workloads, but they should be
validated on real device traffic before becoming defaults.

`GODEBUG=madvdontneed=1` can make released heap pages show up as lower RSS more
promptly on Linux, but it does not remove live packet buffers or gVisor state.

The practical low-memory starting point is therefore:

```text
WireGuard-heavy graph:
  batchSize: 1 or 16
  GOMEMLIMIT=64MiB to 128MiB, depending on device RAM and traffic
  GOGC=25 or 50
  GODEBUG=madvdontneed=1
  optional one-CPU cpuset/affinity if throughput can be lower

DefaultTun/Spoofer-heavy graph:
  GOMEMLIMIT=64MiB to 128MiB
  GOGC=25 or 50
  GODEBUG=madvdontneed=1
  queueLength/endpointCh around 64 only when backlog bounds matter
```
