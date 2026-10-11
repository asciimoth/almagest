# Network resource ownership and TUN control

Checkpoint 15 adds `bin/internal/isolation/network`. The Linux production
profile uses it with a durable recovery journal. The direct daemon does not
import it.
Checkpoint 16 adds [socket control and packet/stream relays](DAEMON_ISOLATION_SOCKET_DATA.md).

The broker owns one bootstrap session and one native operation lane. The
launcher supplies immutable policy and an exclusively owned backend.
`Serve` connects the checkpoint 14 transport, forwards bounded state events,
and calls `Shutdown` on peer loss. There is no native fallback in R.

## Ownership and lifetime

Every TUN has a random resource ID, session, type, generation, and graph epoch.
The registry validates these fields before native access. Closed IDs remain
tombstones; they are never adopted as new resources. Successful updates return
`CurrentResource` with the next generation. Close is idempotent for the current
reference. A graph release advances the epoch and closes the old resources.
Ordinary config changes do not advance the epoch.

Native calls are serialized, including default replacement and graph release.
Preflight validates syntax, protected restrictions, backend options, and name
collisions before it removes the old default. Equivalent options preserve its
reference and native object. Prefix sets are sorted and deduplicated for this
comparison. Changed defaults receive fresh IDs.

The sysnet adapter retains a native identity guard as well as the original
backend object. Linux checks TUNGETIFF on the retained file and the interface
index. Windows checks LUID and adapter GUID; sysnet-windows also checks native
identity inside configuration and cleanup. No request can supply these native
identities. Linux state reads use the verified interface index because the
current tuntap wrapper caches its original name after a sysnet rename.

The adapter compares name, MTU, addresses, and routes with its last observed
state before changes and cleanup. An external change or identity mismatch
requires recovery; it does not authorize deletion of a replacement interface.
Native implementations must also enforce exclusive creation and identity-safe
updates. This component does not replace the backend's route, firewall, and DNS
ownership rules or claim an atomic transaction with external administrators.

## Results and limits

Every accepted mutation gets a receipt before native work starts. Request IDs
do not affect its fingerprint. Repeating an operation ID with the same input
returns its receipt; different input returns a conflict. `GetResult` recovers
a lost reply, including the resource reference and observed state.

Mutation replies retain `rejected`, `committed`, `compensated`, or `uncertain`
and a closed error code. Native error text is not sent. Read failures use the
transport's typed errors. Cancellation is not proof that no native effect
occurred. A late create is closed before it can be published. A partial create
with a known owned object is compensated when cleanup is proved. A backend
error with unknown effects remains in resource accounting and requires recovery.

There is at most one native worker, including a call that ignores cancellation.
Calls and cleanup waits have a five-second bound. Status and receipt queries
do not wait for that worker. A timed-out or unproved mutation latches recovery
state; an unrelated success cannot clear it. Inspection and session restart
are required to clear that latch. Shutdown revokes the session before it waits.
If native work does not stop, its resources remain recorded and cleanup reports
recovery rather than success.

Policy limits cap live resources and receipts at the existing compiled ceilings
(4,096 and 8,192). Receipts last for the session and are not evicted. When the
receipt budget is exhausted, new mutations fail before effects; trusted shutdown
can still clean up. A new session is required to renew that budget. This avoids
replaying an old operation after its receipt was forgotten. Closed resources
release their native objects but retain bounded identity tombstones.

State and capability replies have an encoded size bound below the control-frame
ceiling. Warnings retain the native identifiers and existing per-warning bounds.
Events carry the resource generation, monotonic state revision, up/down state,
MTU-change flag, and terminal close state. An object being present does not imply
that it is up. A full 128-entry event queue revokes the session and starts cleanup.
Canceled watchers cannot publish an old source as the current TUN.

The typed contract now includes default rule mode (`full`, `include`, or
`exclude`), DNS address selection, exact native capability operation names, and
per-TUN capability revisions. No protected-config schema changes are needed.
Socket and data-channel dispatch are described in the checkpoint 16 record.
Matcher dispatch is implemented by checkpoint 18; see
[five-tuple matchers](DAEMON_ISOLATION_MATCHERS.md). Host-effect dispatch is
described in [host control](DAEMON_ISOLATION_HOST_CONTROL.md).

## Verification

Use Docker for Linux tests and the disposable VM for Windows tests. The native
Linux test requires `ALMAGEST_ISOLATION_NATIVE_TEST=1`, a Docker marker,
`CAP_NET_ADMIN`, and `/dev/net/tun`. Run it with `--network none`; do not use the
host network namespace. For example, with the repository mounted at `/src`:

```sh
go test -race ./bin/internal/isolation/network ./bin/internal/isolation/protocol ./bin/internal/isolation/transport
go test ./bin/internal/isolation/network -run '^$' -fuzz '^FuzzTunValidation$' -fuzztime=5s -parallel=2
```

The package tests cover forged sessions and IDs, wrong types, stale generations,
restart identity, concurrent default replacement, preflight preservation,
equivalent updates, partial creates, late canceled completion, cleanup failure,
epoch release, receipt conflicts and limits, wire receipts, peer loss, and event
overflow. Native Linux tests also check name collisions, MTU updates, readback,
external changes, and ownership-safe cleanup.

`TestLinuxDefaultTunControl` uses `ALMAGEST_ISOLATION_DEFAULT_TEST=1` and
builds a synthetic underlay inside that same container. Its fixture also needs
`CAP_SYS_ADMIN` and an unconfined Docker seccomp profile for the existing
sysnet-linux route-monitor namespace setup. It uses the container's own direct
DNS provider. This is component evidence, not a complete production profile.
Under the revised design, N may retain existing launch capabilities required
by its backend, including `CAP_SYS_ADMIN` where needed. None reach R. The v0.10.1
current-namespace monitor remains useful where that capability is absent.

The Windows selectors are:

```sh
just test-total winvm:m15::network-resource-ownership winvm:m15::native-tun-control
```

The native Windows case requires the elevated `ALM-WINVM` guest. It checks a
foreign-name collision, regular TUN create/update/close, and default-TUN reuse
and replacement. Packet relay behavior and complete restricted profiles remain
the qualification work of checkpoints 16, 24, 25, 30, and 31.

## Checkpoint results, 2026-10-09

- Focused `just check` passed in Docker with Go 1.26.8. It ran the ownership,
  wire-control, bounds, sysnet contract, and control-cancellation selectors with
  the race detector, plus normal formatting, spelling, secret checks, lint,
  vet, and Windows amd64/arm64 static checks.
- Native Linux regular and default TUN tests passed in disposable network
  namespaces with Go 1.26.6 and the race detector. The final combined Docker
  run also passed the complete network, protocol, and transport package suites.
- A five-second, two-worker `FuzzTunValidation` smoke run passed with 52,171
  executions. Twenty repeated runs of the portable ownership, wire-control,
  bounds, and sysnet-contract tests also passed with the race detector.
- The ordinary and privileged Windows VM selectors passed in
  `artifacts/winvm/run-HsUuanhM` on Windows Server 2022 amd64.
- These are focused checkpoint checks, not a new full-project or complete
  isolated-daemon qualification run. No host network settings were changed.


## Address allocator integration

N dispatches typed IP and subnet reservations to the native system allocator.
An exact reservation and an allocation each own one native reserve count.
The result contains an opaque resource reference, its graph epoch, and the
observed address or subnet. Allocation requests select IPv4 or IPv6; subnet
allocation also selects a prefix length. Native pool exhaustion returns a
normal empty allocation through the runtime adapter.

R cannot call the native allocator's `FreeAll` methods. Its `FreeAllIP` and
`FreeAllSubnets` methods release only references held by that adapter. Each
adapter belongs to one graph epoch. A stale adapter cannot reserve addresses
or release a replacement graph's reservations. Release, epoch change, peer
loss, and shutdown free the exact owned counts. Receipt replay cannot add an
extra count. Canceled or malformed results are compensated; unproved cleanup
blocks new work. This in-memory allocator state needs no durable host journal.

The native allocator interfaces do not return errors from explicit reserve or
free calls. The runtime adapter exposes `Err` and stops new allocations after
a failed required reservation or an uncertain release. A rejected optional
allocation for one family does not disable another family. A malformed
successful reply closes the runtime session so N can revoke all its resources.

Tests cover reference counts, foreign reservations, receipt replay and
conflicts, forged identity, stale graphs, cancellation, partial effects,
cleanup failure, pool exhaustion, bounds, peer loss, malformed replies, and a
blocked backend with responsive control reads. Native Linux and Windows tests
compare the same allocator backend through direct and wire calls. Fuzz targets
cover typed request/result validation and native prefix conversion.

Use `TestLinuxAllocationParity` inside Docker; it needs no network capability.
Use `just test-total winvm:m30::allocator-parity` for native Windows execution.
These component checks do not establish complete-daemon qualification.

## Runtime system integration

The runtime client implements the complete `sysnet.System` interface used by
`almagest.BuildEngine`. Capability reports retain the native system revision,
operation keys, rule profiles, routing profiles, ownership fields, states,
reasons, and limitations. The broker adds only its internal resolver-snapshot
and application-DNS operations; the runtime omits those internal entries from
the public native report. Per-TUN state includes both system and instance
capability revisions.

One public runtime TUN object remains stable while N advances its generation
after an update or replaces the native default TUN. Reads, writes, events,
state reads, and later updates always select the current generation. Mutations
are serialized per device. This also makes additive address and route updates
atomic with respect to other runtime updates. A closed or foreign adapter is
reported as an unknown TUN. `File` always returns nil, `IsNative` is false, and
the runtime receives no platform handle.

`SetDNS` creates a typed, graph-owned application-DNS endpoint and serves the
node's resolver on the bounded data lane. The create request carries no host
selector. A successful default-TUN generation change installs the new endpoint
before the old endpoint closes. Detach, replacement, peer loss, graph release,
and system close release it. `OutDNS` remains the native provider in N and is
opened lazily for each caller, so closing one node's provider does not affect
later nodes.

Portable tests cover capability conversion, stable generations, every TUN
setter and getter, concurrent additive updates, replacement, application DNS,
receipt replay, foreign sessions, malformed values, cleanup, and construction
of a real engine. The system-option fuzz target exercises regular/default TUN
parsing and the check/build paths. Native Linux and Windows tests compare the
wire report with the direct backend report. The trusted Linux composition runs
only in Docker with no host network and checks final TUN, DNS, and recovery
journal cleanup. Use the locked Windows VM selector
`just test-total winvm:m30::runtime-system-parity` for Windows parity.
