# Daemon isolation data-channel contract

Status: frozen version 1 design for checkpoints 14 through 18. The
[checkpoint 13 decision](DAEMON_ISOLATION_DATA_PATH_DECISION.md) records the
security evidence and the maintainer's decision to proceed with conservative
relays. This file specifies the implementation
target. The [checkpoint 16 component implementation](DAEMON_ISOLATION_SOCKET_DATA.md)
records its checks and remaining qualification; this is not a qualified
production transport.

## Selected mechanisms

N owns every native socket, listener, TUN/Wintun session, and driver handle.
R receives opaque resource references and bytes or packets through broker
relays. Linux and Windows use the same resource semantics:

| Resource | Version 1 mechanism |
| --- | --- |
| TUN, including default TUN | Packet relay; N performs native reads, writes, configuration, and teardown. |
| TCP connection, including accepted TCP | Ordered byte relay with independent read/write halves. |
| TCP listener | N accepts and offers bounded child connection references; each claimed child uses the TCP relay. |
| Connected UDP | Packet relay; N applies the native connected-socket semantics and checks any explicit destination against protected policy on every send. |
| Unconnected/multicast UDP | Packet relay; N checks each destination and typed ancillary field on every send. |
| Application DNS endpoint | Correlated packet relay; N sends validated DNS questions to R and accepts only the matching DNS response. |
| Five-tuple matcher | One bounded control RPC per lookup; no observation token, lease, client cache, or per-packet routing RPC. |

There is no direct native handle, shared mapping, ring, generic socket option,
native pointer, driver request, or raw ancillary-control blob in this version.
Userspace-only resources remain local to R. Relayed resources report non-native
status and expose no file or `RawConn` escape. Direct mode retains its native
objects and fast paths without these transports.

Linux uses private `AF_UNIX/SOCK_STREAM` socketpairs. Windows uses private
connected byte-mode named pipes with overlapped I/O. Trusted bootstrap prepares
the exact endpoints, restricts inheritance/access, and binds them to the session.
R receives only its IPC endpoints. Authenticate the final restricted role as
specified by checkpoint 14; a pipe name or initial Linux peer credential alone
is insufficient. Reject and close unexpected ancillary handles.

Use one duplex data transport per R/N session, separate from duplex control
and diagnostic transports. Each transport has its own bounded reader/writer
work. A data or diagnostic writer must not hold a control lock. Multiplex data
by resource with per-resource queues and credits; never block the data reader
on native I/O for one resource. Additional lanes or shared-memory acceleration
are not required to implement version 1.

## Framing, identity, and limits

Negotiate data version 1 and limits on the authenticated control channel before
any data record. Unsupported versions fail closed. The bootstrap session ID,
session epoch, policy digest, resource type, graph epoch, and resource generation
use the existing control contract. N binds each live data channel to one exact
resource reference. Neither a guessed ID nor possession of an IPC endpoint
creates a resource. IDs are not reused within a session.

Each data frame has a four-byte unsigned big-endian body length, a four-byte
unsigned big-endian metadata length, UTF-8 JSON metadata, and concatenated raw
payload bytes. Body length excludes its own four-byte prefix and includes the
metadata-length word. No base64, compression, native structures, or native
pointers are used. Decode metadata with the checkpoint 14 strict JSON rules:
reject duplicate/unknown members, excess depth, invalid numbers, and trailing
data. Validate lengths before allocation and before reading variable bodies.

Metadata has exactly these members:

- `version`: 1; `session`: 32 lowercase hex characters; `sessionEpoch`:
  nonzero uint64; `resource`: 32 lowercase hex characters; `generation` and
  `graphEpoch`: nonzero uint64.
- `sequence`: nonzero uint64, starting at 1 for each resource and direction,
  increasing by one per frame with no wrap, replay, or gap.
- `records`: a nonempty array. Each record contains `request`, a nonzero uint64
  operation identifier; `length`, a uint32 payload length; and `packet`, either
  null for TCP or the typed packet metadata object below. Payload slices appear
  in record order; their lengths must sum exactly to the remaining frame bytes.

Request IDs increase monotonically per channel and direction, without reuse or
wrap during the session.
They correlate write completions or read deliveries and cancellation. TCP writes
larger than one record are split into separate bounded operations by R. There
is no transport retry after an ambiguous native write. The receiver validates
the complete frame into private memory before dispatch; shared mutable storage
never backs an approved native operation.

| Limit | Version 1 ceiling |
| --- | ---: |
| Complete frame, including both length words | 80 KiB |
| JSON metadata | 8 KiB, depth 32 |
| Combined raw payload in one frame | 64 KiB |
| Records in one frame | 16 |
| TCP record | 32 KiB |
| UDP datagram or TUN packet | 65,535 bytes; also limited by native support and TUN MTU |
| Queued data per resource and direction | 256 KiB and 64 records |
| Queued data per session and direction | 8 MiB and 4,096 records |
| Native read/write operations in flight | One per resource per direction |
| Offered but unclaimed accepted connections | 16 per listener, 128 per session |
| Unclaimed accepted connection lifetime | 5 seconds |
| Time to finish a started frame | 5 seconds from its first byte |
| Native close/cancel cleanup wait | 5 seconds; then fail with recovery-required state |

Queue byte accounting includes stored metadata and payload. In-flight data,
reserved read capacity, and transport buffers count against session limits.
Check both byte and record limits, including zero-length datagrams. Allocate
capacity on demand within the session ceiling, not the per-resource maximum for
every resource. The existing 4,096 live-resource ceiling includes accepted
children. Policy may lower ceilings but cannot raise them. Negotiation must
reject limits too small for the configured MTU or one legal record rather than
silently truncate packets. Native maximums above this version's packet ceiling
are an explicit isolation capability limit that requires qualification.

Send the first ready record immediately. A worker may include up to 16 records
already ready for the same resource within the payload limit. Do not wait for a
batch to fill. Schedule at most one frame per ready resource before visiting
the next ready resource. This preserves low-rate traffic while permitting
bounded batching for bulk traffic. Use normal per-resource packet order in
version 1; multi-queue native APIs stay inside N and do not expose queue handles.

## Flow control and typed channel controls

The control transport carries typed channel messages for read requests, credit,
write completion, cancel, deadline update, half-close, accept offer/claim/reject,
TUN state events, and terminal errors. These messages are data-contract version
1 messages, separate from generic network-control operations. Every message
carries the session and full resource reference; operation messages also carry
their request ID. Bounds and the existing 64 KiB control frame limit apply.
There is no free-form method name or native request passthrough.

Credits grant bytes and record slots, never authority. Start with zero credit.
A receiver grants only capacity it has reserved within both queue ceilings.
Deduct credit before enqueueing a frame; replenish only after capacity is
released. Unused credit is revoked when the resource generation ends. Duplicate,
overflowing, or excessive credit and any over-credit send are protocol errors.
Keep control-message queues bounded by live operations/resources; coalesce
unsent credit and deadline updates. Use the existing 128 concurrent-request
limit for pending calls. Cancel, close, completion, and terminal notifications
have reserved control capacity and must work when new-call capacity is full.
Reserve 128 queued control messages for these terminal operations, separate
from the 128 ordinary pending calls. If a stalled peer exhausts this reserve,
fail the session and clean up locally; do not wait for R to consume an error.

Read requests specify a bounded buffer size and deadline. A TCP delivery is
correlated to one read request. UDP/TUN reads use a full native packet buffer;
the adapter then applies the caller's buffer/truncation rules. A write completion
reports the native accepted byte/packet count and a typed error. Receipt of IPC
bytes is not native write completion. A partial TCP write reports its count;
unsent bytes are not replayed automatically. Each datagram/TUN packet has its
own result within a batch. Preserve native errors rather than reporting success
for discarded data.

Deadlines use a remaining duration in nanoseconds, with zero meaning already
expired and null meaning no deadline. R computes remaining time immediately
before dispatch and also enforces the caller's absolute deadline locally. N
starts its timer on receipt; the two processes do not compare clock epochs.
Cancellation can race a native write: report the known count or an uncertain
completion, never a guarantee that no packet was sent. Late results release
capacity but cannot satisfy a new operation with a different ID. Channel loss
fails all pending calls; there is no reconnect-and-replay or native fallback.

TCP preserves byte order, EOF, read/write deadlines, partial writes, and
independent close-read/close-write. Process half-close after prior writes finish
or fail; remote EOF follows already delivered bytes. Full close discards queued
work and wakes both halves. EOF and failure are distinct typed results.

Packet metadata contains exactly `source` and `destination` (numeric address
and port, or null where inapplicable), `interfaceIndex` (uint32 or null),
`hopLimit` and `trafficClass` (0..255 or null), `originalLength` (uint32),
`truncated` (boolean), and `dns` (null or a nonzero uint64 correlation object).
The DNS member is valid only for an owned `ResourceDNS`; socket and TUN paths
reject it. Numeric addresses include a scope identifier where the native IPv6
API requires one. Unknown fields and raw native cmsg bytes are rejected. For
outbound records, original length equals payload length and
truncated is false; N validates source/interface and destination authority.
For TUN, endpoint/interface/hop fields are null: the payload is one complete
IP packet, without parsing protocol bodies in N. Outbound TUN truncation is
invalid. Zero-length UDP is valid; zero-length TUN is invalid.

An application-DNS question and response each contain one complete DNS packet.
R must echo the opaque correlation value. N also requires a response message
with the original DNS ID. At most 64 questions are pending. Invalid, stale,
spoofed, or mismatched responses fail the request and never reach the native
DNS server. Application resolver errors become bounded DNS `SERVFAIL` replies.

N translates supported ancillary fields to/from native API values and preserves
sender/destination, packet boundaries, truncation, and partial-batch outcomes.
If an existing native feature needs metadata not representable here, report a
capability gap and review an explicit contract extension; never forward a raw
native control blob or silently drop required semantics. Optional destination
policy applies to each packet, including explicit sends through a connected
socket. Without that optional policy, preserve valid native send behavior.

## Accept, revocation, and failure

N offers each accepted connection with a new owned socket reference and numeric
local/remote endpoints. R claims or rejects it by reference. N starts no data
delivery before claim and credit. Expiry, limit exhaustion, listener removal,
or R loss closes unclaimed children. Closing a listener does not close already
claimed children; graph-epoch release closes every child in that epoch. Claims
racing close/expiry fail and cannot revive a closed child.
Claimed resources have no automatic idle expiry; quotas, explicit close, graph
lifecycle, or peer loss control their lifetime.

N serializes resource lifecycle changes. On close, graph removal, policy
restriction, generation replacement, or restore, first reject new work for the
old reference, revoke credits, cancel queues/native operations, and tear down
the owned native resource. Acknowledge close only after native I/O has stopped
and the teardown result is known. Datagrams/bytes already submitted to the OS
cannot be recalled. An unproved host teardown returns recovery-required state.
R acknowledgement is not required to revoke native access because R has no
native copy. TUN up/down/MTU/close events carry a monotonically increasing
state revision and resource generation; stale events cannot revive a channel.

A syntactically valid stale resource record is discarded with a stale-reference
error. Invalid framing, identity, sequence, lengths, metadata, or credit is a
session protocol error: stop the session and clean up its owned resources.
Do not delete foreign host resources. On R loss, N closes all owned channels.
On N loss, R fails all adapters and stops the session. Neither path attempts a
native fallback. Bound cleanup waits; report uncertain host state and retain
recovery records when native teardown cannot be proven.

## Implementation and release gates

Checkpoint 14 implements and fuzzes framing, peer binding, separate transports,
and bounded control progress. Checkpoints 15/16 implement registry ownership,
packet/stream relays, accept, credits, deadlines, and cancellation. Checkpoint 18
implements matcher RPCs. Tests must cover both directions, native metadata,
half-close, stale state, saturation, fairness, malicious framing, and role death
before these pieces enter the production composition.

Checkpoint 12 measurements are diagnostic baselines. Checkpoint 30 qualifies
the complete OS profiles against functional and hostile-runtime requirements
before the checkpoint 32 default switch. Checkpoint 31 records the accepted
current performance state. Later optimizations must preserve version 1
semantics or receive a reviewed version change with fresh security evidence.
