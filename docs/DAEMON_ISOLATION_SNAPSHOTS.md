# Isolated snapshot creation and startup restore

Checkpoint 28 is implemented in the runtime-owned `almagestd` snapshot
coordinator. This change does not enable the production isolated composition.
Direct mode keeps the same snapshot API and archive version. The future
isolated runtime uses the same coordinator after bootstrap gives it a bounded,
read-only startup archive stream.

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

## Verification

Tests cover writer and snapshot ordering, queued-writer fairness, cancellation,
the bbolt writer boundary, concurrent write capture, protected-config rejection,
immutable-state reapplication, startup rollback, archive compatibility,
duplicate and escaping entries, symlinks, expansion limits, path limits,
entry-count limits, API behavior, and disk and malformed-input failures. Fuzz
targets exercise path canonicalization and cross-platform alias registration.

The focused commands are:

```sh
go test -race -cover ./almagestd
go test ./almagestd -run '^$' -fuzz '^FuzzSnapshotEntryName$' -fuzztime=20s -parallel=2
go test ./almagestd -run '^$' -fuzz '^FuzzSnapshotEntryAliases$' -fuzztime=20s -parallel=2
```

The fuzz commands run in a network-disabled Go container. Live restore epochs,
broker drain, application restore journals, and crash recovery remain checkpoint
29 work.

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
