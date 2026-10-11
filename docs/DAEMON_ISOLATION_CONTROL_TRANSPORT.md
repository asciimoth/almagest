# Bounded isolation control transport

Checkpoint 14 is implemented in `bin/internal/isolation/transport`. The Linux
production profile uses it for its private N/R/C/H lanes. The direct daemon
does not import this package or create its channels.

## Wire format and limits

Each control record has a four-byte big-endian body length followed by strict
UTF-8 JSON. The complete frame, including the length word, is at most 64 KiB.
A started record and each write have a
five-second deadline. An idle session can wait for work without an idle timeout.
Negotiation itself has a five-second deadline.

Both endpoints first exchange a version 1 hello containing the trusted session,
epoch, policy digest, and compiled limits. Every value must agree. There is no
version downgrade. The peer cannot select its role or policy. Production
bootstrap must authenticate and restrict the child before calling `New`.

The decoder rejects duplicate and unknown members, incorrectly cased member
names, invalid UTF-8, unpaired Unicode surrogates, trailing JSON, more than 32
container levels, and arrays or objects with more than 4,096 entries. Typed
integer fields reject fractions, negative unsigned values, and overflow.
Fixed-size identifier and digest arrays require their exact length.
Request bodies use a closed table of the existing protocol request types.
Owners must still check operation-specific values, policy, resource ownership,
and generations before effects.

Request IDs start at one and increase without reuse or wrap in each direction.
The transport fills the request metadata from its trusted session and assigned
ID. Incoming metadata must match. Unknown operations, invalid identities,
unexpected replies, and invalid message kinds stop the session. Errors use a
closed code table that preserves `errors.Is`; raw native error text is not sent.
No request or mutation is automatically replayed.

There are at most 128 pending calls and 128 handler workers per direction.
Cancellation returns promptly to the caller and cancels the owner's context.
A canceled call keeps its slot until its terminal reply. A canceled handler
keeps its slot until it returns. Thus a handler that ignores cancellation cannot
cause unbounded worker creation. Cancellation does not prove that a mutation
had no effect; owners still need the operation receipts from later checkpoints.

The writer has 128 ordinary queue slots and 128 reserved terminal slots. A
single FIFO preserves request-before-cancel order. Queue entries, including the
one being written, retain their slot until the write finishes. Terminal overflow
or a stalled writer fails the session and closes all three lanes. Event delivery
has 128 slots and does not block the reader; a full receive queue fails the
session. These are bounded failure rules, not a claim that a stalled control
peer will process cancellation.

## Config transfer

`SendConfig` sends ordered, individually acknowledged chunks. Each carries a
transfer ID, offset, total length, final flag, and opaque bytes. The payload is
32 KiB, below the 64 KiB chunk ceiling, so base64 and the envelope fit inside one
control frame. The complete document is at most 8 MiB.

Only one transfer runs per direction. Transfer IDs increase without reuse.
The receiver rejects gaps, duplicates, changes in total length, premature final
chunks, empty intermediate chunks, and overflow. Assembly has a fixed 30-second
deadline and an allocation no larger than the declared total. The receiver
holds at most one complete unconsumed document and rejects another transfer
until the owner consumes it. Session loss clears incomplete assembly.

Chunk acknowledgement means receipt only. It is not a file write or commit.
Checkpoint 19 must bind a received transfer to the config writer's typed
revision and operation-ID transaction. Config parsing and persistence are not
part of this transport.

## Private endpoints

Control, data, and diagnostics have distinct duplex streams. They do not share
writer locks, buffers, or queues. Data and diagnostic consumers belong to
checkpoints 16 and 21. The tests stop both consumers while control calls proceed.

On Linux, bootstrap creates unnamed `AF_UNIX/SOCK_STREAM` socketpairs with
close-on-exec and `SO_PASSCRED`. It passes only each child's exact endpoints and
closes unused copies. The receiver validates kernel message credentials on
every read against the expected final PID, UID, and GID. The PID is in the
receiver's namespace. An ancestor broker appears as PID zero inside the child's
PID namespace. That mapping requires the explicit bootstrap-only
`OutsidePIDNamespace` flag; UID/GID and exact endpoint ownership still apply.
The broker's check of the child uses its nonzero PID in the broker namespace.
Creation-time `SO_PEERCRED` is not used to prove the
child's final identity. Production bootstrap must verify the restrictions and
retain the child process identity. Unexpected rights are closed, and missing,
duplicate, malformed, or truncated ancillary credentials fail the stream.
Go's Unix receive path installs received descriptors with close-on-exec.

On Windows, bootstrap creates a first-instance, single-instance, local-only
byte pipe with overlapped I/O and a protected DACL for its own SID. It connects
both endpoints before launch and verifies both connection process IDs against
bootstrap. Client handle rights omit creation of another pipe instance. Handles
start non-inheritable. The launcher must use an exact inherited handle list,
verify the suspended child using its retained process handle, apply restrictions,
close unused copies, and then resume the child. The runtime never connects by
name. Creation-time pipe process IDs identify bootstrap, not the final child.
The existing AppContainer experiment exercises this inheritance path in both
caller and LocalSystem service profiles.

## Verification

Run Linux checks in Docker and Windows checks in the disposable VM. No test
needs host route, firewall, DNS, or TUN changes.

The package tests cover framing, strict decoding, concurrent request limits,
typed errors, cancellation, peer death, event overflow, blocked data/diagnostic
lanes, maximum-size config transfer, replay, and config expiry. Linux tests also
cover descriptor cleanup, message credentials, final child credentials,
fragmented stream reads, and absence of a listener. Windows tests cover actual
connected pipes and read/write deadlines.

The fuzz targets are `FuzzControlDecode`, `FuzzControlFrame`,
`FuzzControlIdentity`, `FuzzConfigAssembly`, and Linux `FuzzAncillary`.
Ancillary fuzzing uses an injected descriptor closer so arbitrary fuzz bytes
cannot close real process descriptors. Separate tests check actual cleanup.

The Windows package selector is
`just test-total winvm:m14::private-control-transport`.
`just windows-isolation-experiment` checks the connected pipe in the restricted
caller and service AppContainers. Only the tested Windows Server 2022 amd64
profile is qualified by those runs. Other OS profiles and complete-daemon
security remain subject to checkpoints 24, 25, and 30.

### Checkpoint 14 results, 2026-10-09

- Docker package tests passed with the race detector on Go 1.26.6. The root
  container also passed the final-child test after the child changed to UID/GID
  65534. The ordinary-user container passed the same test with a separate child
  PID and unchanged user identity. `TestLinuxAncestorCredentials` also passed
  in a network-disabled Docker container with `CAP_SYS_ADMIN` and seccomp
  unconfined for PID namespace creation. It verified the broker's zero-PID
  mapping inside the child namespace. That test skips when namespace creation
  is denied, so the explicit Docker run is required for this evidence.
- All five fuzz targets passed five-second smoke campaigns with two workers.
  The strict decoder campaign was repeated after the fixed-array checks. These
  short runs do not replace the sustained hostile-runtime qualification gate.
- Focused `just check` passed in Docker with Go 1.26.8. It included normal
  formatting, spelling, secret scanning, lint, vet, Windows amd64/arm64 static
  checks, and race-enabled selected tests. Windows PowerShell lint was also
  rerun successfully with its module path supplied to the container.
- The Windows package suite passed on Windows Server 2022 amd64 in
  `artifacts/winvm/run-D4c1F0eU`. It includes the separate-process attempt to
  connect to the already connected pipe, with its exact name known.
- Both restricted AppContainer profiles passed in
  `artifacts/winvm/run-kmnRvapz`. The child read a challenge from bootstrap and
  returned its result on the inherited duplex pipe. The sentinel handle stayed
  absent. Neither profile used elevation or a new external service.
- The direct daemon's dependency list contains no isolation transport package.
  No host network settings were changed by these checks.
