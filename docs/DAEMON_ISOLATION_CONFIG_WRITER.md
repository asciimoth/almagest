# Fixed-target isolation config writer

Checkpoint 19 is implemented in `bin/internal/isolation/configwriter`.
`bin/cmd/almagest-config-writer-role` is the restricted C process entry point.
The packaged Linux production composition starts it when regular config is
writable. Read-only configurations omit C.

## Contract

The writer implements the version 1 `ReadCurrent`, `Replace`, and `GetResult`
contract. Bootstrap selects one target when it constructs the writer. No
request contains a path, file mode, owner, ACL, temporary name, protected
config selector, command, or network operation.

Config content is opaque and limited to 8 MiB. Revisions are SHA-256 content
revisions. Wire reads and replacements move bytes on the bounded config
transfer lane; their control replies and requests carry only a transfer ID and
revision. Inline wire data is rejected. The component does not parse HuJSON,
archives, databases, or graph data.

The writer serializes reads and changes. It compares the expected revision
before a write and retains up to 8,192 operation receipts. A repeated operation
ID with the same input returns its first result without another write. Reuse
with different input is a conflict. A caller uses `GetResult` after a lost
reply. Receipt expiry returns `ErrOperationUnknown`; it never causes an
automatic replay.

Pre-commit failures are `rejected`. A failure after the atomic replacement is
`uncertain` and uses the new observed revision. This distinction prevents a
caller from treating a durability failure as proof that no replacement
occurred. Cancellation is checked before temporary-file work and immediately
before the commit boundary.

## Linux target handling

Linux opens the target directory from `/` with `openat2` and
`RESOLVE_BENEATH`, `RESOLVE_NO_SYMLINKS`, and `RESOLVE_NO_MAGICLINKS`. It keeps
that directory descriptor for the writer lifetime. All target and temporary
operations are relative to the held descriptor. The writer rejects a symlink,
magic link, non-regular target, cross-volume target, hard-link alias, unsafe
owner, writable group/other mode, changed approved metadata, and changed file
identity at the commit boundary.

Replacement uses a random private sibling created with `O_EXCL` and
`O_NOFOLLOW`. It preserves the approved mode, UID, and GID, writes all bytes,
flushes the file, closes it, rechecks the target identity, renames relative to
the held directory, and flushes the directory. A directory-flush error after
rename is an uncertain result.

The selected Linux profile therefore requires `openat2`. An older kernel that
cannot provide these resolution rules fails writer construction. There is no
path-based compatibility fallback.

## Windows target handling

Windows accepts only canonical local drive paths. It rejects UNC and device
paths, alternate data streams, DOS device aliases, trailing-dot/space aliases,
reparse points in every component, hard-link aliases, and cross-volume targets.
It opens and retains the target directory without delete sharing. Target reads
use no-follow handles and verify the volume/file identity, owner, bounded DACL,
mode, and exact approved security descriptor.

The writer creates and flushes a temporary sibling. `ReplaceFileW` provides the
atomic target replacement and preserves the target ACL. The replacement is
then reopened through a no-follow handle and flushed. Sharing violations or
post-commit validation/flush failures do not become successful receipts.

The component does not create the C process token, AppContainer, job, integrity
label, or directory ACL. Those launcher controls remain checkpoints 23 and 25.
The registered Windows VM case exercises the file and ACL behavior under the
ordinary-user phase.

## Test coverage

Portable tests cover opaque binary content, compare-and-replace, stale
revisions, operation-ID reuse, lost-reply lookup, receipt expiry, size limits,
foreign sessions, cancellation, concurrent writers, pre-commit failures,
post-commit uncertainty, peer loss, unknown operations, inline-wire rejection,
and a complete config-transfer/control exchange.

Linux tests use real files and cover target and parent symlinks, hard links,
unsafe parent permissions, non-canonical paths, post-open symlink redirection,
external replacement, and metadata changes. Windows tests cover real atomic
replacement, unsafe path forms, hard links, and reparse points. The portable
fuzz target checks stable receipts and file-content invariants across arbitrary
opaque bytes, revisions, operation IDs, cancellation, and retry.

Use these focused checks:

```sh
go -C bin test -race -cover ./internal/isolation/configwriter
go -C bin test ./internal/isolation/configwriter -run '^$' -fuzz '^FuzzWriterReceipts$' -fuzztime=30s -parallel=2
just test-total winvm:m19::fixed-target-config-writer
```

The Windows selector is required platform evidence. A cross-compiled test
binary is only a build check and does not replace the VM run.

The final checkpoint 19 Windows run passed without skips on 2026-10-11 as the
ordinary VM user. It created a real NTFS junction without symlink privilege,
rejected a target below that reparse-point parent, and proved that the held
parent cannot be renamed and replaced after writer startup. The selector also
requires the hard-link checks and all portable receipt seeds. The retained run
is `artifacts/winvm/run-tIM0MI2L`; its source archive SHA-256 is
`a6ef6e7909f200a7287795e4abe3cecac1ffcd4f7e060d6112b91f91d549eee4`.

The portable race-enabled writer suite passed with 78.1 percent statement
coverage. A separate 30-second, two-worker `FuzzWriterReceipts` campaign
completed 1,607,081 executions and preserved the receipt and content
invariants.

## Checkpoint 27 runtime integration

`bin/internal/isolation/configpersistence` connects R's existing
`daemonservices.ConfigPersistence` contract to C. The Linux production
composition uses this path. Direct mode continues to use `daemonservices/localconfig` and
does not create a transport, transfer buffer, or C process.

R sends opaque config bytes once on the bounded config-transfer lane and then
sends one revision-checked replace request. If the replace reply is lost, R
queries the retained operation receipt with the same operation ID. It never
replays the replacement. An unavailable or expired receipt produces an
explicit recovery-required result. A rejected transfer before the replace
request is not reported as uncertain because C has no mutation request to
apply.

The daemon service result now identifies `committed`, `rejected`, and
`uncertain` outcomes. The config coordinator publishes `Config.Main` and the
new admin revision only after a committed result. On rejection or uncertainty,
it reconciles the prior graph and keeps the prior published document. It never
writes the old bytes as rollback, so it cannot overwrite a newer external file
or a commit whose reply was lost. The error identifies recovery as required
when C cannot prove the result. This remains a coordinated transaction, not an
atomic transaction across the graph, host effects, and filesystem.

The existing runtime mutation lock serializes autosave, admin updates, and
snapshot config replacement. A new concurrency test confirms that two admin
updates with the same revision produce one commit and one conflict, and that
the active graph, published document, and fixed target agree. A separate
integration test runs an admin update and a later autosave through a real C
writer and authenticated transport. Existing update tests continue to cover
selective reconciliation, equivalent updates, graph rollback, autosave retry,
shutdown flush, and revision conflicts. A runtime-initialization failure test
also removes an active node before the candidate fails, then confirms that
rollback restores the old graph and does not send a config replacement.

Portable adapter tests use a real writer and authenticated transport. They also
cover transfer failure, malformed read transfer identity, lost replace replies,
temporarily unknown receipts, C death, recovery-required results, concurrent
call serialization, stale revisions, and receipt lookup. The result conversion
fuzz target checks arbitrary outcomes, codes, revisions, and operation IDs.

Focused verification on Linux (amd64, Go 1.26.7) on 2026-10-10:

```sh
go -C bin test -race -cover ./internal/isolation/configpersistence ./internal/isolation/configwriter ./almagestd/daemonservices/localconfig
go -C bin test ./internal/isolation/configpersistence -run '^$' -fuzz '^FuzzConvertConfigResult$' -fuzztime=10s -parallel=2
```

The adapter coverage was 82.4 percent, the writer coverage was 78.5 percent,
and the direct local persistence coverage was 76.6 percent. The fuzz run
completed 386,450 executions. These component results do not replace the full
project gate or the later Linux and Windows complete-composition gates.

The C process uses the dedicated control-only transport constructor. It cannot
receive a network data lane or admin lane. After the platform restriction is
installed, it reads one bounded trusted bootstrap record, validates one target
file name below its held directory, opens the fixed target, and publishes
readiness. The Linux profile Docker gate launches the real static C executable
and performs a read and durable replacement through `configpersistence`.
Duplicate fields, path separators, alternate-stream syntax, false readiness,
peer loss, and malformed target names are rejected. Bootstrap validation has a
fuzz target.

Windows metadata validation fingerprints the owner, DACL protection state, and
ordered ACE bytes. It does not hash the non-semantic offsets and padding of a
self-relative security descriptor because `ReplaceFileW` can rebuild that
layout while it preserves the same security. The portable Windows persistence
and daemon coordinator tests verify that a durable replacement remains
committed after this rebuild.
