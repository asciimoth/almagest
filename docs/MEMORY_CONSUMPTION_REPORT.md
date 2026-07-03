# Almagestd Memory Consumption Report

Date: 2026-07-02
Updated: 2026-07-03

## Summary

The largest idle memory consumer in example-sized `almagestd` setups is the
embedded WireGuard runtime from `github.com/asciimoth/wgo`.

The dominant allocation is not in the daemon admin API, config parsing, log
cache, Pasta graph state, or gVisor netstack objects. Heap profiles showed
96-97% of retained heap in:

```text
github.com/asciimoth/wgo/device.(*Device).PopulatePools.func3
```

The retaining path is:

```text
wgo/device.(*Device).RoutineReadFromTUN
  -> wgo/device.(*Device).NewOutboundElement
  -> wgo/device.(*Device).GetMessageBuffer
  -> wgo/device.(*Device).PopulatePools.func3
```

Each active WireGuard TUN reader preallocates `device.BatchSize()` outbound
elements. `wgo.Device` defaults `batchSize` to `256` when the configured value
is `0` or omitted, and each outbound element owns one
`[MessageBufferSize]byte`. `MessageBufferSize` is approximately 65.6 KiB.

Approximate baseline per active WireGuard TUN reader:

```text
256 * 65.6 KiB ~= 16 MiB
```

This explains the observed jump from small configs to WireGuard-heavy example
configs. Two active WireGuard nodes retain roughly 38 MiB of Go heap; four
active WireGuard nodes retain roughly 75 MiB of Go heap.

Almagest now exposes that `wgo` batch size as `batchSize` on
`almagest/WireGuard`. Positive values are passed to both
`wgo.DeviceOptions.BatchSize` and `batchudp.StdNetBindOptions.BatchSize`; `0`
preserves the default. This lowers the fixed WireGuard TUN-reader and UDP-bind
batch allocation budget roughly linearly, but it is not a global daemon memory
limit. In Docker heavy-load checks, lowering both WireGuard nodes from the
default effective batch size `256` to `1` reduced after-load RSS, while large
HTTP responses still left the daemon in the hundreds of MiB because gVisor,
HTTP payloads, packet buffers, `sync.Pool`s, and Go heap spans dominated.

The non-WireGuard examples are not idle-memory problems. Real Docker daemon
runs for `direct`, `select`, `tun2net`, and `tun_split` restored at roughly
21-25 MiB RSS. Under burst traffic, however, the `DefaultTun`/`Spoofer` paths
can still grow to roughly 94-214 MiB RSS without WireGuard because gVisor
netstack state, packet/TCP buffers, daemon transfer buffers, and Go heap spans
remain warm after load. The pure mock-network `select` setup stayed close to
idle under repeated requests.

A native-TUN comparison against `github.com/xjasonlyu/tun2socks` showed that
Almagest's `DefaultTun -> Spoofer -> SocksClient -> OutNetwork` path is
substantially heavier than tun2socks when both are reduced to the same basic
TUN-to-SOCKS job. In Docker `--network none` runs, with management listeners
disabled and the same in-container SOCKS target, almagestd idled at about
29 MiB RSS while tun2socks idled at about 12 MiB RSS. After a 3,000-request
small-response burst, almagestd retained about 144 MiB RSS versus about
42 MiB for tun2socks. With 400 1 MiB responses, almagestd retained about
128 MiB RSS versus about 95 MiB for tun2socks. This comparison does not
exercise WireGuard. It points at general-purpose daemon, graph, native
sysnet-linux, gonnect/socksgo wrapper, and Spoofer forwarding overhead on top
of the shared gVisor netstack class.

A native-TUN comparison against upstream `wireguard-go` showed the same pattern
for the narrow WireGuard client job. With both clients reduced to one native
TUN, one WireGuard peer, the same route, the same endpoint, the same
in-container peer, and no management listener on the measured process,
almagestd using `DefaultTun -> WireGuard -> OutNetwork` idled at about
29 MiB RSS while upstream `wireguard-go` idled at about 10 MiB RSS. After a
3,000-request small-response burst, almagestd retained about 56 MiB RSS versus
about 13 MiB for `wireguard-go`. With 400 1 MiB responses, almagestd retained
about 365 MiB RSS versus about 231 MiB for `wireguard-go`. The narrow daemon is
much smaller, but the large-response run also confirms that userspace
WireGuard itself can retain hundreds of MiB after high-volume payload traffic
has dirtied packet buffers and Go heap spans.

The follow-up `MEMORY_NON_GVISOR_OVERHEAD_REPORT.md` breaks that delta down
into fixed daemon footprint, native `DefaultTun`/`sysnet-linux`, graph wrapper,
per-flow forwarding, SOCKS client, copy-buffer, and Go runtime retention costs,
with a ranked list of reduction candidates.

The `--no-pool` flag does not materially change idle daemon memory and did not
reduce resident memory under the heavy `tun2net` burst checks. It disables only
the daemon-level `leveledBufPool`; it does not disable WireGuard's `wgo`
message-buffer pools or gVisor netstack allocation/retention. In paired Docker
runs, `--no-pool` stayed within noise at idle and was slightly higher after
1,000- and 3,000-request `DefaultTun -> Spoofer -> MockNetwork` bursts.

Lowering `VTun.endpointCh` and `Spoofer.queueLength` reduces the maximum number
of outbound gVisor packet buffers that can sit in their channel endpoints, but
it is not an idle-memory fix. The empty channel capacity itself is small, and
the full-queue payload budget at MTU 1500 is only about 1.5 MiB for 1024 slots
versus about 96 KiB for 64 slots per endpoint. In matching Docker heavy-load
runs, reducing both knobs from 1024 to 64 changed after-load RSS by only about
2 MiB and did not prevent the daemon from reaching roughly 228 MiB RSS.
The heavy-load resident set is still dominated by gVisor TCP/IP state, packet
buffers outside these two queues, goroutine stacks, Go heap spans, and
application/forwarding buffers.

Running the daemon with fewer available CPU cores does reduce memory, but only
as a secondary effect. `wgo` now uses `runtime.GOMAXPROCS(0)` for its default
worker count, so CPU-constrained containers create far fewer WireGuard worker
goroutines and fewer OS threads. In Docker checks of the two-WireGuard
`examples/socks2wireguard.hujson` graph, constraining the process to one
effective CPU lowered idle RSS by about 2-3 MiB and lowered after-load RSS by
about 16-20 MiB for a 500-request small-response workload. Live heap after
forced GC stayed close to the unconstrained run, so CPU limits should be treated
as a scheduler/thread/stack overhead reduction, not as a replacement for
`WireGuard.batchSize` or other packet-buffer controls.

## Measurement Method

Measurements were taken with a temporary local `go test` probe in
`bin/almagestd` that:

- parsed the same HuJSON daemon config path,
- used protected `System.Type = "debug"`,
- used debug default TUN builder `"pipe"` to avoid host networking changes,
- built `almagest.BuildEngine`,
- waited for restored async runtime startup,
- forced two GCs,
- recorded Go `runtime.MemStats`, `/proc/self/status`, and heap profiles.

For one heavy case, a real `almagestd` binary was also run inside Docker with
`--network none`, debug system integration, and no admin listeners. That sanity
check matched the probe's idle RSS closely.

Additional real-daemon WireGuard load checks were run inside Docker with:

- `CGO_ENABLED=0` static `almagestd` binary,
- `docker run --network none`,
- stdin HuJSON config with embedded protected config,
- protected `System.Type = "debug"`,
- debug default TUN builder `"pipe"`,
- admin listeners disabled unless explicitly measuring their overhead,
- in-container SOCKS5/HTTP clients driving requests through the graph.

The non-WireGuard example checks below used config directories copied from
`./examples` and the same Docker isolation. `DefaultTun` examples were run with
the debug `"vtun-socks"` default TUN builder so traffic could be injected
through the restored graph from inside the container. `examples/select.hujson`
has no external ingress, so its load check enabled the Unix admin API and
repeatedly pressed the restored `HttpClient` nodes' send buttons.

The `--no-pool` checks were also real-daemon Docker runs with `--network none`.
They used a rebuilt `almagest-e2e` image, copied `examples/tun2net.hujson`
inside the container, disabled admin listeners, set protected
`System.Type = "debug"`, used debug DefaultTun builder `"vtun-socks"` on
`127.0.0.1:1081`, and drove in-container SOCKS5 HTTP requests to
`10.199.20.1:8080`.

The lower-queue checks used the same Docker isolation and graph shape as the
`tun2net` load checks: `DefaultTun` was built by the debug `"vtun-socks"`
builder, the restored graph was `DefaultTun -> Spoofer -> MockNetwork ->
HttpServer`, admin listeners were disabled, and `curl --socks5-hostname
127.0.0.1:1081` drove 3,000 in-container requests at parallelism 80. The two
knobs were changed together for the comparison:

- debug DefaultTun `VTun` `EndpointCh`: `1024` versus `64`,
- `Spoofer.queueLength`: `1024` versus `64`.

The small-CPU checks were also real-daemon Docker runs with `--network none`.
They used the same static daemon binary and the same
`examples/socks2wireguard.hujson` graph as the WireGuard batch-size checks,
with admin pprof enabled only inside the network-isolated container. No
`almagestd` daemon was run directly on the host. The checked Docker CPU modes
were:

- default container CPU view: cgroup `cpu.max=max 100000`,
  `cpuset.cpus.effective=0-19`,
- CPU quota: `docker run --cpus=1`, cgroup `cpu.max=100000 100000`,
  `cpuset.cpus.effective=0-19`,
- cpuset: `docker run --cpuset-cpus=0`, cgroup `cpu.max=max 100000`,
  `cpuset.cpus.effective=0`.

A tiny static Go helper was run in the same image to confirm what the Go
runtime saw in each mode. With Go 1.25.5 in this environment, the default mode
reported `NumCPU=20 GOMAXPROCS=20`, `--cpus=1` reported
`NumCPU=20 GOMAXPROCS=2`, and `--cpuset-cpus=0` reported
`NumCPU=1 GOMAXPROCS=1`.

The WireGuard batch-size checks were real-daemon Docker runs with the same
`docker run --network none` isolation. They used a static daemon binary mounted
into the existing `almagest-e2e` image, copied
`examples/socks2wireguard.hujson`, disabled admin listeners, set protected
`System.Type = "debug"`, and drove `curl --socks5-hostname 127.0.0.1:1081`
through the restored `LocalProxy -> VTun -> WireGuard -> MockNetwork ->
WireGuard -> VTun -> HttpServer` graph. The only graph change in the
small-response matrix was `batchSize` on both WireGuard nodes. The
large-response matrix also replaced the debug HTTP response with a 1 MiB body.

The tun2socks comparison was run with a dedicated Docker harness added under:

```text
tools/memory/tun2socks_compare.sh
tools/memory/tun2socks_compare.Dockerfile
tools/memory/tun_compare_runner.py
```

The harness builds the current almagestd and tun2socks v2.6.0
(`4127937ea7c450a5230b273f406c9410acec2be7`) in Docker, then runs every
measurement in a fresh container with:

```text
docker run --network none --cap-add NET_ADMIN --device /dev/net/tun
```

The common comparison setup is:

- native Linux TUN, MTU 1500,
- TUN address `198.18.0.1/15`,
- only `10.199.20.1/32` routed into the tested TUN,
- client workload connects to `http://10.199.20.1:8080/`,
- tested process forwards TCP through SOCKS5 at `127.0.0.1:1080`,
- the in-container SOCKS5 test server maps every CONNECT to a local fixed-body
  HTTP server at `127.0.0.1:18080`, preventing the proxy's own upstream socket
  from re-entering the TUN route,
- management/admin listeners disabled for both tools.

The almagestd graph used for the comparison was:

```text
DefaultTun -> Spoofer -> SocksClient -> OutNetwork
```

The almagestd `Spoofer` was configured to match tun2socks' exposed gVisor
defaults where Almagest has equivalent knobs:

```text
mtu: 1500
queueLength: 1024
disableSACK: false
ttl: 64
icmpBurst: 50
icmpLimit: 1000
congestionControlAlg: "reno"
disableNagle: true
disableTCPModRecBuff: true
tcpSendBufferSize: 0
tcpReceiveBufferSize: 0
```

tun2socks was run as:

```text
tun2socks -loglevel error \
  -device tun://t2s0 \
  -mtu 1500 \
  -proxy socks5://127.0.0.1:1080 \
  -tun-post-up 'sh -c "ip addr add 198.18.0.1/15 dev t2s0 && ip link set dev t2s0 up && ip route add 10.199.20.1/32 dev t2s0"'
```

This is a functional comparison, not a byte-for-byte dependency comparison:
almagestd used `gvisor.dev/gvisor v0.0.0-20260325202830-7644cf3a343c` through
`gonnect-netstack v0.4.30`, while tun2socks v2.6.0 used
`gvisor.dev/gvisor v0.0.0-20250523182742-eede7a881b20`.

The upstream `wireguard-go` comparison was run with a dedicated Docker harness
added under:

```text
tools/memory/wireguardgo_compare.sh
tools/memory/wireguardgo_compare.Dockerfile
tools/memory/wireguardgo_compare_runner.py
```

The harness builds the current almagestd and upstream `wireguard-go` commit
`ecfc5a8d54462e18e13c72173e2623d16d8e25a0`, then runs each measurement in a
fresh container with:

```text
docker run --network none --privileged --device /dev/net/tun
```

`--privileged` is used only because this harness creates an additional network
namespace inside the already network-isolated Docker container; the benchmarked
routes and TUN devices are not created in the host network namespace.

The common comparison setup is:

- measured client in the container root network namespace,
- unmeasured upstream `wireguard-go` peer in an inner `ip netns` namespace,
- veth underlay between client and peer namespaces,
- client WireGuard address `10.77.0.1/24`,
- peer WireGuard address `10.77.0.2/24`,
- only `10.77.0.2/32` routed into the measured client TUN,
- WireGuard MTU 1420,
- endpoint pair `192.0.2.1:25221` and `192.0.2.2:25222`,
- `PersistentKeepalive = 1` on both sides,
- workload connects to `http://10.77.0.2:8080/`,
- fixed-body HTTP server runs in the peer namespace,
- management/admin listeners disabled for the measured process.

The almagestd graph used for the comparison was:

```text
DefaultTun -> WireGuard -> OutNetwork
```

The almagestd WireGuard node used default `batchSize: 0`, which preserves the
embedded `wgo` library default. Upstream `wireguard-go` has no equivalent CLI
batch-size option. To ensure the measured peer was the userspace daemon and not
the host kernel WireGuard implementation, the harness sets:

```text
WG_I_PREFER_BUGGY_USERSPACE_TO_POLISHED_KMOD=1
```

The `After forced-GC stats` columns below were added from companion Docker
runs. They enabled the loopback web admin pprof endpoint inside the
network-isolated container, requested
`/debug/pprof/heap?debug=1&gc=1` after the workload, then immediately read
`/proc/$pid/status`, `/proc/$pid/smaps_rollup`, and the heap profile's
`runtime.MemStats`. Values are formatted as:

```text
RSS / Private_Dirty / HeapAlloc
```

Because the earlier peak/after-load columns were captured in separate runs and
many rows originally had admin disabled, the forced-GC column should be read as
a companion check for the same graph/workload shape, not as an exact same-PID
continuation of the older peak row.

No `almagestd` daemon was run directly on the host.

## Measured Results

Values below are after restore, idle wait, and forced GC. The probe used debug
pipe TUNs, so these are conservative for native network paths.

| Config | RSS | Live Go heap | Goroutines |
| --- | ---: | ---: | ---: |
| empty config | 20.3 MiB | 0.6 MiB | 2 |
| `examples/select.hujson` | 25.4 MiB | 0.7 MiB | 8 |
| `examples/tun_split.hujson` | 25.6 MiB | 1.0 MiB | 97 |
| `examples/direct.hujson` | 23.9 MiB | 0.7 MiB | 31 |
| `examples/tun2net.hujson` | 25.1 MiB | 0.7 MiB | 33 |
| `examples/socks2wireguard.hujson` | 31.8 MiB | 38.2 MiB | 195 |
| `examples/wireguard_over_socks.hujson` | 35.7 MiB | 38.3 MiB | 203 |
| `examples/nested_wireguard.hujson` | 39.3 MiB | 75.4 MiB | 382 |

Docker sanity check for `examples/socks2wireguard.hujson` with a real daemon:

```text
VmRSS:   32120 kB
VmSize: 1254540 kB
Threads: 23
docker stats memory.current: ~20 MiB
```

The thread count differs from goroutine count because goroutines are multiplexed
onto OS threads.

Real-daemon Docker load checks:

| Case | Workload | Idle RSS | Peak/after-load RSS | After-load private dirty | After forced-GC stats | Threads after load |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| empty config, admin disabled | none | 17.2 MiB | 17.2 MiB | 2.2 MiB | 21.7 / 3.4 / 0.6 MiB | 8 |
| empty config, web+Unix admin enabled | none | 17.7 MiB | 17.7 MiB | 2.2 MiB | 21.7 / 3.5 / 0.6 MiB | 8 |
| `examples/select.hujson`, admin disabled | none | 23.2 MiB | 23.2 MiB | 5.8 MiB | 24.0 / 4.4 / 0.7 MiB | 10 |
| `examples/select.hujson`, Unix admin enabled | 1,000 restored `HttpClient` sends through admin API | 23.5 MiB | 25.6 MiB | 7.4 MiB | 28.5 / 8.0 / 1.0 MiB | 21 |
| `examples/direct.hujson` | 1,000 failed HTTP-over-SOCKS attempts through debug `DefaultTun -> Spoofer -> OutNetwork` | 20.7 MiB | 99.8 MiB | 81.1 MiB | 31.0 / 9.4 / 2.6 MiB | 25 |
| `examples/tun2net.hujson` | 1,000 HTTP-over-SOCKS requests through debug `DefaultTun -> Spoofer -> MockNetwork`; 993 OK, 7 failed | 23.1 MiB | 93.8 MiB | 74.2 MiB | 94.0 / 72.3 / 47.1 MiB | 25 |
| `examples/tun2net.hujson` overload burst | 3,000 HTTP-over-SOCKS requests through same path; 2,915 OK, 85 failed | 22.9 MiB | 214.3 MiB | 194.7 MiB | 234.9 / 213.1 / 131.5 MiB | 25 |
| `examples/tun_split.hujson` | 1,000 failed HTTP-over-SOCKS attempts through debug `DefaultTun -> TunSplitter -> Spoofer -> OutNetwork` | 25.3 MiB | 99.1 MiB | 80.0 MiB | 32.8 / 11.2 / 2.8 MiB | 25 |
| `examples/socks2wireguard.hujson` | 2,000 small HTTP requests through SOCKS5 | 29.5 MiB | 96.1 MiB | 76.2 MiB | 159.7 / 137.6 / 119.2 MiB | 24 |
| `examples/nested_wireguard.hujson` plus a temporary `LocalProxy` node | 500 small HTTP requests through SOCKS5 | 36.1 MiB | 106.3 MiB | 86.4 MiB | 121.9 / 99.9 / 108.4 MiB | 23 |
| `examples/socks2wireguard.hujson` with 1 MiB HTTP response body | 400 HTTP requests through SOCKS5 | 33.7 MiB | 270.9 MiB | 250.6 MiB | 507.5 / 485.5 / 150.8 MiB | 25 |

The large-response run completed all 400 client requests. It is the clearest
reproduction of the high resident-memory range: the graph still had only two
active WireGuard nodes, but sustained payload volume dirtied and retained far
more packet, TCP, and application buffers than the small-response run.

Custom WireGuard `batchSize` real-daemon Docker checks:

| Case | WireGuard `batchSize` on both nodes | Workload | Idle RSS | Peak/after-load RSS | After-load private dirty | After forced-GC stats | Threads after load | Result |
| --- | ---: | --- | ---: | ---: | ---: | ---: | ---: | --- |
| `examples/socks2wireguard.hujson` | `0` / omitted, effective `256` | 2,000 small HTTP requests through SOCKS5, parallelism 80 | 30.5 MiB | 171.3 MiB | 151.4 MiB | 161.4 / 139.3 / 117.9 MiB | 24 | 2,000 OK |
| same graph | `64` | same workload | 30.6 MiB | 165.1 MiB | 145.2 MiB | 160.4 / 138.3 / 109.5 MiB | 24 | 2,000 OK |
| same graph | `16` | same workload | 29.4 MiB | 160.6 MiB | 140.4 MiB | 158.8 / 136.7 / 88.7 MiB | 26 | 2,000 OK |
| same graph | `1` | same workload | 27.9 MiB | 142.4 MiB | 122.4 MiB | 147.7 / 125.7 / 82.5 MiB | 24 | 2,000 OK |
| same graph with 1 MiB HTTP response body | `0` / omitted, effective `256` | 400 HTTP requests through SOCKS5, parallelism 40 | 33.4 MiB | 490.3 MiB | 470.3 MiB | 507.5 / 485.5 / 150.8 MiB | 25 | 400 OK |
| same large-response graph | `16` | same workload | 32.4 MiB | 453.8 MiB | 433.7 MiB | 418.1 / 390.8 / 207.8 MiB | 25 | 400 OK |
| same large-response graph | `1` | same workload | 29.7 MiB | 435.2 MiB | 415.1 MiB | 208.4 / 182.5 / 88.8 MiB | 25 | 400 OK |

This is the clearest answer for the new WireGuard batch-size option. The knob
works and the graph remained functional even at `batchSize: 1` for these
Docker-isolated workloads. Lower values reduced both idle RSS and after-load
RSS, especially once traffic had touched the WireGuard buffers. The effect was
not proportional to the whole resident set, because the burst workloads also
dirtied gVisor TCP/IP state, HTTP response buffers, SOCKS/forwarding buffers,
goroutine stacks, and Go heap spans outside `wgo`.

Idle RSS moved less than the theoretical retained-heap budget because large
zeroed Go heap spans are not necessarily resident until touched. Under the
2,000-request small-response burst, the reduced batch size lowered after-load
RSS by about 29 MiB between the default and `batchSize: 1`. Under the 1 MiB
response burst, it lowered after-load RSS by about 55 MiB, but the daemon still
retained roughly 435 MiB RSS at `batchSize: 1`. The batch size should therefore
be treated as an effective WireGuard buffer-sizing and throughput knob, not as
a hard memory ceiling for heavy application traffic.

Small available-CPU real-daemon Docker checks:

| Case | Docker CPU mode | Go runtime view | Idle RSS | Idle private dirty | Idle HeapAlloc after GC | Idle goroutines | Idle threads |
| --- | --- | --- | ---: | ---: | ---: | ---: | ---: |
| `examples/socks2wireguard.hujson` | default | `NumCPU=20`, `GOMAXPROCS=20` | 28.8 MiB | 9.6 MiB | 38.6 MiB | 203 | 16 |
| same graph | `--cpus=1` | `NumCPU=20`, `GOMAXPROCS=2` | 27.1 MiB | 7.8 MiB | 37.9 MiB | 59 | 7 |
| same graph | `--cpuset-cpus=0` | `NumCPU=1`, `GOMAXPROCS=1` | 26.0 MiB | 6.6 MiB | 37.8 MiB | 51 | 6 |

Companion 500-request small-response checks through the same graph:

| Case | Docker CPU mode | Workload | Idle RSS | After forced-GC RSS | After forced-GC private dirty | After forced-GC HeapAlloc | Goroutines after load | Threads after load | Result |
| --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| `examples/socks2wireguard.hujson` | default | 500 HTTP requests through SOCKS5, parallelism 20 | 30.7 MiB | 78.5 MiB | 57.5 MiB | 59.4 MiB | 203 | 22 | 500 OK |
| same graph | `--cpus=1` | same workload | 26.9 MiB | 63.0 MiB | 42.3 MiB | 60.0 MiB | 59 | 8 | 500 OK |
| same graph | `--cpuset-cpus=0` | same workload | 26.1 MiB | 59.0 MiB | 38.3 MiB | 60.6 MiB | 51 | 8 | 500 OK |

This confirms that a smaller available CPU count can reduce RSS for
WireGuard-heavy graphs, because each `wgo.Device` creates fewer worker
goroutines when `GOMAXPROCS` is smaller. The idle effect was modest in this
graph, about 2.8 MiB between the default container CPU view and a one-CPU
cpuset. The 500-request companion check showed a larger RSS difference, but
live heap after forced GC was almost the same across CPU modes. That makes the
CPU limit useful for reducing worker, stack, OS thread, and runtime overhead,
while the main packet and application buffer demand remains governed by graph
shape, traffic volume, `WireGuard.batchSize`, gVisor/netstack behavior, and Go
heap retention.

Paired `--no-pool` real-daemon Docker checks:

| Case | Flag | Workload | Idle RSS | After-load RSS | After-load private dirty | After forced-GC stats | Threads after load | Result |
| --- | --- | --- | ---: | ---: | ---: | ---: | ---: | --- |
| empty config, admin disabled | default pool | none | 18.7 MiB | 18.7 MiB | 2.2 MiB | 21.6 / 3.4 / 0.6 MiB | 8 | running |
| empty config, admin disabled | `--no-pool` | none | 18.9 MiB | 18.9 MiB | 2.3 MiB | 21.7 / 3.4 / 0.6 MiB | 8 | running |
| `examples/tun2net.hujson` | default pool | 1,000 HTTP-over-SOCKS requests through debug `DefaultTun -> Spoofer -> MockNetwork` | 25.6 MiB | 105.3 MiB | 84.3 MiB | 104.7 / 82.9 / 48.8 MiB | 26 | 1,000 OK |
| `examples/tun2net.hujson` | `--no-pool` | same 1,000-request burst | 25.7 MiB | 106.8 MiB | 86.0 MiB | 101.8 / 80.2 / 43.8 MiB | 24 | 1,000 OK |
| `examples/tun2net.hujson` | default pool | 3,000 HTTP-over-SOCKS requests through debug `DefaultTun -> Spoofer -> MockNetwork` | 25.9 MiB | 241.0 MiB | 219.9 MiB | 231.1 / 209.2 / 128.5 MiB | 25 | 3,000 OK |
| `examples/tun2net.hujson` | `--no-pool` | same 3,000-request burst | 25.8 MiB | 245.8 MiB | 225.0 MiB | 219.6 / 197.9 / 130.9 MiB | 25 | 3,000 OK |

These paired runs should be read as evidence that the daemon global pool is not
the main retained-memory source for this workload. Disabling it may reduce reuse
of some transfer buffers, but the measured resident set was dominated by
gVisor/netstack state, packet/TCP buffers, goroutine stacks, Go heap spans, and
other pools outside `bin/almagestd/bufpool.go`.

Matching lower-queue real-daemon Docker checks:

| Case | VTun `endpointCh` | Spoofer `queueLength` | Workload | Idle RSS | Peak RSS during load | After-load RSS | After-load private dirty | After forced-GC stats | Threads after load | Result |
| --- | ---: | ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| `tun2net`-shape debug graph | 1024 | 1024 | 3,000 HTTP-over-SOCKS requests through `DefaultTun -> Spoofer -> MockNetwork`, parallelism 80 | 25.9 MiB | 227.8 MiB | 230.2 MiB | 209.1 MiB | 228.5 / 206.9 / 124.4 MiB | 25 | 3,000 OK |
| same graph | 64 | 64 | same workload | 25.8 MiB | 228.0 MiB | 228.0 MiB | 206.9 MiB | 237.8 / 216.1 / 125.2 MiB | 26 | 3,000 OK |

This comparison is the clearest answer for the queue-size question: lowering
both values from 1024 to 64 had almost no effect on idle RSS and only a small
after-load effect for this workload. The peak RSS was effectively unchanged.
The forced-GC companion run was also noisy and did not show a reliable
post-load reduction; the 64-slot run was slightly higher after GC.
The low values were still high enough for the small-response MockNetwork burst,
so throughput did not collapse and no requests failed. Under heavier fanout,
slower downstream forwarding, larger responses, or native-network jitter, lower
queues should be expected to trade backlog memory for earlier packet drops,
TCP retransmits, higher latency, and possible client timeouts.

The non-WireGuard runs show a separate behavior: idle RSS stays near 20-25 MiB,
but `DefaultTun`/`Spoofer` traffic can still push the daemon into the
90-215 MiB range without WireGuard. That growth came from real packet/TCP
traffic through gVisor netstacks and daemon buffers, not from `wgo` message
buffer pools. The memory did not immediately return to idle after load.

Native-TUN comparison with tun2socks:

| Tool | Setup | Workload | Idle RSS | Idle private dirty | Peak/after-load RSS | After-load private dirty | Threads after load | Result |
| --- | --- | --- | ---: | ---: | ---: | ---: | ---: | --- |
| almagestd | `DefaultTun -> Spoofer -> SocksClient -> OutNetwork`, native TUN, SOCKS5 egress | none | 29.2 MiB | 8.1 MiB | 29.2 MiB | 8.1 MiB | 13 | running |
| tun2socks v2.6.0 | native TUN, SOCKS5 egress | none | 12.2 MiB | 2.1 MiB | 12.2 MiB | 2.1 MiB | 9 | running |
| almagestd | same | 3,000 1 KiB HTTP responses through TUN, parallelism 80 | 29.2 MiB | 8.1 MiB | 143.5 MiB | 121.6 MiB | 26 | 3,000 OK |
| tun2socks v2.6.0 | same | same | 12.1 MiB | 2.1 MiB | 42.2 MiB | 31.5 MiB | 26 | 3,000 OK |
| almagestd | same | 400 1 MiB HTTP responses through TUN, parallelism 40 | 28.6 MiB | 7.6 MiB | 128.3 MiB | 105.9 MiB | 26 | 400 OK |
| tun2socks v2.6.0 | same | same | 12.2 MiB | 2.2 MiB | 95.2 MiB | 84.5 MiB | 24 | 400 OK |

The small-response burst is the clearest apples-to-apples memory delta.
Both tools completed 3,000 requests in about four seconds, but almagestd
retained roughly 101 MiB more RSS after the burst. The large-response run is
less latency-comparable: almagestd completed in about 10.4 seconds and
tun2socks in about 2.4 seconds, so the two processes saw different instantaneous
buffer pressure even though the request count and response size matched.

The main almagestd-specific costs in this comparison are not WireGuard:

- `sysnet-linux` default-TUN creation and route-management machinery,
- Pasta workspace/node/runtime scaffolding,
- the generic `gonnect` network abstraction and `socksgo` client wrapper,
- Spoofer's TCP forwarding path, which creates per-flow gVisor TCP endpoint
  state plus two forwarding workers using `io.Copy`,
- Go heap spans and packet/application buffers retained after burst traffic.

tun2socks is much narrower. Its stock path is one TUN device, one gVisor stack,
one proxy/tunnel layer, and no graph engine, admin API, sysnet abstraction,
node lifecycle, or general network-resource wrapping. Its retained memory still
grew under load because the shared class of costs remains: gVisor TCP endpoint
state, packet buffers, SOCKS transport buffers, goroutine stacks, and Go heap
retention. It simply had a smaller fixed baseline and fewer layers around the
netstack.

This means the high non-WireGuard RSS seen in `DefaultTun`/`Spoofer` bursts is
not inherent to gVisor alone. A single-purpose tun2socks-style implementation
using gVisor stayed much smaller for the same TUN-to-SOCKS test. Almagest's
extra memory buys graph composability, runtime node lifecycle, native system
integration, and interchangeable `gonnect.Network` resources, but those layers
are visible in RSS once traffic has warmed the path.

Native-TUN comparison with upstream `wireguard-go`:

| Tool | Setup | Workload | Idle RSS | Idle private dirty | Peak/after-load RSS | After-load private dirty | Threads after load | Result |
| --- | --- | --- | ---: | ---: | ---: | ---: | ---: | --- |
| almagestd | `DefaultTun -> WireGuard -> OutNetwork`, native TUN, native UDP egress | none | 29.1 MiB | 8.6 MiB | 29.1 MiB | 8.6 MiB | 14 | running |
| upstream `wireguard-go` | native TUN, native UDP egress | none | 9.6 MiB | 5.2 MiB | 9.6 MiB | 5.2 MiB | 15 | running |
| almagestd | same | 3,000 1 KiB HTTP responses through WireGuard, parallelism 80 | 29.1 MiB | 8.6 MiB | 55.8 MiB | 34.4 MiB | 16 | 3,000 OK |
| upstream `wireguard-go` | same | same | 10.1 MiB | 5.7 MiB | 13.0 MiB | 8.6 MiB | 15 | 3,000 OK |
| almagestd | same | 400 1 MiB HTTP responses through WireGuard, parallelism 40 | 28.9 MiB | 8.1 MiB | 365.0 MiB | 343.5 MiB | 25 | 400 OK |
| upstream `wireguard-go` | same | same | 9.5 MiB | 5.1 MiB | 231.2 MiB | 226.8 MiB | 24 | 400 OK |

The small-response burst is the cleanest daemon-overhead comparison. Both
clients completed the same 3,000 tunneled requests in about one second:
almagestd took 1.13 seconds and upstream `wireguard-go` took 1.00 seconds. The
after-load RSS delta was about 43 MiB. Most of that is Almagest's fixed native
daemon and graph cost plus a warmer Go heap after moving traffic through
`DefaultTun`, `sysnet-linux`, the Pasta node runtime, `gonnect` wrappers, and
the embedded `wgo` integration.

The large-response run is a different regime. Upstream `wireguard-go` retained
about 231 MiB RSS by itself after 400 MiB of response payload moved through the
tunnel. Almagest retained about 365 MiB and took 4.85 seconds versus
`wireguard-go`'s 1.21 seconds. That means the large-payload result should not
be read only as graph overhead: the base userspace WireGuard daemon also keeps
a large dirty resident set after high-volume payload traffic, and Almagest adds
its own daemon/native-TUN/wrapper/runtime overhead on top.

The source-level comparison explains the idle and small-burst gap:

- upstream `wireguard-go` uses `conn.IdealBatchSize = 128`,
- upstream native TUN and UDP receive paths allocate batch-sized slices and
  message buffers in `device.RoutineReadFromTUN` and
  `device.RoutineReceiveIncoming`,
- almagestd uses the forked `github.com/asciimoth/wgo`; its default
  `defaultDeviceBatchSize` is `256`,
- almagestd passes `WireGuard.batchSize` into both
  `wgo.DeviceOptions.BatchSize` and `batchudp.StdNetBindOptions.BatchSize`,
- with `batchSize: 0`, Almagest preserves the larger `wgo` default, while
  upstream `wireguard-go` stays at its native 128-slot default,
- both implementations use full-size WireGuard message buffers, so large
  payload traffic can dirty and retain much more RSS than idle profiles show,
- Almagest also pays for `DefaultTun`/`sysnet-linux`, config restore, node
  lifecycle, engine worker tracking, graph link wrappers, status subscriptions,
  and a larger daemon binary.

This comparison does not replace the two-WireGuard example checks above. It
answers a narrower question: for one client-side userspace WireGuard interface,
the original daemon is much smaller than almagestd configured as an equivalent
graph, but both daemons can retain large resident sets after high-volume
payload bursts.

`examples/direct.hujson` and `examples/tun_split.hujson` were intentionally
measured under debug isolation, not native host routing. In that mode
`OutNetwork` is the debug outbound network, and `tun_split`'s `RULE cmd curl`
/ `RULE cmd wget` predicates do not match the debug SOCKS ingress. Those
workloads therefore report failed connection attempts, but they still exercise
the restored TUN, splitter, Spoofer, and forwarding paths under burst pressure.

## Main Source: WireGuard TUN Reader Buffers

Almagest creates a `wgo.Device` in:

```text
lib/node_wireguard_device.go
```

Relevant code path:

```go
n.dev = wgdevice.NewDevice(nil, nil, logger, n.workerSpawner, wgdevice.DeviceOptions{
    BatchSize: n.config.batchSize,
})
```

The WireGuard device stores the runtime pointer on the node:

```text
lib/node_wireguard.go
```

Relevant field:

```go
dev *wgdevice.Device
```

In `github.com/asciimoth/wgo@v0.5.0/device/device.go`, `NewDevice` computes
the effective batch size as:

```go
batchSize := options.BatchSize
if batchSize <= 0 {
    batchSize = defaultDeviceBatchSize
    if batchSize < conn.IdealBatchSize {
        batchSize = conn.IdealBatchSize
    }
}
```

`defaultDeviceBatchSize` is `256`. Positive `DeviceOptions.BatchSize` values
therefore override the default unless an initially attached TUN or bind reports
a larger batch size.

It then starts long-lived background workers:

```go
workers := effectiveDeviceWorkerCount(options)
for i := 0; i < workers; i++ {
    device.spawnWorker(func() { device.RoutineEncryption(id) }, ...)
    device.spawnWorker(func() { device.RoutineDecryption(id) }, ...)
    device.spawnWorker(func() { device.RoutineHandshake(id) }, ...)
}
```

The default worker count is `runtime.GOMAXPROCS(0)`, so every WireGuard device
creates `3 * GOMAXPROCS` worker goroutines plus other runtime goroutines unless
`DeviceOptions.WorkerCount` is set. With the 20 CPU environment used
during inspection, even unlinked WireGuard nodes created many goroutines:

| Synthetic config | Live Go heap | Goroutines |
| --- | ---: | ---: |
| two unlinked WireGuard nodes | 0.9 MiB | 132 |
| four unlinked WireGuard nodes | 1.2 MiB | 262 |

The Docker CPU-limit checks confirmed this scaling in the real daemon. The
same two-active-WireGuard graph had 203 goroutines with `GOMAXPROCS=20`, 59
goroutines with `GOMAXPROCS=2`, and 51 goroutines with `GOMAXPROCS=1`.
Reducing available CPU cores therefore does reduce scheduler, goroutine stack,
and OS thread overhead, but it does not remove the large WireGuard message
buffers allocated once a TUN reader is active.

The large heap appears only after the WireGuard TUN is attached and the device
is brought up.

In `github.com/asciimoth/wgo@v0.5.0/device/send.go`,
`RoutineReadFromTUN` allocates one outbound element per batch slot:

```go
batchSize := device.BatchSize()
elems := make([]*QueueOutboundElement, batchSize)

for i := range elems {
    elems[i] = device.NewOutboundElement()
    bufs[i] = elems[i].buffer[:]
}
```

`NewOutboundElement` allocates or retrieves a message buffer:

```go
func (device *Device) NewOutboundElement() *QueueOutboundElement {
    elem := device.GetOutboundElement()
    elem.buffer = device.GetMessageBuffer()
    return elem
}
```

In `github.com/asciimoth/wgo@v0.5.0/device/pools.go`, message buffers are:

```go
device.pool.messageBuffers = NewWaitPool(PreallocatedBuffersPerPool, func() any {
    return new([MessageBufferSize]byte)
})
```

In `github.com/asciimoth/wgo@v0.5.0/device/constants.go`:

```go
MaxMessageSize = MaxSegmentSize
MaxTunOffsetHeadroom = 64
MessageBufferSize = MaxMessageSize + MaxTunOffsetHeadroom
```

`MaxSegmentSize` is `(1 << 16) - 1`, so every message buffer is about 65.6 KiB.

This is the main retained heap:

```text
two active WireGuard nodes:
  38,117,472 B retained in PopulatePools.func3

four active WireGuard nodes:
  76,234,944 B retained in PopulatePools.func3
```

## Secondary Source: WireGuard UDP Receive Buffers

WireGuard receive routines also preallocate message buffers.

In `github.com/asciimoth/wgo@v0.5.0/device/receive.go`:

```go
bufsArrs := make([]*[MessageBufferSize]byte, maxBatchSize)

for i := range bufsArrs {
    bufsArrs[i] = device.GetMessageBuffer()
    bufs[i] = bufsArrs[i][:]
}
```

For debug loopback networks, the inspected configs used a batch size of `1`, so
this was not a large idle source in the probe.

For native Linux binds, `github.com/asciimoth/batchudp@v0.3.0` defaults to:

```go
IdealBatchSize = 128
```

and `StdNetBind` returns that batch size when the network is native on Linux or
Android and no positive `StdNetBindOptions.BatchSize` is supplied. That can add
roughly:

```text
128 * 65.6 KiB ~= 8 MiB
```

per native receive routine that is open. Almagest passes the WireGuard node's
`batchSize` into `StdNetBindOptions.BatchSize`, so positive values also reduce
native UDP receive-side batch allocation. Real native `OutNetwork` setups may
therefore consume more memory than the debug-pipe measurements when the field is
left at `0`, and less when a smaller positive value is set.

## VTun and Spoofer

`VTun` and `Spoofer` both create gVisor netstacks, but they were not the main
retained heap source in idle profiles.

`VTun` creation path:

```text
lib/node_vtun_runtime.go
github.com/asciimoth/gonnect-netstack@v0.4.30/vtun/vtun.go
```

`VTun` builds a gVisor stack and channel endpoint:

```go
st, err := no.BuildStack(true)
ep: channel.New(o.epch(), uint32(no.GetMTU()), "")
incomingPacket: make(chan *buffer.View, 256)
```

Almagest exposes this endpoint queue as `endpointCh` on `almagest/VTun`.
The default is `1024`:

```text
lib/node_vtun.go
defaultVTunEndpointCh = 1024
```

The debug DefaultTun `"vtun-socks"` builder has a separate protected-config
copy of the same idea:

```text
bin/almagestd/system.go
debug DefaultTun EndpointCh default = 1024
```

Lowering this value reduces only the gVisor `channel.Endpoint` outbound packet
queue created by `channel.New`. It does not change the fixed
`incomingPacket` channel of 256 `*buffer.View` values, the gVisor stack's TCP
send/receive buffers, DNS resolver state, goroutine stacks, or the daemon and
WireGuard buffer pools. When the channel is empty, the memory difference is
mostly channel slot storage, so changing 1024 to 64 saves only a few KiB at
idle. When the channel is full of ordinary MTU-sized packets, the payload
backlog capacity falls roughly from:

```text
1024 * 1500 B ~= 1.5 MiB
64 * 1500 B ~= 96 KiB
```

Actual packet buffers include metadata and may be smaller or larger than MTU,
but this is the right order of magnitude. If the queue fills, gVisor's channel
endpoint returns `ErrNoBufferSpace` and drops the packet rather than growing the
queue.

`Spoofer` creation path:

```text
lib/node_spoofer_runtime.go
github.com/asciimoth/gonnect-netstack@v0.4.30/spoofer/io.go
```

`Spoofer` creates a netstack endpoint with configurable queue length:

```go
opts.WithTunEndpoint(stackTun, n.config.queueLength)
```

and the endpoint defaults queue length to 1024 if less than 1.

Almagest exposes this as `queueLength` on `almagest/Spoofer`; the daemon default
is also `1024`:

```text
lib/node_spoofer.go
defaultSpooferQueue = 1024
```

`queueLength` feeds `gonnect-netstack/spoofer.NewTunEndpoint`, which creates
another gVisor `channel.Endpoint`. Its memory behavior is the same class as
`VTun.endpointCh`: small when empty, bounded by queued packet buffers when
full, and not a cap on the rest of the Spoofer netstack. The Spoofer TUN reader
still allocates read buffers from the detached TUN's batch size, and the
Spoofer stack still allocates per-flow TCP/UDP endpoint state, timers, packet
buffers, and forwarding goroutines.

These components add goroutines and can grow under real traffic, but in the
idle heap profiles they were below the WireGuard buffer cost by two orders of
magnitude.

## Non-WireGuard Example Behavior

The example configs that do not contain WireGuard nodes are:

- `examples/direct.hujson`
- `examples/select.hujson`
- `examples/tun2net.hujson`
- `examples/tun_split.hujson`

Their idle daemon memory is modest. In Docker debug runs, all four restored in
roughly the same band as the empty daemon plus a few runtime nodes:

```text
select:    ~23 MiB RSS
direct:    ~21 MiB RSS
tun2net:   ~23 MiB RSS
tun_split: ~25 MiB RSS
```

`select` is the smallest under load. Its graph is made from mock networks,
selectors, two HTTP servers, and two HTTP clients. Repeatedly triggering 1,000
HTTP client sends through the Unix admin API raised RSS only from about
23.5 MiB to 25.6 MiB. The forced-GC companion stayed in the same small regime
at about 28.5 MiB RSS, with about 1 MiB live Go heap. There was no obvious
persistent buffer growth.

The `DefaultTun`/`Spoofer` examples behave differently. Even without
WireGuard, burst traffic through the debug DefaultTun SOCKS ingress dirties and
retains gVisor packet/TCP buffers, daemon transfer buffers, goroutine stacks,
and Go heap spans. The successful `tun2net` path rose from about 23 MiB idle to
about 94 MiB RSS after a 1,000-request run. A heavier 3,000-request burst raised
RSS to about 214 MiB and produced some client failures under pressure, but the
daemon stayed running. In companion checks, forced GC did not return these
successful `tun2net` bursts to idle: RSS remained about 94 MiB after the
1,000-request run and about 235 MiB after the 3,000-request run.

Lowering `VTun.endpointCh` and `Spoofer.queueLength` changes this behavior only
at the packet-backlog edge. In the matching 3,000-request Docker comparison,
the default `1024/1024` run and the reduced `64/64` run both completed all
requests. Idle RSS stayed around 26 MiB in both cases, peak RSS stayed around
228 MiB, and after-load RSS was only about 2 MiB lower with the reduced
queues. The forced-GC companion check was noisy and still in the same range:
about 229 MiB RSS for `1024/1024` and about 238 MiB RSS for `64/64`. That means
these knobs can shave queued-packet headroom when queues are actually full, but
they do not materially reduce the heavy-load RSS plateau for small successful
HTTP flows.

The `direct` and `tun_split` Docker debug load checks did not represent
successful native outbound routing. `direct` routes to `OutNetwork`, and
`tun_split` falls through to its `TRUE -> SLOT 3` `OutNetwork` path because the
debug system has no process-command matcher for the injected SOCKS traffic.
Those workloads therefore failed at the connection level. They still raised RSS
to about 100 MiB, showing that failed TUN/Spoofer flow setup is enough to warm
netstack and buffer pools substantially. In the forced-GC companion checks,
these failed-attempt rows dropped much closer to idle, about 31-33 MiB RSS,
which suggests their warmed state was less persistent than the successful
`tun2net` forwarding bursts.

## Basic Daemon Without Nodes

The daemon itself appears modest. With no nodes restored, the real daemon is
mostly process image, Go runtime, and small service scaffolding.

Temporary probe empty-config measurement:

```text
RSS: 20.3 MiB
Live Go heap: 0.6 MiB
Goroutines: 2
```

Real Docker daemon measurements with an empty `nodes` object:

```text
admin disabled:
  VmRSS:        17.2 MiB
  PrivateDirty:  2.2 MiB
  Threads:        8

web + Unix admin enabled:
  VmRSS:        17.7 MiB
  PrivateDirty:  2.2 MiB
  Threads:        8
```

The difference between admin disabled and web+Unix admin enabled was about
0.5 MiB RSS in the static Docker run. Admin listeners are therefore not a
primary memory source for a basic daemon.

The no-node RSS comes mostly from:

- clean file-backed binary text and read-only data,
- embedded admin panel assets,
- Go runtime metadata, stacks, scheduler state, timers, and allocator arenas,
- parsed HuJSON config/protected config trees,
- the empty Pasta workspace and node class registry,
- the command runner queue/goroutine,
- the config autosaver goroutine/channel,
- signal handling and admin listener goroutines,
- the daemon log cache rings.

In the static Docker measurement, `/proc/$pid/smaps_rollup` reported only about
2.2 MiB of `Private_Dirty` memory for the empty daemon. Most RSS was
`Private_Clean`, which is consistent with mapped executable/read-only pages, not
retained mutable daemon state.

`VmSize` was about 1.25 GiB even for the empty daemon. This is not resident
memory. It is mostly Go runtime virtual address reservation and should not be
read as actual consumption; use RSS, PSS, cgroup `memory.current`, and Go heap
profiles instead.

The embedded web panel is compiled into the daemon in `bin/almagestd/api.go`:

```go
//go:embed static
var webStatic embed.FS
```

The current `bin/almagestd/static` tree is about 1.3 MiB, dominated by
`vendor/litegraph/litegraph.js` at about 1.0 MiB. These bytes increase the
binary/read-only mapping size whether or not the web admin listener is enabled.
Serving the admin UI adds only small HTTP listener and mux state.

The admin log cache is not a significant memory source. Defaults in
`bin/almagestd/config.go` are:

```go
Debug: 32
Info: 64
Warn: 128
Error: 256
Fatal: 64
```

`bin/almagestd/logger.go` allocates fixed rings:

```go
return &logRing{entries: make([]api.LogEntry, size)}
```

The default cache is only 544 `api.LogEntry` slots.

The global daemon buffer pool in `bin/almagestd/bufpool.go` is also not an idle
baseline source. It is an array of `sync.Pool`s and starts empty. It can retain
buffers after traffic, especially through SOCKS, HTTP, TUN, or netstack paths,
but it does not explain no-node startup RSS.

`--no-pool` calls `configureGlobalBufPool(true)`, which sets `globalBufPool` to
`nil` before `almagest.BuildEngine` receives it. That prevents Almagest's daemon
pool from being passed into engine nodes and TUN wrappers, but it does not
change allocation behavior inside:

- `github.com/asciimoth/wgo` message-buffer pools,
- gVisor TCP/IP stacks and channel endpoints,
- Go runtime heap-span retention,
- package-local pools that do not use `engine.Pool()`.

In the paired Docker checks, empty-daemon RSS stayed about 19 MiB with and
without `--no-pool`. Under the successful 3,000-request `tun2net` burst,
after-load RSS was about 241 MiB with the default pool and about 246 MiB with
`--no-pool`, so this flag is not an effective mitigation for the observed
100+ MiB heavy-load RSS.

## Why RSS Can Reach 100+ MiB

The idle measurements show live Go heap scaling with active WireGuard nodes.
RSS may be lower immediately after startup because large zeroed heap spans do
not necessarily become resident until touched.

Under real traffic or native network operation:

- TUN reader buffers become resident as packets are read.
- Native UDP receive buffers can add additional large resident batches.
- WireGuard `sync.Pool`s can retain message buffers after traffic bursts.
- Lower WireGuard `batchSize` values reduce the TUN and UDP batch-buffer
  budget, but they do not bound gVisor, application payloads, or Go heap-span
  retention.
- The daemon global `leveledBufPool` can retain buffers used by SOCKS, HTTP,
  TUN wrappers, and forwarding paths.
- `--no-pool` disables only that daemon global pool; it does not bound gVisor,
  WireGuard, or Go runtime retention.
- Lower `VTun.endpointCh` and `Spoofer.queueLength` values bound two gVisor
  channel endpoint queues, but they do not bound per-flow gVisor TCP/IP state
  or Go heap-span retention.
- gVisor TCP/IP stacks allocate per-flow packet buffers, TCP send/receive
  buffers, route state, timers, and goroutine stacks.
- Application payloads can be retained by node config or request/response
  handling; a large `HttpServer` response body is real daemon memory.
- Multiple WireGuard devices multiply all of the above.
- gVisor netstacks and forwarding paths add smaller but real overhead as flows
  are opened.

The Docker load checks show these useful regimes:

```text
empty daemon without nodes:                  ~17-18 MiB RSS
non-WireGuard select/mock-network load:      ~26 MiB RSS
non-WireGuard TUN/Spoofer burst traffic:     ~94-214 MiB RSS
non-WireGuard TUN/Spoofer burst, --no-pool:  ~107-246 MiB RSS
two WireGuard nodes, small sustained traffic: ~96-171 MiB RSS depending on burst shape
two WireGuard nodes, 1 MiB HTTP responses:   ~271-490 MiB RSS depending on concurrency
```

The non-WireGuard `tun2net` burst and the WireGuard 1 MiB response case are
important because they show that the issue is not only "number of nodes."
Payload volume and concurrency dirty many pages that were only virtually
reserved at idle and leave several pools warm after the burst. The measured
`Private_Dirty` memory after the earlier 1 MiB WireGuard response run was about
251 MiB; the higher-parallelism batch-size rerun reached about 470 MiB private
dirty memory at the default batch size. The heavier non-WireGuard `tun2net`
burst reached about 195 MiB private dirty memory.

RSS is also not expected to fall immediately after traffic stops. The Go
runtime may keep heap spans for reuse, `sync.Pool` keeps recently used objects
until future GC/runtime pool behavior, and OS-level RSS accounting can lag
behind logical object death. The forced-GC companion runs confirmed this for
successful `tun2net` and WireGuard traffic: GC reduced the live-heap view, but
RSS and private dirty memory often stayed close to the post-load plateau.

This explains reports like:

```text
basic daemon: ~17-20 MiB
basic daemon with --no-pool: ~19 MiB
non-WireGuard mock/select graph: ~23-26 MiB
non-WireGuard TUN/Spoofer burst traffic: ~100-215 MiB
non-WireGuard TUN/Spoofer burst traffic with --no-pool: ~107-246 MiB
example graph with active WireGuard traffic: ~100 MiB
large payloads, higher concurrency, or native/multiple WireGuard paths: 300+ MiB possible
```

## Recommended Fixes

### 1. Use smaller WireGuard batch sizes where throughput allows

This is the largest available configuration mitigation for WireGuard-heavy
graphs.

Current behavior:

```text
preallocate batchSize full-size message buffers per active TUN reader
```

Possible improvements:

- Configure `almagest/WireGuard.batchSize` to a smaller positive value for
  low-memory daemon profiles.
- Consider a smaller default batch size for userspace graph nodes.
- Allocate TUN read elements lazily instead of allocating the full batch up
  front.
- Read into MTU-sized buffers and copy only when a packet needs to enter the
  WireGuard encryption path.
- Keep a small warm batch and grow temporarily under load.

Reducing `batchSize` from 256 to 32 reduces the TUN reader buffer budget from
roughly 16 MiB to roughly 2 MiB per active WireGuard node. The real daemon RSS
reduction can be smaller at idle because untouched zeroed heap spans may not be
resident yet, but the reduction becomes visible after traffic touches those
buffers.

### 2. Make WireGuard device creation lazier

Almagest currently schedules config apply during `OnInit`, which creates a
`wgo.Device` even before links are active. This mostly costs goroutines and
stack reservation, not the main heap, but it is still visible:

```text
two unlinked WireGuard nodes: 132 goroutines
four unlinked WireGuard nodes: 262 goroutines
```

Lazy creation would improve idle graphs containing configured but unconnected
WireGuard nodes.

This is secondary because active linked WireGuard nodes still pay the configured
TUN reader buffer cost until `wgo` buffering becomes lazy or bounded.

### 3. Tune native UDP receive batching

For native Linux binds, `batchudp` defaults to batch size 128 when the
WireGuard node's `batchSize` is left at `0`. That can allocate large
receive-side message buffer batches in WireGuard.

Possible improvements:

- set `almagest/WireGuard.batchSize` explicitly for native low-memory profiles,
- reduce default batch size for low-memory daemon profiles,
- allocate receive message buffers lazily,
- return receive buffers to a bounded pool after idle periods.

### 4. Bound or drain `sync.Pool` retention after bursts

`wgo` uses `sync.Pool` for message buffers. This is good for throughput, but
after bursts it can retain many 65 KiB buffers until GC and runtime pool
behavior release them.

Consider an explicit bounded pool or pressure-aware trimming for daemon
environments where memory is more important than peak throughput.

The existing `--no-pool` flag is useful for isolating the daemon global pool in
experiments, but the paired load checks above show that it is not enough by
itself to lower heavy-load RSS for `DefaultTun`/`Spoofer` graphs.

### 5. Use CPU limits as a secondary low-memory profile knob

For WireGuard-heavy graphs, running the daemon in an environment with fewer
available CPU cores reduces the default `wgo` worker count because workers scale
from `runtime.GOMAXPROCS(0)`. In Docker, a one-CPU cpuset reduced the
two-WireGuard graph from 203 goroutines to 51 goroutines and from 16 idle
threads to 6 idle threads.

This can reduce RSS by a few MiB at idle and more after small bursts, but it is
not the primary memory fix. The retained packet-buffer heap remained close to
the unconstrained run after forced GC. Use CPU limits or cpusets when the daemon
should have a small scheduler/thread footprint, and use `WireGuard.batchSize`
or deeper `wgo` buffer changes for the large per-device buffer budget.

## Most Likely Code Areas To Change

Primary:

```text
github.com/asciimoth/wgo/device/send.go
github.com/asciimoth/wgo/device/pools.go
github.com/asciimoth/wgo/device/device.go
```

Secondary:

```text
github.com/asciimoth/wgo/device/receive.go
github.com/asciimoth/batchudp/bind_std.go
lib/node_wireguard_device.go
lib/node_wireguard.go
```

## Conclusion

The high memory usage is mainly a WireGuard buffering design issue exposed by
Almagest's node graphs. Each active WireGuard node can retain tens of MiB due
to full-size packet buffers allocated per batch slot. Graphs with multiple
WireGuard nodes scale linearly at idle, and real traffic/native binds can push
those allocations into resident memory.

The basic daemon without nodes is small by comparison: roughly 17-20 MiB RSS in
the measurements here, with only about 2 MiB private dirty memory in the static
Docker run. Admin listeners, log rings, config parsing, and the empty engine are
not the cause of 100 MiB+ reports. Running the same empty daemon with
`--no-pool` stayed in the same band, about 19 MiB RSS.

Under load, RSS can rise much higher than idle heap profiles suggest. A
Docker-isolated non-WireGuard `tun2net` graph rose from about 23 MiB idle RSS
to about 94 MiB after a mostly successful 1,000-request burst, and to about
214 MiB under a heavier 3,000-request burst with some client failures. A
second paired `tun2net` check using the current daemon image completed all
1,000- and 3,000-request bursts both with and without `--no-pool`; after-load
RSS was about 105/241 MiB with the default pool and about 107/246 MiB with
`--no-pool`. A Docker-isolated two-WireGuard graph rose from about 34 MiB idle
RSS to about 271 MiB after 400 SOCKS5 HTTP requests with 1 MiB responses. That
growth is consistent with packet buffers, netstack/TCP buffers, daemon buffer
pools, and Go runtime retention after traffic bursts.
Companion forced-GC checks still showed about 94/235 MiB RSS after the
successful 1,000-/3,000-request `tun2net` bursts, so those rows are not just
waiting for the next ordinary GC.

The new `almagest/WireGuard.batchSize` field is an effective WireGuard-specific
memory knob. In the Docker-isolated two-WireGuard graph, setting both nodes to
`batchSize: 1` reduced the small-response 2,000-request after-load RSS from
about 171 MiB to about 142 MiB. With 1 MiB responses, it reduced after-load RSS
from about 490 MiB to about 435 MiB. All requests completed in these checks,
but these runs do not prove that `batchSize: 1` is a good default for high PPS
or native-network workloads; smaller batches can trade memory for more syscalls
and lower packet burst throughput. The forced-GC companion checks showed the
same direction for the small-response run, and showed a much larger post-GC
drop for the large-response `batchSize: 1` run, from the hundreds of MiB range
to about 208 MiB RSS.

Lowering `VTun.endpointCh` and `Spoofer.queueLength` is useful as a backpressure
knob, not as a primary memory fix. In the matching 3,000-request Docker check,
reducing both values from 1024 to 64 still left after-load RSS around 228 MiB,
only about 2 MiB below the default-queue run. The forced-GC companion run did
not show a reliable reduction either.

Running with fewer available CPU cores also helps, but less directly. In the
two-WireGuard Docker graph, a one-CPU cpuset reduced idle RSS from about
29 MiB to about 26 MiB and reduced a 500-request after-GC RSS from about
79 MiB to about 59 MiB. That came with a large drop in goroutines and threads,
while live heap after forced GC stayed around 60 MiB in all three CPU modes.
CPU limits are therefore useful for trimming worker/thread overhead in small
deployments, not for eliminating the dominant WireGuard and gVisor/application
buffer retention.

Using a smaller WireGuard batch size gives the largest immediate idle and
post-load memory reduction for WireGuard-heavy graphs. Further `wgo` buffer
allocation changes, especially lazy or bounded message-buffer allocation, would
be needed to reduce the fixed per-batch cost without relying only on smaller
batches. Traffic-heavy DefaultTun/Spoofer graphs may also need separate
pressure controls for gVisor and daemon buffer retention.
