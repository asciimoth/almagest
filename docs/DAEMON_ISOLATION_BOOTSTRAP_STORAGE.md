# Trusted bootstrap and storage preparation

Checkpoint 23 is implemented in `bin/internal/isolation/bootstrap` and
`bin/internal/isolation/roleentry`. The packaged Linux production entry uses
these components. Direct mode continues to use its local config and storage
implementations and does not migrate storage or start a role process.

## Process and input boundary

The role commands reject command-line role selection, regular files, terminals,
and missing inherited bootstrap channels. C has a restricted fixed-target
service entry. The Linux production N entry performs the complete trusted
bootstrap and fails closed if the installed profile is incomplete. Dependency tests
keep the daemon runtime, library node code, ZIP, bbolt, and Configer out of N,
C, and the trusted bootstrap package. Only N may import native sysnet backends;
C and the input bootstrap package must exclude them.
The dependency checks cover Linux amd64 and Windows amd64/arm64 imports,
including build-tag-selected code, without executing foreign-platform binaries.
Snapshot transfer limits are shared through the dependency-free
`internal/snapshotlimits` package, so
bootstrap does not import the full daemon API or engine for a size constant.

Linux native settings now live in `bin/internal/nativebackend`, separate from
the daemon platform package and Configer. Direct startup and the isolated
native composition use the same defaults and scalar conversions. Trusted
bootstrap can decode the selected `System.Linux` JSON object without importing
runtime code. This parser does not read files, construct a backend, or install
executable callbacks. Callback settings remain data until trusted code binds
them. The production N entry consumes this parser.

Under the revised trusted-backend design, N loads its native sysnet backend
without loading node or daemon-runtime code. On Linux, trusted
bootstrap becomes N in place through `linuxprofile.PrepareBackend`; it must
start restricted children before that preparation and accept requests only
after preparation succeeds. `networkrole.RunLinux` now assembles this lifecycle
from trusted, held bootstrap inputs. `RunLinuxProduction` and the production
command entry connect input preparation, role cgroups, callbacks, the journal,
and admin gates.

The Linux assembly creates its own private lanes, starts R/C/H under distinct
identities, obtains restriction acknowledgements, prepares N in place, and
starts the journaled broker and fixed admin listeners. It publishes readiness
only after runtime application readiness. Failure cleanup closes lanes, kills
remaining wrappers, and confirms wrapper and pinned namespace-child exit.
Uncertain cleanup is reported, not treated as success. An already-closed lane
is only a repeated stop request; it does not replace native exit observation.
Once a role starts, the supervisor owns its lifetime. Parent cancellation
requests graceful stop before forced termination; it does not let `os/exec`
kill R concurrently with that stop request.

The caller must supply trust-checked executables, private storage, and
separately provisioned role cgroups. Production bootstrap prepares runtime
inputs and native callback bindings before it starts request service. Windows
has a native assembly boundary and fixed storage materializer, but it remains
disabled until complete Windows daemon qualification and packaging exist.

`LaunchSpec` fixes an absolute executable, trusted native libraries, a trusted
working directory, one assigned role, and a complete environment. It does not
search `PATH`. It rejects duplicate variables and loader variables such as
`LD_PRELOAD`, `LD_LIBRARY_PATH`, `DYLD_*`, and `PATH`. Linux path checks walk
each component through no-follow file descriptors and verify trusted ownership
and permissions. Windows checks reject UNC/device paths, alternate streams,
reparse points, hard links, unsafe owners, null DACLs, and untrusted replacement
rights.

The daemon no longer imports `godotenv/autoload`. A writable `.env` file cannot
silently change protected paths or launch inputs before argument validation.

## Protected and runtime inputs

Bootstrap reads the regular and protected sources once. `--unsafe-config` can
select the untrusted loader only for regular config. The protected source and
every referenced TLS file always use trusted no-follow checks. Input size is
bounded by the compiled 8 MiB config ceiling. Duplicate HuJSON object members
are rejected before a value can be interpreted as authority.

The legacy combined stdin form has a narrow bootstrap parser. It accepts at
most one top-level `Protected` member, removes that member from the bytes for R,
and applies protected defaults when it is absent. This exception applies only
to the local launch stream. A regular file or startup snapshot cannot promote a
`Protected` member.

Bootstrap resolves protected secret environment variables and exact TLS file
references once. The runtime record contains values, node scopes, TLS policies,
and source categories, but no environment names or file paths. Each item is at
most 1 MiB and all resolved immutable data is at most 8 MiB. A standardized
protected-document digest binds later role sessions without making R a policy
source. PEM and policy semantic validation stays in R; bootstrap only resolves
the trusted sources.

`PrepareRuntimeStorage` materializes the regular bytes, resolved protected
document, and optional opaque snapshot under three fixed reserved names in R's
held private directory. It stages each file with `openat`, `O_EXCL`, and
`O_NOFOLLOW`, sets mode `0600`, flushes it, transfers it to R's final identity,
and installs it with `renameat`. It does not traverse or replace other runtime
data. A missing snapshot removes only the fixed stale snapshot name. Bootstrap
must have directory-write authority while it performs this step. For the
capability-limited Linux qualification profile, this requires
`CAP_DAC_OVERRIDE`; file ownership transfer separately requires `CAP_CHOWN`.
Mode is set before ownership transfer so `CAP_FOWNER` is not required.

Startup snapshots remain untrusted. Bootstrap opens only the exact
operator-selected file or stdin and gives R a read-only, 32 MiB bounded byte
stream. It does not import or call ZIP, HuJSON config, bbolt, or snapshot restore
code. Public certificate refresh uses a closed `none` or `OS` source selection
and a compiled byte limit. It has no path field.

## Storage layout and migration

`StorageLayout` defines five explicit absolute paths: legacy application data,
private runtime data, the broker journal, an operator backup, and a durable
migration state record. Paths cannot overlap. Their parents must be trusted and
non-replaceable. Protected configs, role binaries, native libraries, and broker
journals can be checked against both the legacy and runtime-writable trees.

Migration is an explicit stopped-daemon operation. It performs these phases:

1. Validate every path and all legacy entries. Reject links, special files,
   existing destinations, unsafe parents, and unsupported layouts.
2. Atomically rename the legacy tree to the operator backup and record the
   phase durably.
3. Copy regular files through no-follow, single-link handles into a private
   staging tree. Sync each file and atomically install the runtime tree.
4. Create an empty private broker directory as a sibling, never below R's
   storage, and retain the completed durable record for later rollback.

An ordinary error rolls the completed phases back when their ownership can be
proved. A later `Recover` rolls an interrupted incomplete record back, or
validates and keeps a record that reached the complete phase. An explicit
`Rollback` requires that record, removes only identity-matching migration
outputs, and restores the preserved backup. The code never recursively changes
ownership and never adopts an existing file or directory.

The version 2 record binds the exact five-path layout and native directory
identities: device/inode on Linux, and volume/file index on Windows. It records
the runtime staging identity before copying data or installing the runtime
directory. Keep the completed record with the backup until rollback is no
longer needed. A missing record, changed directory, missing backup, occupied
legacy path, or directory created before its identity could be recorded stops
recovery without removing outputs. Stop all roles before migration or rollback;
the trusted parent directories must not be changed during the operation.
Rollback restores the migration-time backup, so preserve any later application
data separately before requesting it.
Rollback also refuses a nonempty broker directory. Resolve host recovery first
and preserve the broker records outside the migration layout before rollback.
The storage migrator cannot decide whether native host effects are safe to
remove and must not discard their only recovery evidence.

Version 1 records have no directory ownership evidence and are rejected. They
were used only by the development component, not a released isolated daemon.
An operator must inspect such a layout and preserve its data; changing the
record version does not create valid ownership evidence. Derived staging and
temporary-record paths must be unoccupied and cannot overlap any layout path.
Record decoding rejects unknown phases, duplicate or case-alias fields,
trailing values, and data above 16 KiB.

## Verification

Portable tests cover regular/protected source separation, bounded combined
stdin, duplicate members, exact environment and TLS resolution, opaque snapshot
input, launch environment injection, role-channel requirements, import
boundaries, successful migration, every injected migration phase failure,
interrupted recovery, explicit rollback, occupied destinations, overlapping
paths, links, hard links, and runtime/trusted-path overlap. Windows-native tests
add DACL, hard-link, reparse-point, alternate-stream, UNC, and replaceable-parent
checks.

The fuzz targets exercise combined HuJSON splitting, protected immutable input
resolution, launch environment validation, and storage-layout path relations.
The focused commands are:

```sh
go test -race -cover ./internal/isolation/bootstrap ./internal/isolation/roleentry
go test ./internal/isolation/bootstrap -run '^$' -fuzz '^FuzzCombinedProtectedSplit$' -fuzztime=30s
go test ./internal/isolation/bootstrap -run '^$' -fuzz '^FuzzResolveProtectedDoesNotPanic$' -fuzztime=30s
go test ./internal/isolation/bootstrap -run '^$' -fuzz '^FuzzRuntimeProtectedDocument$' -fuzztime=30s
go test ./internal/isolation/bootstrap -run '^$' -fuzz '^FuzzStorageLayoutValidation$' -fuzztime=30s
go test ./internal/isolation/bootstrap -run '^$' -fuzz '^FuzzLaunchEnvironmentValidation$' -fuzztime=30s
just test-total winvm:m23::trusted-bootstrap-storage
```

Complete Linux child launch, protected-policy distribution, sandbox
installation, and production storage selection are connected at checkpoint 32.
The equivalent Windows rollout remains unavailable.

### Checkpoint 23 results, 2026-10-10

- The race-enabled portable bootstrap, role-entry, and protocol suites passed.
  Bootstrap statement coverage was 76.3%; role-entry coverage was 76.5%.
- Four 30-second fuzz campaigns passed with two workers in a
  network-disabled Go 1.26.6 Docker container. They completed 297,377 combined
  input, 778,593 protected-resolution, 471,510 storage-layout, and 884,150
  launch-environment executions.
- Linux vet and Windows amd64/arm64 cross-builds passed.
- The Windows Server 2022 amd64 ordinary-user case passed in
  `artifacts/winvm/run-hiYZM7Ym`. It verified every registered portable, fuzz
  seed, DACL, hard-link, directory-junction, migration, and role-channel test
  without elevation.
- The full check ran all lint, vet, portable Go, panel, Docker E2E, and Windows
  ordinary-user and privileged suites successfully. The Windows suite evidence
  is in `artifacts/winvm/run-24JtKs2C`.
- The final, independent two-guest packet-flow gate did not complete. Its first
  run and an isolated retry timed out in the existing `flow-network.ps1`
  network-configuration step, before an Almagest packet-flow scenario started.
  The retry evidence is in `artifacts/winvm/run-flow-gQgNxxNR`. This change does
  not modify the packet-flow harness or host network state.
- The first VM run found that an ordinary user cannot create a symbolic link,
  which made a required reparse-point test skip. The final test uses an
  ordinary-user directory junction and requires the rejection to pass.
- Direct mode starts no role process and performs no storage migration. No
  network, route, firewall, DNS, or TUN state changed in these checks.
