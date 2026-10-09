# Daemon isolation performance budgets

Status: numeric checkpoint 9 budget proposal. The Almagest maintainer who
accepts and merges checkpoint 9 is the approval owner. Merge approval freezes
these values before data-path selection. A later change to a limit needs a
reviewed update with measurements; a failed candidate does not justify changing
the budget after results are known.

These are qualification gates, not claims about the current implementation.
By explicit maintainer direction at checkpoint 13, conservative broker relays
are selected and their design is frozen before full performance qualification.
The values below remain unchanged and must pass before production enablement;
see the [selection decision](DAEMON_ISOLATION_DATA_PATH_DECISION.md).

## Acceptance budgets

All percentages are relative to the same-host, same-build-flags, in-process
native baseline. CPU is combined R and N CPU time. Memory is combined private
resident memory above direct mode after caches and buffer pools reach steady
state. A candidate must meet every applicable row.

| Workload | Linux budget | Windows budget |
| --- | ---: | ---: |
| Interactive TCP/UDP request-response added median latency | <= 250 us | <= 500 us |
| Interactive TCP/UDP request-response added p99 latency | <= 1 ms | <= 2 ms |
| Low-rate TUN added p99 one-way latency | <= 500 us | <= 1 ms |
| Bulk TCP/UDP/TUN goodput loss | <= 10% | <= 15% |
| 64-byte TUN/UDP packets-per-second loss at saturation | <= 15% | <= 20% |
| Bulk combined CPU increase at equal useful throughput | <= 20% | <= 25% |
| Interactive combined CPU increase at equal request rate | <= 15% | <= 20% |
| Idle memory increase for R plus N | <= 32 MiB | <= 48 MiB |
| Steady-state memory increase under one saturated channel | <= 64 MiB | <= 96 MiB |
| Control request p99 while one data channel is saturated | <= 100 ms | <= 150 ms |
| Resource close/cancel p99 while one data channel is saturated | <= 250 ms | <= 350 ms |
| Packet loss introduced below offered saturation point | 0 | 0 |

Direct mode has a separate regression budget against the section 1 source
baseline on the same host:

| Direct-mode metric | Maximum regression |
| --- | ---: |
| TCP, UDP, and TUN goodput or packets per second | 2% |
| Median and p99 latency | 2% or 25 us, whichever is larger |
| CPU at equal useful throughput/request rate | 2% |
| Idle and steady-state private resident memory | 4 MiB |
| Transport/serialization allocations or copies | zero |
| Internal role processes, IPC channels, or per-packet queues | zero |

The zero direct-mode items are invariant checks and are not subject to
statistical tolerance. Native fast paths stay enabled.

## Workloads and environments

Run Linux on a physical x86-64 host with a real `/dev/net/tun`, at least four
physical CPU cores, at least 8 GiB RAM, and a fixed performance power profile.
Run Windows on a physical or dedicated VM host with the qualified Windows
version, real Wintun ring, at least four assigned physical cores, and at least
8 GiB assigned RAM. Do not accept nested or oversubscribed CI measurements as
the final result. Record CPU, kernel/Windows build, Go version, backend and
Wintun versions, memory, power mode, affinity, queue counts, build tags, and all
benchmark commands.

For native TUN, connected TCP, TCP listener, connected UDP, unconnected UDP,
and each platform-specific channel, compare in-process baseline, refactored
`--no-isolation`, single-record relay, bounded-batch relay, shared-memory relay,
and eligible direct handles. Use both directions; IPv4 and IPv6; 64, 256, 512,
1,200, 1,500, and effective-maximum byte packets; one and many flows; low-rate
interactive request-response; bulk streams; small-packet saturation; one and
multiple TUN queues where supported; matcher enabled and disabled; and a
representative WireGuard workload. Measure cold start and at least 60 seconds of
steady state after warm-up.

Record goodput, packets per second, loss, retransmission, median/p95/p99 latency,
jitter, per-role CPU, context switches, migrations, wakeups, syscalls, IPC
messages, copied bytes, allocations, queue depth, batch distribution,
backpressure time, drops, memory, retained buffer capacity, and control response
time. Security authority tests are a separate mandatory gate. A fast candidate
that exceeds the allowed authority is rejected.

## Comparison rules

- Interleave baseline and candidate runs to reduce thermal and time drift. Use
  at least seven measured runs for each acceptance workload after two warm-ups.
- Use the median run for throughput, CPU, and memory. Use the pooled p99 only
  when each run contains at least 100,000 latency samples; otherwise use the
  worst per-run p99.
- A result within 1% of a percentage limit is inconclusive. Repeat it with at
  least 15 measured runs. A candidate passes only when the repeated result is
  below the limit.
- Keep offered load equal when latency and CPU are compared. For saturation
  comparisons, report both maximum stable load and the equal-load point.
- Normal scheduler runs decide acceptance. Pinned-CPU runs can diagnose a
  failure but cannot replace them.
- Any skip leaves that platform/resource decision open. Do not average Linux
  and Windows, interactive and bulk, directions, resource types, or packet
  sizes to hide a failure.

Repeat selection measurements after material changes to framing, buffer
ownership, IPC primitive, native backend, Go runtime, Wintun, TUN queueing, or
process restrictions.
