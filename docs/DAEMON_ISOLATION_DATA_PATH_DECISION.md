# Daemon isolation data-path security decision

Status: implemented for the packaged Linux amd64 isolated profile. Version 1
uses broker-owned byte and packet relays for every native resource. The
[version 1 data-channel contract](DAEMON_ISOLATION_DATA_CHANNEL_CONTRACT.md) is
the production contract. Direct native handle transfer and shared memory remain
excluded. Windows keeps direct mode until its complete isolated profile passes
the production gates.

On 2026-10-09 the maintainer explicitly requested a conservative choice for
every open decision and a frozen design so implementation can continue. Current
performance is accepted under the
[performance policy](DAEMON_ISOLATION_PERFORMANCE_BUDGETS.md). Functional and
hostile-client qualification passed for packaged Linux amd64 and remains
mandatory for a future Windows profile.

## Evidence used

- **F12/P12:** The [checkpoint 12 report](DAEMON_ISOLATION_DATA_PATH_EXPERIMENT.md)
  records functional packet exchange, timing, limits, and artifact paths for
  Linux and Windows. Its short runs show rate, CPU, TUN-tail, and Wintun-loss
  costs that are accepted as diagnostic baselines. The complete workload matrix
  and original/current direct comparison are optional future measurements.
  TCP listener transfer/accept is not a measured checkpoint 12 workload.
- **L13:** The removed checkpoint-13 harness ran a hostile native probe through
  the checkpoint-10 launcher in disposable Docker network and mount namespaces.
  It uses the same UID/GID, PID namespace, capability drop, Landlock, and syscall
  filter, then adds the execution-denial filter. This deliberately hostile C
  process tests descriptor authority independently of cooperative Go wrappers.
  The broker creates real TCP, listener, connected/unconnected UDP, nonpersistent
  TUN, memfd, and eventfd resources. Every peer receive has a five-second limit;
  the container run has a 90-second limit. Missing observations fail validation.
- **W11/W12:** The retained Windows Server 2022 build 20348 amd64 archive
  `artifacts/winvm/run-9r0vIA0p/guest-artifacts.tar` contains real restricted-token
  AppContainer results for both caller and service profiles. Each normal-mode
  result has 140 direct Winsock measurement records: TCP and connected UDP,
  IPv4 and IPv6. Thus N-to-R Winsock transfer worked for those resources. A
  generic duplication limitation in the same result does not negate those
  records. Wintun transfer was not implemented. The checkpoint 11 checks also
  report rejection of R-to-N Winsock duplication, access to N's token, native
  network connection, Wintun open, and unlisted handles. These checks do not
  prove safe operation or revocation of an already transferred socket.
- **S13:** Source review covered the removed Linux prototype launcher and TUN
  broker, plus `bin/cmd/windows-isolation-probe/main_windows.go`. Their
  cooperative timing protocols were development evidence, not production
  contracts.

## Per-platform and per-resource decision

The relay selections below are final for version 1. Linux implementation and
qualification are complete. Windows implementation and production qualification
remain incomplete. Native userspace-only networks and TUNs remain in R and are
outside this matrix.

| OS/resource | Direct-handle decision | Version 1 selection and qualification notes | Evidence |
| --- | --- | --- | --- |
| Linux TUN | Reject current grant: device survives N closing its copy; device-wide teardown under hostile I/O is unqualified. | Bounded packet relay, implemented and qualified. | F12/P12, L13, S13 |
| Linux TCP connection | Reject: R can duplicate and retain the socket after broker close. | Ordered byte relay with stream order, half-close, deadlines, cancellation, and bounded close; implemented and qualified. | F12/P12, L13 |
| Linux TCP listener | Reject current profile: accept is denied, while the inherited listener still retains the port. | N accepts and relays bounded accepted streams; implemented and qualified. | L13, S13; F12/P12 has no listener result |
| Linux connected UDP | Reject: send-to overrides the configured peer even when reconnect is denied. | Packet relay with destination validation and packet metadata; implemented and qualified. | F12/P12, L13 |
| Linux unconnected UDP | Reject: R retains send-to authority after N closes its copy. | Packet relay with per-send validation and bounded queues; implemented and qualified. | F12/P12, L13 |
| Windows Wintun | Reject: current API has no supported transferable session/ring contract. | N-owned Wintun with packet relay. Caller-profile Wintun remains unavailable without native launch authority. | W11/W12, F12/P12, S13 |
| Windows TCP connection | Reject version 1 transfer: authority and lifetime are not qualified. | Ordered byte relay; qualify stream semantics and hostile retention/close. | W11/W12, F12/P12, S13 |
| Windows TCP listener | Reject: transfer, accept, and revocation are unqualified. | N accepts; relay accepted streams. Add listener measurements and hostile tests. | W11/W12, S13; F12/P12 has no listener result |
| Windows connected UDP | Reject version 1 transfer: destination override and socket-control scope are unqualified. | Packet relay; qualify per-send policy, packet metadata, deadlines, close, rate, and CPU. | W11/W12, F12/P12, S13 |
| Windows unconnected UDP | Reject: no safe direct grant is qualified. | Packet relay with per-send validation, then functional qualification. | W11/W12, F12/P12, S13 |

Linux uses private Unix stream socketpairs. Windows uses private connected
byte-mode named pipes with overlapped I/O. Both use separate control, data, and
diagnostic transports and the same bounded relay records. Five-tuple matching
uses one bounded control RPC per lookup, without observation leases or an R-side
cache. Shared rings, direct Winsock, direct TUN, and alternate fast paths require
a later reviewed contract version; they are not optional version 1 modes.

## Direct-handle authority review

Linux L13 confirms that `setsockopt`, TCP/UDP reconnect, UDP disconnect with
`AF_UNSPEC`, rebind, listen changes, accept, runtime shutdown, and the tested
TUN query/persistence/queue ioctls return `EPERM`. The syscall filter supplies
these denials; the descriptor is not intrinsically limited to read/write.
Do not remove these restrictions to make a native wrapper work without a new
authority review. Other ioctl commands are denied by the same syscall rule;
the audit does not claim an exhaustive native ioctl test matrix.

The filter permits send-to, descriptor duplication, and file flag changes.
L13 proves a connected UDP socket can send a datagram to a different bound
receiver while `connect` remains denied. Checking only the dial destination is
insufficient. It also proves shared open-file flags can be changed through an
inherited descriptor. Optional policy is enforced in N, never in an R wrapper.
Absence of an optional restriction still permits the normal typed sysnet
operation; rejection of this direct mechanism does not add a mandatory network
allowlist.

N closes its transferred socket, listener, and TUN copies before the hostile
operations start. R duplicates TCP, closes its first copy, and still sends data.
Both UDP forms also send after N's close. The listener still holds its port and
the nonpersistent TUN still exists. R exit releases the listener, produces TCP
EOF, and removes this TUN. L13 tests normal process exit with retained handles,
not forced termination, persistent TUN, multiple queues, broker death, or native
device teardown during traffic. Those are open tests, not implied successes.

A future direct proposal must enumerate socket options, bind/reconnect,
send-to/ancillary controls, multicast, ioctl/driver controls, shared file state,
duplication, and native fast-path bypass. On Windows it must test both transfer
directions, requested handle access rights, inheritance, mapping/event rights,
and the exact AppContainer/provider combination. A Wintun adapter handle is not
a transferable Wintun session. R must never receive split-driver control or
general process/token access. Existing Windows token and job results cannot
substitute for tests on each transferred handle.

## Shared-memory and relay review

Reject the current shared-memory candidates as production channels. They use
one shared slot, not a ring with a producer/consumer ownership contract.

On Linux the socket and TUN experiments create an unsealed read/write memfd.
L13 proves that R can truncate it to zero while N retains a mapping. The audit
checks the new backing size without touching the invalidated pages. Access to
those pages is therefore not a safe production assumption. R can also change
the record length and payload after N validates them. Copying length to a local
variable bounds that one length; it does not stabilize the payload. The existing
socket and TUN loops pass shared payload memory to native I/O. Neither channel
binds records to a live resource generation or graph epoch.

The Linux notification descriptors are shared in both directions. L13 fills
the response event counter; N's nonblocking notification then returns `EAGAIN`.
The timing harness uses blocking event descriptors, so a hostile writer can
prevent progress. Its bounded iteration count does not bound elapsed work or
wakeups. Reject notification counting as a trusted ownership signal.

Windows transfers an actual file mapping and request/response events in W12.
It checks a local copy of length, then passes the shared payload to native I/O.
An event is a wakeup, not proof that R stopped modifying the record. There is no
qualified generation, ownership, or hostile-notification protocol. Linux memfd
truncate evidence does not establish a Windows mapping-resize exploit. Windows
mapping/event attacks remain untested and block approval independently.

Before another shared-memory candidate is eligible, it must:

- Fix backing size and minimal access rights before transfer. Keep native
  handles and broker-private state outside shared memory.
- Validate indices, wraparound, lengths, offsets, counts, flags, resource IDs,
  generations, and epochs with overflow-safe arithmetic and fixed work limits.
- Snapshot metadata once and copy payload to bounded private memory, or prove
  an OS-enforced ownership transition. R's claimed ownership bit is insufficient.
- Treat notifications as hints. Bound wakeups, batches, queues, bytes, and
  accepted connections; reserve independent cancellation and control capacity.
- Test malformed and racing records, stale generations, resize attempts,
  repeated/missing notifications, stalled peers, cancellation, and peer death.
  Corruption must close the affected channel without unsafe memory access or
  loss of unrelated control progress.

The selected relay keeps all native resources in N, where typed operations can
be validated and resources revoked independently of R's cooperation. The
checkpoint 12 benchmark is not the version 1 implementation: it lacks full
framing, ownership, cancellation, metadata, and hostile-peer tests. Its
performance results remain diagnostic. Implement the frozen contract, then qualify it; do not
promote the benchmark loop into production.

## Frozen lifetime contract

For the selected relay, graph removal, resource close, or generation replacement
must first reject new work for the old reference. N cancels queued/in-flight
work, closes or tears down the owned native resource, discards stale records,
and acknowledges only the state it can prove. Packets already submitted to the
OS cannot be recalled. A failed teardown returns an uncertain/recovery result.
TCP half-close remains distinct from resource revocation. UDP close must wake
blocked readers; TUN events must retain order with generation changes.

Restore invalidates the previous graph epoch. Policy expansion needs trusted
restart; a policy reduction must stop or revalidate affected work before it is
reported active. R loss must cause N to close owned resources. N loss must fail
R's adapters and stop the session. Shutdown must bound all waits and keep host
cleanup under trusted ownership. Test these cases with saturated data and logs.

For a direct handle, closing N's copy or receiving a cooperative close reply
is not hostile revocation. The required action must be proven native teardown,
or termination of R and every process that can retain a duplicate. If process
termination is the only mechanism, graph detach and restore require a full
runtime restart. Do not advertise seamless per-resource close under that
contract. No such production termination contract is selected or qualified
here. The non-breakaway job/PID containment is only part of that proof.

## Archived reproduction record

The accepted L13 run on 2026-10-09 is retained at
`artifacts/data-path/security-20261009T104032Z-236290/`. It ran on Linux 7.2.8
amd64 with Docker 29.8.1 and the pinned Debian/Go 1.26.6 experiment image.
The environment file records the source revision, dirty state, and C source
hash. All 17 required observations passed. No host network namespace was used.
An initial build failed on an unused variable; the corrected audit verifies
delivery to the configured UDP peer as well as the alternate destination.

The standalone probe and evidence validator were removed after the relay
decision was implemented. Windows results above remain retained VM evidence;
this cleanup does not claim a new Windows hostile-handle run.

The focused `just check` passed in Docker on 2026-10-09: all four evidence
validator tests, formatting, spelling, secret scanning, shell checks, Linux
lint/vet, and Windows amd64/arm64 cross-platform lint passed. The container used
the mounted development tools, including Go 1.26.8. The first check attempts
stopped on the C API identifier spelling check, workspace file permissions, and
the container's missing npm certificate trust. The final run used the workspace
UID and the trusted certificate bundle without disabling TLS verification.
The retained W11/W12 archive also passed its existing evidence validator again
in Docker. No new Windows VM execution or full-project runtime suite was run;
the change affects development experiments and design documents only.

Checkpoints 14 through 18 implemented the frozen relay contract, and the Linux
profile passed the checkpoint-30 production gate. Repeat the security review
after material changes to restrictions, OS/provider, Go runtime, Wintun, native
backend, IPC framing, memory ownership, or TUN queueing. Windows remains
unreleased; this does not reopen the version 1 design choice.
