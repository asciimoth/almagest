# Socket control and data relays

Checkpoint 16 adds socket ownership to `bin/internal/isolation/network`, a
bounded data lane in `bin/internal/isolation/relay`, and runtime adapters in
`bin/internal/isolation/runtime`. These components are not imported by the
direct daemon. The packaged Linux isolated composition uses them.

The implementation is available for component integration. Checkpoint acceptance
depends on the complete functional method/platform matrix. Performance is
accepted and is not a gate. The tests below are not a complete restricted-role
or complete-daemon qualification.

## Ownership and API

`OpenSocket` uses the checkpoint 15 registry and retained operation receipts.
It validates numeric endpoints, TCP/UDP family, socket purpose, multicast
options, and buffer limits before native work. The selected sysnet `OutNet` or
`LocalNet` creates the object. Backend marks, underlay selection, and platform
LocalNet restrictions remain in N. There is no runtime-supplied mark or raw
socket-control callback.

Sockets and listeners share session, type, generation, graph epoch, and live
resource accounting with TUNs. Optional socket/listener quotas apply separately.
Late canceled creates are compensated. Accepted connections have new references;
N requires a claim before data I/O. Unclaimed offers expire after five seconds,
with limits of 16 per listener and 128 per session. Listener close removes its
unclaimed children and preserves claimed children. Epoch release removes both.

Interface snapshots include the requested network purpose, interface identity,
name, MTU, flags, hardware address, and numeric unicast/multicast addresses.
Runtime selection by name or index uses that snapshot.

`OutDNS` uses an `OpenSocket` request with kind `resolver` and purpose `outbound`.
All address, transport, multicast, and native socket-option fields must be empty.
This creates a session-owned packet channel to the existing native `OutDNS()`
provider, not a host socket selected by R. The same data lane carries complete
DNS packets, up to 65,535 bytes. DNS payloads never enter control JSON. N may
parse DNS as part of the trusted backend; application DNS nodes stay in R.

R uses at most four persistent channels per returned resolver, or fewer when
the protected socket quota is smaller. Each channel
has at most one active query and one pending response. Pending response capacity
is reserved against the existing 8 MiB session data budget before native work.
Native provider allocations remain subject to N's process/cgroup limits.
Queries have a five-second ceiling and honor shorter request deadlines,
cancellation, and channel close. A closed channel cannot publish a late reply
or close the shared native provider. Closing the resolver or runtime client
stops its workers. DNS work uses data-worker slots, not the native mutation
gate, so a stalled query does not stop control operations.

The relay preserves native provider selection, split-name routing, dynamic
upstreams, response flags, and records instead of reconstructing them from a
flat list. It has no arbitrary upstream or public-resolver fallback. The
`dns_no_upstream` completion code preserves the native no-upstream error without
exposing native error text. The Linux sysnet v0.10.1 provider matrix qualifies
all supported Linux provider modes. Windows provider qualification remains a
later platform gate; packet-level parity tests alone do not qualify a provider.

`ResolverSnapshot` and the explicit `SnapshotDNS` adapter remain available for
trusted integrations that supply `SetResolverSnapshotSource`. A missing snapshot
source is unsupported. Normal `OutDNS` no longer requires that source or reads
resolver files in R.

Managed default-TUN DNS uses the reverse direction. Bootstrap creates one
`ApplicationDNSEndpoint` in N and registers its opaque `ResourceDNS` reference.
R's `ServeDNS` adapter runs four bounded application-resolver workers. N queues
at most 64 complete questions, assigns a nonzero correlation value, and accepts
only an exact correlated response with the original DNS ID. Socket and TUN
resources reject this DNS metadata. Resolver errors become `SERVFAIL`; close,
deadline, malformed response, peer loss, and endpoint detachment wake pending
work. The native provider and managed DNS server remain in N, while node
resolver callbacks execute only in R.

The Linux native composition test attaches this endpoint to a default TUN,
queries N's managed DNS listener, receives R's application response, and checks
cleanup. This qualifies the reverse path without moving provider logic or node
callbacks across the trust boundary.

## Data and completion

The separate authenticated data stream uses the frozen version 1 framing:
80 KiB complete frames, 8 KiB strict JSON metadata, 64 KiB payload, and full
session/resource/generation/epoch identity. The parser rejects duplicate or
unknown members, invalid lengths, sequence gaps, and replay. It admits up to
16 records per frame. This implementation grants one record at a time per
resource and direction; a batch beyond that grant is an over-credit error.

A read request grants reserved receive capacity. `prepare_write` reserves one
record in N and returns its credit; the subsequent data frame and `write`
request produce the native completion. IPC receipt is not a successful native
write. Both byte and record reservations are bounded, including empty UDP
packets. Queues allocate on demand. The data reader never performs native I/O;
the FIFO writer serves one ready frame per resource before another frame from
that resource. No batching delay is added.

The session ceiling is 8 MiB per direction. Accounting includes native read
scratch and retained packet batches as well as encoded output. One native read
and one native write can run per resource. Data calls stop at 96 occupied
control-call slots to leave capacity within the existing 128-slot ceiling for
status, deadlines, and cleanup. Transport terminal replies and cancellation
retain their separate reserved queue capacity.

TCP preserves byte order, partial counts, EOF, half-close, and deadlines. UDP
preserves datagram boundaries, zero-length packets, source endpoints, explicit
destinations, and caller-buffer truncation. Linux message APIs translate a
closed ancillary set: IPv4/IPv6 packet information, hop limit/TTL, and traffic
class/TOS. Unknown controls, duplicate controls, and descriptor controls are
rejected. N validates selected source addresses and interfaces before a send.
Windows rejects unsupported raw message controls explicitly. Multicast uses
Gonnect's native supported family and named group/control operations.

R enforces absolute I/O deadlines locally. N receives remaining durations, not
clock timestamps. A local timeout retains a bounded worker and its capacity
until the terminal reply or peer loss. Cancellation after native submission can
be uncertain; writes are never replayed. Socket creation uses the same rule so
a canceled caller cannot lose a late committed socket. Close first rejects new
work, closes the native object, and waits for native I/O to stop. An unproved
cleanup reports recovery-required state.

## TUN adapters

Runtime TUNs return `IsNative() == false` and `File() == nil`. Their advertised
batch size is one. They validate caller offsets and packet sizes and retain
partial write counts. Native batch buffers, native header offsets, and native
handles stay in N. Events carry the existing revision and generation checks;
a newer generation terminates the old adapter instead of reviving it.
Closing that old adapter cannot remove its replacement from the client registry.

Linux integration can select `network.RelayTUNFactory` in the broker's
`sysnet-linux.Config`. It creates a fresh, exclusive TUN without GSO. This avoids
silently overflowing the frozen queue limits with the native 128-packet GSO
batch. Direct mode keeps its existing factory and offload path. The relay must
be compared with that unchanged baseline; disabling offload in the baseline
would invalidate the comparison. Backend batches above 64 records or the
bounded scratch capacity return an explicit capability error.

The Linux factory must attach the device before `os.NewFile` registers it with
Go's poller. MTU reads use the verified live interface identity, because the
native TUN name cache can be stale after a sysnet rename.

## Verification and remaining qualification

Run checks only in Docker or the disposable Windows VM. Linux native tests
require `ALMAGEST_ISOLATION_NATIVE_TEST=1`, `CAP_NET_ADMIN`, `/dev/net/tun`, and a
Docker marker. Use `--network none`; no host network namespace is needed.

Inside that container:

```sh
go test -race -timeout 90s ./bin/internal/isolation/network ./bin/internal/isolation/relay ./bin/internal/isolation/runtime ./bin/internal/isolation/protocol ./bin/internal/isolation/transport
go test ./bin/internal/isolation/relay -run '^$' -fuzz '^FuzzDataFrame$' -fuzztime=5s -parallel=2
go test ./bin/internal/isolation/relay -run '^$' -fuzz '^FuzzDataFrameMetadata$' -fuzztime=5s -parallel=2
go test ./bin/internal/isolation/runtime -run '^$' -fuzz '^FuzzParseEndpoint$' -fuzztime=5s -parallel=2
go test ./bin/internal/isolation/relay -run '^$' -bench '^BenchmarkDataFrame$' -benchmem -benchtime=300ms -count=3
```

The Windows selectors are:

```sh
just test-total winvm:m16::socket-relays winvm:m16::native-tun-relay
```

Portable tests cover IPv4/IPv6 TCP and UDP, wildcard/ephemeral listeners,
stream order, half-close, zero-length datagrams, truncation, pending deadline
changes, close, peer loss, offer revocation, receipt conflicts, stale references,
graph release, DNS over relayed sockets, interface snapshots, control progress
with blocked data readers, malformed frames, and credit limits. Linux tests add
native TUN traffic in both directions, offset handling, typed UDP ancillary
sends, and multicast join/leave plus control metadata on IPv6 delivery. The
multicast test sends to loopback through the multicast API; it does not claim
qualification of multicast routing on external interfaces.

Additional contract tests cover invalid socket purposes, kinds, families,
endpoints, deadlines, buffers, keepalive limits, multicast flags, relay memory
accounting, canceled receives, stale/future resource generations, runtime error
mapping, endpoint parsing, and TUN event/generation transitions. Structured
fuzzing exercises data metadata and numeric endpoint parsing in addition to the
raw frame parser.

A deterministic gated-stream test now holds one resource in the writer, queues
a second resource, and then immediately submits more work for the first. It
proves that the global FIFO sends the queued peer before later work from the
hot resource. The test passed 100 consecutive runs with the race detector.
This closes the component-level send-fairness assertion.

The portable broker/client tests use separate in-process stream endpoints.
Checkpoint 14 covers private endpoint authentication; final cross-process
restrictions, all supported multicast/native option combinations, complete
userspace-stack parity, and functional fairness still need the platform
qualification runs. One-record credits and JSON control costs are accepted as
the current baseline and can be optimized later.

On Linux amd64, Go 1.26.6, AMD Ryzen 5 5600H, five-second fuzz runs completed
435,319 raw-frame executions, 358,438 structured-metadata executions, and
357,051 endpoint-parser executions. Three 1,200-byte encode/decode
microbenchmarks measured 20.7–21.3 us/op, 56.4–58.0 MB/s, about 9,040 B/op, and
210 allocations per operation. These are codec measurements in Docker, not
native traffic
throughput, cross-process latency, or a comparison with either direct baseline.
No performance acceptance condition applies.

Verified on 2026-10-09:

- Docker Linux: the five isolation packages above pass with the race detector,
  including native TUN, UDP metadata, and multicast component tests. The
  cancellation/accept tests also pass 20 consecutive race-test runs.
- Docker: focused `just check` runs the repository lint/static checks, including
  Windows amd64/arm64 checks, and the selected relay tests.
- Windows VM: ordinary socket/DNS/interface tests and the elevated native TUN
  relay test pass. The native run is retained in
  `artifacts/winvm/run-xkuGbcML`. Windows can report an interface MTU of `-1`;
  the signed snapshot field preserves that value. Native TUN test addressing
  uses a `/32` address plus an explicit route to avoid a duplicate connected
  route. The expanded ordinary suite, including socket validation, relay
  accounting, runtime TUN events, pending accept deadlines, and stale/future
  data generations, also passes (`artifacts/winvm/run-I46ZeKat`).

Local logs under `artifacts/isolation-m16` retain the final Docker checks and
the Windows socket rerun. These generated artifacts are not tracked in Git.
