# Isolated snapshots and live restore

Checkpoints 28 and 29 are implemented in the runtime-owned `almagestd`
snapshot coordinator. The Linux isolated runtime uses the same coordinator and
graph-epoch contract as direct mode. Direct mode keeps the same snapshot API
and archive version.

## Ownership boundary

R parses ZIP, HuJSON, and bbolt content. R creates archives, stages restored
files, validates database buckets, and restores startup storage before it opens
the live bbolt database or builds the graph. Bootstrap opens only the exact
operator-selected startup source and limits it to 32 MiB. N and C do not import
archive, config, database, or snapshot coordinator code. Existing dependency
tests enforce this boundary for the privileged role commands.

Snapshot config is regular application config. Creation removes all top-level
`Protected` members. Import and restore reject a `Protected` member instead of
silently promoting or removing it. Config source files and the snapshots
subtree are not snapshot authority. A daemon-generated TLS CA remains compatible
mutable application state. A protected TLS CA is reapplied from immutable
bootstrap input and cannot be replaced by an archive. Broker policy, resource
IDs, sessions, graph epochs, sandbox identities, command profiles, and the
broker recovery journal stay outside R's storage layout. Restored application
bytes cannot authorize an old broker reference because N validates its current
session, graph epoch, resource type, ID, and generation.

## Consistent creation

All live bbolt updates enter one storage-writer barrier. Snapshot creation first
holds autosave serialization, then waits for active writers and prevents new
writers from starting. It captures the canonical regular config, uses bbolt's
read-transaction copy, walks the permitted file-storage paths, syncs the ZIP,
and then resumes writers. Waiting is cancellation-aware. A queued snapshot also
prevents an unlimited stream of new writers from starving it.

The bbolt transaction remains the stable-generation mechanism for node-owned
KV, DNS, secret, and TLS state. Other mutable daemon files are controlled admin
writes. A source check fails if a future bbolt update bypasses the writer
barrier.

## Archive safety bounds

Archive version 1 remains compatible. The validator applies these documented
bounds to creation, list/export inspection, import, live restore, and startup
restore where applicable:

- 32 MiB compressed input;
- 256 MiB expanded file storage;
- 64 KiB manifest;
- 8 MiB regular config;
- 256 UTF-8 bytes for the display name;
- 4,096 ZIP entries;
- 4,096 bytes for one ZIP entry name;
- 255 bytes for one path component;
- 32 path components;
- 1 MiB total entry-name processing work.

The expanded-byte limit also bounds decompression and staging writes. Extraction
uses the declared entry size as a hard copy limit and rejects a stream whose
actual size differs from its ZIP header. The validator continues to reject
traversal, absolute or backslash paths, duplicate and case-aliased entries,
Windows device names and alternate streams, unsafe symlinks, unsupported file
types, unknown top-level entries, malformed databases, and excluded restore
paths. Entry count and total path work bound zero-length and path-heavy ZIP
attacks that do not reach the byte limit.

## Live restore transaction

Live restore validates and stages the desired archive before it changes the
graph. It then holds the admin mutation gate and autosave lock, waits for active
storage writers, and writes a durable recovery archive below the excluded
snapshot directory. The bounded application journal contains only its version,
the desired snapshot UUID, recovery and config hashes, and one of these states:

- `prepared`: the recovery archive is durable and no commit exists;
- `data-applied`: restored application storage is durable, but the graph is not
  committed; or
- `committed`: restored storage and graph are authoritative and rollback is no
  longer permitted.

The journal contains no broker session, resource ID, native identity, graph
epoch, path, or protected policy. It cannot authorize a host effect. Its fixed
paths are outside the storage subtree copied into snapshots. Files are private,
synced, atomically replaced, size-bounded, schema-checked, and checksum-checked.

The coordinator stops the old graph before it replaces storage. In isolated
operation it asks N to release the old graph epoch and waits for a committed
drain result. The runtime client uses a settled operation, so cancellation or a
lost reply is not accepted as proof of cleanup. Direct operation uses the same
local epoch boundary without an N call. A failed new graph is stopped and
drained, then recovery data and the prior graph are installed under another
fresh epoch. Rollback uses its own bounded context and continues after the
request context is canceled.

The storage-writer barrier remains held through file and bbolt replacement.
The coordinator admits new writers only when graph reconstruction starts,
because node construction can write storage. On rollback, it stops the failed
graph and obtains a new exclusive storage barrier before it replaces data.

One engine notification transaction covers restore and rollback. Its final
workspace and worker snapshots are published asynchronously, so a blocked
subscriber cannot hold the restore locks. Storage reset notifications are sent
only after the committed journal state is durable.

If rollback cannot be proved, the daemon keeps the recovery files, reports the
failure, and latches recovery-required state. Later graph mutations are rejected
while diagnostic reads remain available. A final regular-config write failure
does not roll back a committed restore: the API returns the successful snapshot
with `ConfigWriteError`, removes the obsolete recovery archive, and retains the
committed journal to report that memory and the config source differ.

At startup, recovery runs before bbolt and the engine open. `prepared` and
`data-applied` restore the verified recovery archive. `committed` keeps restored
storage. If the regular config hash still differs, startup reports the
unresolved write and retains the mutation-blocking journal; it does not claim
that restart selected the restored config. A malformed journal or missing or
modified recovery archive stops startup with recovery-required state.

## Verification

Tests cover writer and snapshot ordering, queued-writer fairness, cancellation,
the bbolt writer boundary, concurrent write capture, protected-config rejection,
immutable-state reapplication, startup rollback, archive compatibility,
duplicate and escaping entries, symlinks, expansion limits, path limits,
entry-count limits, API behavior, and disk and malformed-input failures. Fuzz
targets exercise path canonicalization and cross-platform alias registration.
Live-restore tests also cover every journal transition, simulated process death,
startup rollback, committed config mismatch, recovery-copy and journal write
failures, request cancellation, successful and failed epoch drains, fresh
rollback epochs, failed-rollback mutation blocking, config-write failure, and a
blocked notification subscriber. The journal decoder has a hostile-input fuzz
target.

The focused commands are:

```sh
go test -race -cover ./almagestd
go test ./almagestd -run '^$' -fuzz '^FuzzSnapshotEntryName$' -fuzztime=20s -parallel=2
go test ./almagestd -run '^$' -fuzz '^FuzzSnapshotEntryAliases$' -fuzztime=20s -parallel=2
go test ./almagestd -run '^$' -fuzz '^FuzzRestoreJournalDecoder$' -fuzztime=20s -parallel=2
```

The fuzz commands run in a network-disabled Go container.

### Checkpoint 28 results, 2026-10-10

- The race-enabled `almagestd` package passed with 78.9% statement coverage.
- Two 20-second, two-worker fuzz campaigns passed in a network-disabled Go
  1.26.6 container. The final runs completed 16,084 entry-name and 28,033
  alias-registration executions. A discovered trailing-slash directory case is
  retained as a regression corpus entry.
- The `snapshots_restore`, `snapshot_archive_validation`, and
  `snapshots_validation` Docker E2E cases passed together. This includes
  compatibility restoration of a daemon-generated TLS CA.
- The full lint, vet, panel, cross-build, and Go race/coverage gates passed. The
  aggregate gate exposed a one-second checkpoint-27 transport-test deadline;
  that functional test now uses the unchanged 30-second production default and
  passed five race-enabled repetitions.
- The complete `just check` did not finish its optional Windows VM phase. The
  existing `m25::windows-role-profiles-caller` case failed once, and its focused
  retry lost guest transport before the test ran. Evidence is in
  `artifacts/winvm/run-biE5ocyL` and `artifacts/winvm/run-F6hEbCog`.
- No host route, DNS, firewall, socket, or TUN experiment was run. Snapshot E2E
  work ran in Docker, fuzzing used `--network none`, and Windows checks ran in
  WinVM.

### Checkpoint 29 results, 2026-10-10

- The race-enabled `almagestd` and isolated runtime packages passed. The full
  repository Go race/coverage phase also passed with 77.6% aggregate statement
  coverage; `almagestd` reported 35.8% in that aggregate run.
- `FuzzRestoreJournalDecoder` ran for 20 seconds with two workers in a
  network-disabled Go 1.26.6 container. It completed 81,314 executions without
  a failure.
- The `snapshots_restore`, `snapshot_archive_validation`, and
  `snapshots_validation` Docker E2E cases passed together. Static checks,
  spelling, secret scanning, lint, vet, panel tests, and Windows amd64/arm64
  lint also passed.
- A Windows amd64 test cross-build passed. The full `just check` did not finish
  its optional WinVM or full Docker E2E phases: the guest SSH transport failed
  before an ordinary Windows test ran, and fail-fast cancellation stopped the
  concurrent Docker build. Evidence is in
  `artifacts/winvm/run-moLDcvW7`. The focused Docker snapshot run then passed.
- No host route, DNS, firewall, socket, or TUN experiment was run. Snapshot E2E
  work ran in Docker, journal fuzzing used `--network none`, and the failed
  native Windows attempt stayed inside WinVM.
