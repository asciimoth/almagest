# Fixed-target isolation config writer

Checkpoint 19 is implemented in `bin/internal/isolation/configwriter`. It is a
component only. It is not connected to daemon startup and does not enable an
isolated production mode.

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
