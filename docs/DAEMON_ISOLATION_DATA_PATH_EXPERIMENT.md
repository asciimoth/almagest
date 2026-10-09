# Daemon isolation data-path experiment

Status: checkpoint 12 harness implementation and preliminary Docker/WinVM
timing are complete. These short-sample runs do not meet the acceptance-run
duration requirements. The production daemon remains unchanged and still
requires `--no-isolation`.

## Scope and commands

The harness is in [`dev/data-path`](../dev/data-path/README.md). The Linux run
uses disposable Docker network and mount namespaces:

```sh
just data-path-experiment
```

The Windows run uses the locked disposable Windows Server 2022 VM:

```sh
just windows-data-path-experiment
```

Do not run either command for acceptance while unrelated host workloads are
active. Docker or the VM contains every native network change, but host CPU,
memory, power, and scheduler activity can invalidate performance numbers.

The Linux launcher uses the checkpoint 10 restricted runtime profile. The
Windows launcher uses both checkpoint 11 AppContainer profiles. R cannot create
native sockets or open TUN/Wintun itself. N owns all native resources and gives
R only the candidate channel under test.

## Implemented candidates and workloads

The harness compares these candidates:

- broker-owned in-process native I/O;
- one-record and 16-record broker relays;
- one-slot bounded shared-memory relays with explicit event notifications;
- broker-configured direct TCP, connected UDP, and eligible Linux TUN handles;
- bounded five-tuple request/reply with the real platform matcher backend.

Unconnected UDP remains broker-owned because a direct socket permits new
destinations. Windows records whether broker-to-AppContainer Winsock duplication
is available. Wintun direct transfer is not claimed because the current API has
no supported transferable session contract.

The matrix includes TCP, connected and unconnected UDP, native TUN/Wintun,
IPv4 and IPv6, 64, 256, 512, 1,200, and 1,500 byte packets, plain and
WireGuard-shaped TUN traffic, cold and warm runs, and disabled, normal, and
overloaded diagnostic logging. The TUN packet size includes the IP and UDP
headers. Every candidate verifies payload integrity and packet boundaries.

The matcher stage uses live TCP and UDP tuples from N. It compares direct
backend calls with one bounded RPC per lookup. It rejects malformed input and
also submits a valid tuple that has no observation token or lease.

Separate control capacity is exercised while a large batched relay is active.
The result records control p99 and requires the saturated data workload to
complete. Linux records runtime and broker CPU, context switches, allocations,
heap use, and broker maximum RSS. Windows records runtime and broker CPU and
runtime allocation and heap counters. Raw records also contain duration,
goodput or packets per second, p50/p95/p99 latency, IPC messages, and copied
bytes.

Windows Wintun records a timed-out reply as packet loss and continues the
measurement. It discards a late reply whose payload belongs to another batch
item. Each Windows record includes `droppedPackets`; useful-rate calculations
exclude dropped packets. A warm workload with a nonzero drop count fails the
zero-loss condition.

## Evidence layout and validation

Linux evidence is written below `artifacts/data-path/<run-id>/`. Each logging
mode has separate socket, matcher, TUN, and diagnostic files. The runner checks
the restriction identity and complete candidate matrix, then writes
`analysis-linux.json`. The analysis uses warm-run medians and compares added
p99, useful-rate loss, combined CPU, and saturated control p99 with
[`DAEMON_ISOLATION_PERFORMANCE_BUDGETS.md`](DAEMON_ISOLATION_PERFORMANCE_BUDGETS.md).
It reports each candidate separately; it does not average a failed candidate
with a passing candidate.

Windows evidence is stored in the normal WinVM artifact archive. The host
validator requires both profiles, all three logging modes, native Wintun in the
service-derived profile, both address families, relay/shared candidates,
matcher semantics, and saturated-control evidence. A missing direct Winsock
candidate is retained as an explicit platform result rather than replaced by a
native fallback.

Each environment record includes the source revision, dirty state, container
or VM identity, OS, Go version, and run parameters. Raw evidence is ignored by
git. The preliminary retained artifact paths and budget summary follow.

## Preliminary results, 2026-10-09

The operator reserved the host before both runs. The host had one AMD Ryzen 5
5600H processor with 6 cores, 12 threads, and 27 GiB of memory. Its CPU governor
was `powersave`; no CPU affinity was set. The source revision was
`d9c0f514e27836a9057cb43a7f9f69d9ea382b78` with uncommitted experiment changes.

The Linux Docker run used Linux 7.2.8, seven runs, and 200 samples per run. Its
retained directory is `artifacts/data-path/20261009T084713Z-121174`. Analysis
contains 732 profile-and-logging-mode comparisons. Added median latency passed
all applicable limits, control p99 was 43,032 ns and passed, but 240 p99,
248 useful-rate, and 420 CPU comparisons failed their provisional budgets.
CPU could not be compared for 312 records. No packet loss was recorded.

The Windows Server 2022 build 20348 VM used four virtual CPUs, 8 GiB of memory,
Go 1.26.6, and Wintun driver 0.14. The full archive is
`artifacts/winvm/run-9r0vIA0p/guest-artifacts.tar`; its analysis is
`artifacts/winvm/run-9r0vIA0p/analysis-windows.json`. The service phase took
494 seconds and the caller phase took 117 seconds. Analysis contains 1,104
profile-and-logging-mode comparisons. Added median latency passed all
applicable limits and the worst control p99 was 823,300 ns, but 154 p99,
695 useful-rate, and 468 CPU comparisons failed. CPU could not be compared for
577 records. Wintun lost 81 of the attempted packets, including 67 in warm
runs; 58 warm candidate/workload comparisons therefore failed the zero-loss
condition. Socket and matcher paths recorded no loss.

The original Windows run metadata says `artifact-validation` failed because
the validator treated a present zero-nanosecond control sample as absent. The
archive contains all six complete result files. The corrected validator accepts
the retained archive, and analysis treats zero as a clock-resolution result.
No measurement was rerun or changed for this correction.

## Preliminary interpretation

The isolation transports are functional, but the current evidence does not
qualify a production data path against the section 9 budgets.

Batching is necessary for high packet rates. For TCP and UDP, an unbatched
relay lost approximately 40–65 percent of useful rate on Linux and 45–90
percent on Windows. A batch of 16 often matched or exceeded the sequential
one-packet native rate. This rate improvement does not make the batch latency
equal to single-packet latency; the result represents amortized IPC work.

The shared-memory candidate was usually faster and used less CPU than the
ordinary broker relay. The difference was not sufficient to remove the main
budget failures. Shared memory is therefore a possible optimization, not a
qualified selection.

Linux TCP and UDP relay latency was small. Typical added p99 was 13–36
microseconds and passed the provisional latency budget. The same paths still
failed many rate and CPU comparisons. Their measured CPU cost was commonly two
to five times the in-process baseline. Windows TCP and UDP typically added
0.6–1.0 ms at p99, but some tails exceeded the 2 ms budget.

One bounded matcher RPC added a median p99 of approximately 169 microseconds
on Linux and 585 microseconds on Windows. These values passed the latency
limits. Linux matcher rate was approximately 14 percent below native at the
median and failed the 10 percent rate limit in half of the comparisons. The
Windows rate result is not dependable because several native matcher intervals
were below the clock resolution.

TUN and Wintun are the principal blockers. Linux relay TUN added approximately
2.4 ms p99 for one packet and 17–18 ms for a batch of 16, above the 0.5 ms
limit. Windows Wintun added approximately 0.7 ms p99 for one packet and
2.1–2.5 ms for a batch, so the single-packet median was within the 1 ms limit
but the batched median was not. Windows Wintun also lost 81 packets. Loss under
this below-saturation workload is a qualification failure even when useful
rate and latency pass.

Direct handles had the lowest IPC latency, but they did not consistently meet
the rate or CPU budgets. They also remain subject to the checkpoint 13
authority and revocation review. Performance alone cannot approve a direct
handle.

Two measurement limitations affect interpretation:

- The Linux raw direct-TUN candidate reported approximately 65 times the rate
  of the native TUN baseline. The native path uses the higher-level TUN wrapper,
  while the direct candidate uses raw file operations. This identifies large
  wrapper overhead, but it is not a valid isolation-overhead comparison until
  both candidates use equivalent packet-processing work.
- Windows wall-clock and process CPU accounting were too coarse for some short
  cases. Several wall-clock samples were zero and CPU values changed in
  approximately 15.6 ms increments. This caused missing CPU comparisons and
  makes short Windows rate results unsuitable for selection.

The next experiment must use equivalent native and candidate implementations,
separate batch throughput from per-packet latency, and run each acceptance
workload for at least 60 seconds. Before that run, investigate the Linux TUN
wrapper allocation/read path and the Wintun packet losses. Retain batching for
bulk traffic, but keep an independent bounded control channel and a low-latency
single-record path. Do not select a production data path until checkpoint 13
also completes its security and revocation review.

## Qualification conditions still open

Checkpoint 12 acceptance remains open. The retained runs use only 200 samples
per case and do not provide 60 seconds of steady state per acceptance workload.
They also do not yet cover two explicit warm-up runs, the supported maximum
packet size beyond 1,500 bytes, queue-count variation, many-flow variation,
both traffic directions as separate records, retransmits, jitter, wakeups,
syscalls, or queue depth. An acceptance run must record these items. A result
within one percent of a budget needs 15 measured repetitions.

The final evidence review must add the same-host original-direct and current
`--no-isolation` comparison. The original source revision is
`765792710fc25ed4b242934aa68bf7973aa5fd3f`; the current direct path must retain
native fast paths and must not contain an isolation transport. A different
machine or a skipped native TUN/Wintun stage is not acceptance evidence.

This checkpoint records measurements only. The
[checkpoint 13 security decision](DAEMON_ISOLATION_DATA_PATH_DECISION.md) adds
hostile-handle evidence and selects conservative broker relays for version 1.
The design is frozen by maintainer direction; these preliminary timings do not
qualify the selected implementation or change the performance budgets.
