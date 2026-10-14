# Daemon isolation data-path experiment

Status: checkpoint 12 harness implementation and preliminary Docker/WinVM
timing are complete. These short-sample runs do not meet the acceptance-run
duration requirements. At this checkpoint, the production daemon still
required `--no-isolation`. Linux amd64 now supports isolation by default;
Windows remains in direct mode by default while isolation support is in
progress.

## Archived scope

This document preserves checkpoint-12 measurements. The standalone benchmark
harness and its Linux probe commands were removed after checkpoint 13 selected
broker-owned relays and production qualification accepted performance as
diagnostic only. The WinVM code retains its Windows analysis helper for old
artifact compatibility, but this checkpoint is not a current project gate.

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
exclude dropped packets. Packet loss is recorded as diagnostic data.

## Evidence layout and validation

Linux evidence was written below `artifacts/data-path/<run-id>/`. Each logging
mode had separate socket, matcher, TUN, and diagnostic files. The runner checked
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
git. The preliminary retained artifact paths and measurement summary follow.

## Preliminary results, 2026-10-09

The operator reserved the host before both runs. The host had one AMD Ryzen 5
5600H processor with 6 cores, 12 threads, and 27 GiB of memory. Its CPU governor
was `powersave`; no CPU affinity was set. The source revision was
`d9c0f514e27836a9057cb43a7f9f69d9ea382b78` with uncommitted experiment changes.

The Linux Docker run used Linux 7.2.8, seven runs, and 200 samples per run. Its
retained directory is `artifacts/data-path/20261009T084713Z-121174`. Analysis
contains 732 profile-and-logging-mode comparisons. Against the former
provisional targets, 240 p99, 248 useful-rate, and 420 CPU comparisons were
outside the target values. Control p99 was 43,032 ns.
CPU could not be compared for 312 records. No packet loss was recorded.

The Windows Server 2022 build 20348 VM used four virtual CPUs, 8 GiB of memory,
Go 1.26.6, and Wintun driver 0.14. The full archive is
`artifacts/winvm/run-9r0vIA0p/guest-artifacts.tar`; its analysis is
`artifacts/winvm/run-9r0vIA0p/analysis-windows.json`. The service phase took
494 seconds and the caller phase took 117 seconds. Analysis contains 1,104
profile-and-logging-mode comparisons. The worst control p99 was 823,300 ns.
Against the former provisional targets, 154 p99, 695 useful-rate, and 468 CPU
comparisons were outside the target values. CPU could not be compared for 577
records. Wintun lost 81 of the attempted packets, including 67 in warm runs.
Socket and matcher paths recorded no loss.

The original Windows run metadata says `artifact-validation` failed because
the validator treated a present zero-nanosecond control sample as absent. The
archive contains all six complete result files. The corrected validator accepts
the retained archive, and analysis treats zero as a clock-resolution result.
No measurement was rerun or changed for this correction.

## Preliminary interpretation

The isolation transports are functional. The measurements below are accepted
as the current baseline and do not qualify or disqualify a production path.

Batching is necessary for high packet rates. For TCP and UDP, an unbatched
relay lost approximately 40–65 percent of useful rate on Linux and 45–90
percent on Windows. A batch of 16 often matched or exceeded the sequential
one-packet native rate. This rate improvement does not make the batch latency
equal to single-packet latency; the result represents amortized IPC work.

The shared-memory candidate was usually faster and used less CPU than the
ordinary broker relay. Shared memory remains a possible later optimization,
not the selected version 1 mechanism.

Linux TCP and UDP relay latency was small. Typical added p99 was 13–36
microseconds. Their measured CPU cost was commonly two to five times the
in-process baseline. Windows TCP and UDP typically added 0.6–1.0 ms at p99,
with some larger tails.

One bounded matcher RPC added a median p99 of approximately 169 microseconds
on Linux and 585 microseconds on Windows. Linux matcher rate was approximately
14 percent below native at the median in half of the comparisons. The
Windows rate result is not dependable because several native matcher intervals
were below the clock resolution.

Linux relay TUN added approximately 2.4 ms p99 for one packet and 17–18 ms for
a batch of 16. Windows Wintun added approximately 0.7 ms p99 for one packet and
2.1–2.5 ms for a batch. Windows Wintun also lost 81 packets. These results are
accepted as the current implementation baseline and can guide later work.

Direct handles had the lowest IPC latency. They remain rejected by the
checkpoint 13 authority and revocation review. Performance alone cannot approve
a direct handle.

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

Checkpoint 13 completed the security and revocation review and selected bounded
broker relays. The production implementation uses separate bounded control and
data lanes. Longer equivalent-work measurements remain optional optimization.

## Optional future measurements

Checkpoint 12 performance acceptance is complete. Future optimization work can
add longer steady-state runs, more packet sizes, queue and flow variation,
retransmits, jitter, wakeups, syscalls, queue depth, and same-host direct-mode
comparisons. None of these measurements is required for isolation qualification.

This checkpoint records accepted diagnostic measurements only. The
[checkpoint 13 security decision](DAEMON_ISOLATION_DATA_PATH_DECISION.md) adds
hostile-handle evidence and selects conservative broker relays for version 1.
The design is frozen by maintainer direction; these timings are not release
gates.
