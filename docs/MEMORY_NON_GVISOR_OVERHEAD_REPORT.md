# Almagestd Non-gVisor Memory Overhead Report

Date: 2026-07-03

## Scope

This report follows up the native-TUN tun2socks comparison in
`MEMORY_CONSUMPTION_REPORT.md`. The question here is narrower: when
`almagestd` and tun2socks both perform the same TUN-to-SOCKS job, what extra
memory does Almagest spend outside the shared gVisor netstack class, and what
can realistically reduce it?

All measurements below were run in fresh Docker containers with:

```text
docker run --network none --cap-add NET_ADMIN --device /dev/net/tun
```

The reproducible harness is:

```text
tools/memory/non_gvisor_overhead.sh
tools/memory/tun_compare_runner.py
```

The full Almagest graph was:

```text
DefaultTun -> Spoofer -> SocksClient -> OutNetwork
```

Both tools routed only `10.199.20.1/32` into a native TUN with MTU 1500 and
used an in-container SOCKS5 endpoint at `127.0.0.1:1080`. That SOCKS endpoint
mapped every CONNECT to a fixed-body local HTTP server, so the proxy's own
upstream socket could not loop back into the tested TUN route.

## Executive Summary

The extra memory is split into two different problems.

First, almagestd has a higher fixed process baseline before traffic. In this
native Docker setup, an empty native almagestd process with no graph retained
about 21.5 MiB RSS, while tun2socks with its TUN, gVisor stack, and SOCKS
proxy path retained about 12.1 MiB. Most of that fixed delta is private-clean
memory, not dirty heap, so GC tuning will not remove it. It is the cost of the
larger daemon binary, imported platform/sysnet/admin/graph/node code, and
startup paths touched by the general-purpose engine.

Second, almagestd grows more under connection churn. The 3,000-request burst
grew the full graph by about 127 MiB RSS in this run, while tun2socks grew by
about 30 MiB in the previous matching run. The likely Almagest-specific
contributors are per-flow engine worker tracking, detached `gonnect` wrapper
operations, generic SOCKS client layers, `io.Copy` buffer allocation outside
the daemon pool, and Go heap/span retention after these allocations have been
touched.

The highest-impact reduction candidates are:

1. Stop tracking per-connection Spoofer copy goroutines as engine workers.
2. Use pooled `io.CopyBuffer` buffers in the Spoofer TCP forwarding path.
3. Reduce `gonnect.DetachedNetwork` hot-path goroutine churn for `Dial`.
4. Add a minimal SOCKS5 CONNECT client mode for simple TCP proxying.
5. Document/expose runtime memory pressure knobs (`GOGC`, `GOMEMLIMIT`) and
   Spoofer TCP buffer defaults for constrained deployments.
6. Longer term, support a lean daemon build/profile that excludes unused admin,
   panel, debug, platform, and node-class code.

## Idle Decomposition

Fresh-container idle results:

| Case | What is active | RSS | Private clean | Private dirty | Threads |
| --- | --- | ---: | ---: | ---: | ---: |
| almagestd empty native | native system + daemon + empty workspace | 21.5 MiB | 16.8 MiB | 3.4 MiB | 10 |
| almagestd `OutNetwork` | empty + system outbound network node | 22.2 MiB | 17.5 MiB | 3.5 MiB | 10 |
| almagestd `SocksClient -> OutNetwork` | parsed SOCKS client and detached network wrappers | 23.1 MiB | 18.1 MiB | 3.8 MiB | 10 |
| almagestd `DefaultTun` only | native TUN, address, route, sysnet-linux routing state | 26.1 MiB | 18.1 MiB | 6.7 MiB | 11 |
| almagestd full graph | DefaultTun, Spoofer netstack, SocksClient, OutNetwork | 28.2 MiB | 19.6 MiB | 7.4 MiB | 13 |
| tun2socks | native TUN, gVisor stack, SOCKS proxy path | 12.1 MiB | 8.7 MiB | 2.1 MiB | 8 |

Interpretation:

- The biggest idle difference exists before Almagest builds any graph. Empty
  native almagestd is already about 9.4 MiB RSS above tun2socks, but only about
  1.3 MiB higher in private dirty memory. That points mostly at executable code
  and read-only data pages touched by daemon startup, not live mutable state.
- `OutNetwork` and `SocksClient` are small idle additions, together about
  1.6 MiB RSS over the empty daemon.
- `DefaultTun` is the largest idle graph component in this setup: about
  4.6 MiB RSS over empty almagestd, mostly private dirty memory. That is native
  TUN/sysnet-linux/netlink/routing state, not gVisor.
- The full graph adds only about 2.1 MiB RSS over `DefaultTun` at idle. Empty
  gVisor queues are not the primary idle problem in this path.

## Load Tuning Matrix

All rows below use the full native graph and 3,000 1 KiB HTTP responses at
parallelism 80. Values are after the workload, not forced-GC companion values.

| Case | Changed knob | Result | Elapsed | After RSS | After private dirty | Notes |
| --- | --- | --- | ---: | ---: | ---: | --- |
| baseline | none | 3,000 OK | 4.0 s | 155.2 MiB | 133.4 MiB | reference run |
| `--no-pool` | disable daemon `leveledBufPool` | 3,000 OK | 3.9 s | 151.1 MiB | 129.1 MiB | small/noisy; not the main source |
| queue 64 | `Spoofer.queueLength=64` | 3,000 OK | 3.9 s | 145.3 MiB | 123.7 MiB | modest reduction |
| TCP buffer 32 KiB | send/receive defaults 32 KiB | 3,000 OK | 3.8 s | 146.6 MiB | 124.6 MiB | modest reduction |
| TCP buffer 64 KiB | send/receive defaults 64 KiB | 3,000 OK | 3.8 s | 146.7 MiB | 124.7 MiB | similar to 32 KiB |
| TCP forward window 32 KiB | `tcpForwardWnd=32768` | 3,000 OK | 3.9 s | 150.1 MiB | 127.8 MiB | weaker than buffer defaults |
| `GOMEMLIMIT=80MiB` | Go memory limit | 2,982 OK, timeouts | 20.3 s | 131.9 MiB | 109.7 MiB | saves memory but hurts latency/reliability here |
| `GOGC=25` | more frequent GC | 3,000 OK | 3.9 s | 141.6 MiB | 119.6 MiB | useful runtime tradeoff |

The tuning results reinforce that the daemon pool is not the main retained
source. The pool change was small and within normal run-to-run noise. The
Spoofer queue and TCP buffer knobs help, but they affect the gVisor/TCP side
of the workload and do not explain the whole tun2socks delta. Runtime memory
pressure can lower RSS, but `GOMEMLIMIT=80MiB` was too aggressive for this
parallel workload.

## Source-Level Findings

### Fixed Daemon Footprint

The empty native daemon includes:

- almagestd startup and config parsing,
- log factory and command runner,
- native `sysnet-linux` initialization,
- Pasta workspace/engine setup,
- built-in node class registration,
- admin API machinery compiled in, though listeners were disabled.

The important measurement is the private-clean split. Empty almagestd had
about 16.8 MiB private clean versus tun2socks' 8.7 MiB. That memory is not a
packet queue or a Go heap object that can be freed after load. Lowering it
requires reducing what the binary imports or what startup touches.

Reduction options:

- Add a lean build/profile for headless forwarding that excludes the web panel,
  admin websocket/session code, debug nodes, unused node classes, and native
  platform features not needed for a specific deployment.
- Keep `lib/` modularity but make daemon node registration feature-scoped, so
  a forwarding-only binary does not touch every node class and its imports.
- Avoid native `sysnet-linux` initialization when a config has no native TUN
  nodes. This would not help the TUN-to-SOCKS graph, but it would lower empty
  and pure mock-network native runs.

### Native DefaultTun/sysnet-linux

`DefaultTun` alone added about 4.6 MiB RSS over empty almagestd. This is the
main non-gVisor graph idle cost.

Relevant path:

```text
lib/node_default_tun_runtime.go
  defaultTunNode.rebuildDefaultTun
github.com/asciimoth/sysnet-linux@v0.2.8/default_tun.go
  System.BuildDefaultTun
github.com/asciimoth/tuntap@v0.3.18
github.com/vishvananda/netlink@v1.3.1
```

A pprof companion run showed startup allocations through:

```text
sysnet-linux/tun.SetTunAddrs
sysnet-linux/routing.Manager.Apply
vishvananda/netlink.(*NetlinkSocket).Receive
asciimoth/tuntap.CreateTUNFromFile / newUDPGROTable
```

Reduction options:

- Provide a "route-only/minimal native TUN" mode for configs that do not need
  default-route replay, process marking, DNS integration, killswitch support,
  or safe route tables.
- Avoid duplicate configure/rebuild work during restore/link replay. The node
  currently configures on `OnReady` and link events; it already has equivalent
  checks, but native restore still touches netlink/routing paths.
- Keep using debug `vtun-socks` for tests where native host integration is not
  the subject; it avoids this sysnet-linux cost and host-route machinery.

### Graph Wrappers and Resource Lifecycle

Almagest emits network and TUN values through detached wrappers:

```text
lib/type_network.go
  NetworkOutputCache.Send -> gonnect.DetachNetwork
lib/type_tun.go
  TunOutputCache.Send -> tun.Detach
```

These wrappers are valuable because they make link/node cleanup reliable. They
also create maps, close tracking, generation state, and extra call layers.
The full graph has at least:

- a detached wrapper for `OutNetwork` into `SocksClient`,
- a `socksgo.Client` network emitted to `Spoofer`,
- a detached TUN wrapper from `DefaultTun` into `Spoofer`,
- a second detached TUN inside `Spoofer` after `tun.Detach`.

Reduction options:

- Add a fast path for single-consumer links where ownership can be transferred
  instead of wrapped, if the graph topology guarantees no fanout and no select
  middleware.
- Coalesce wrapper layers when a wrapper is immediately re-wrapped by the next
  node. This needs care because each wrapper has its own lifecycle boundary.
- Keep wrappers for correctness by default, but allow an explicit "unsafe fast
  forwarding" mode for controlled benchmark/appliance deployments.

### DetachedNetwork Dial Goroutine

`gonnect.DetachedNetwork.Dial` runs operations through `runDetachedOp`, which
spawns a goroutine even when the wrapped network is a native context-aware
`net.Dialer` path:

```text
github.com/asciimoth/gonnect@v0.31.0/detachable.go
  DetachedNetwork.Dial
  runDetachedOp
```

In this graph, each proxied TCP flow goes through `SocksClient`, and the SOCKS
client connects to the proxy through the detached `OutNetwork` wrapper. That
adds a short-lived goroutine per upstream SOCKS connection, in addition to
Spoofer's two forwarding goroutines.

Reduction options:

- Add a direct-call fast path for operations that are known to honor context
  cancellation, especially native `Dial`.
- Keep the goroutine fallback for operations that may ignore context or need
  late-result cleanup.
- Pass a lightweight, non-notifying spawner for high-frequency detached
  operations if lifecycle tracking is still required.

### Spoofer TCP Forwarding Workers

For each intercepted TCP flow, Almagest starts two engine-tracked workers:

```text
lib/node_spoofer_forward.go
  forwardSpooferTCP
    spawner.Spawn(io.Copy(upstream, c), "spoofer tcp upstream")
    spawner.Spawn(io.Copy(c, upstream), "spoofer tcp downstream")
lib/worker.go
  workerTracker.spawn
```

This means the 3,000-request workload creates about 6,000 engine worker records
and worker lifecycle notifications. Those records are deleted when workers
finish, but the allocation churn warms goroutine stacks, maps, notification
objects, log call paths, and Go heap spans. tun2socks also uses goroutines, but
it does not route every copy goroutine through Almagest's engine worker
tracking machinery.

Reduction options:

- Treat per-flow copy goroutines as untracked short-lived work. Keep aggregate
  counters on the Spoofer node instead of exposing every flow as an engine
  worker.
- Add a lightweight worker path with no `WorkerSnapshot`, no notification
  fanout, and no node snapshot cache.
- Track only long-lived runtime workers in the engine worker tracker.

This is likely one of the highest-impact code changes for burst workloads.

### Copy Buffers

`forwardSpooferTCP` currently calls `io.Copy` directly. That does not use the
daemon `leveledBufPool`, which explains why `--no-pool` does not materially
improve this workload.

Reduction options:

- Use `io.CopyBuffer` with buffers from the existing daemon pool, or a
  Spoofer-local `sync.Pool`.
- Keep the buffer size explicit, for example 16-32 KiB, and measure throughput
  impact.
- Preserve `io.Copy` fast paths: Go's `copyBuffer` still uses `WriterTo` or
  `ReaderFrom` when available before using the provided buffer.

Expected impact is moderate. It will not remove gVisor TCP state, but it should
reduce per-flow allocation churn and post-load heap span retention.

### SOCKS Client Generality

`almagest/SocksClient` uses `github.com/asciimoth/socksgo`, a general SOCKS
client supporting SOCKS4/4a/5, UDP, TLS, WebSocket, GOST extensions, Tor lookup,
auth, smux, filters, and custom networks. In the comparison workload, only a
plain SOCKS5 CONNECT path is needed.

Reduction options:

- Add a minimal SOCKS5 CONNECT-only mode for `SocksClient`.
- Reuse parsed proxy addresses and prebuilt no-auth request pieces.
- Avoid extension/default setup for plain `socks5://host:port` when UDP, BIND,
  TLS, WebSocket, GOST, Tor, and auth are unused.

This is more likely to reduce fixed and per-connection allocation overhead than
idle RSS by itself, but it is a good fit for the exact TUN-to-SOCKS appliance
case.

### Runtime Heap Retention

The Go runtime keeps heap spans and goroutine stack capacity warm after bursty
traffic. In the tuning run:

- `GOGC=25` reduced after-load RSS from 155.2 MiB to 141.6 MiB with no request
  failures.
- `GOMEMLIMIT=80MiB` reduced after-load RSS to 131.9 MiB but caused timeouts
  and made the run about five times slower.

Reduction options:

- Document recommended `GOGC` ranges for memory-constrained deployments.
- Add protected daemon config that calls `debug.SetGCPercent` and
  `debug.SetMemoryLimit`, so users do not need environment variables.
- Treat `GOMEMLIMIT` as an SLO tradeoff knob, not a free memory reduction.

## Suggested Implementation Order

1. Add a low-overhead Spoofer per-flow path: direct goroutines or a lightweight
   worker tracker instead of engine worker tracking for every TCP copy worker.
2. Switch Spoofer TCP copy to `io.CopyBuffer` with a measured pool-backed
   buffer.
3. Benchmark `gonnect.DetachedNetwork` direct-call fast paths for native
   context-aware operations.
4. Add a plain SOCKS5 CONNECT fast path in `SocksClient`.
5. Add daemon runtime memory knobs (`GOGC`, `GOMEMLIMIT`) to protected config
   and document tested tradeoffs.
6. Design a lean/headless almagestd build profile only after the hot-path
   allocation churn is reduced, because the fixed idle baseline is secondary
   for burst workloads.

## What Not To Chase First

- `--no-pool`: the measured effect is small because the hottest TCP forwarding
  path does not use the daemon pool.
- `Spoofer.queueLength` alone: reducing it from 1024 to 64 helped only
  modestly here and mainly caps queued packet backlog, not general TCP state.
- WireGuard knobs: this comparison does not include WireGuard; `batchSize` is
  still important for WireGuard graphs but irrelevant to this TUN-to-SOCKS
  delta.
