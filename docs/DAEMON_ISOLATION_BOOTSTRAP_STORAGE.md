# Trusted bootstrap and storage preparation

Checkpoint 23 is implemented in `bin/internal/isolation/bootstrap` and
`bin/internal/isolation/roleentry`. These are isolated-mode components. They are
not connected to daemon startup. Direct mode continues to use its local config
and storage implementations and does not migrate storage or start a role
process.

## Process and input boundary

The network-broker and config-writer commands have minimal entry packages. They
import only the role-entry and versioned protocol packages. They reject command
line role selection, regular files, terminals, and missing inherited bootstrap
channels. The commands stop with an unavailable message because checkpoints 24
and 25 have not installed production restriction profiles. A dependency test
keeps the daemon runtime, nodes, panel, ZIP, bbolt, Configer, and native sysnet
backends out of these privileged role binaries.

This describes the current entry stubs, not the final backend dependency rule.
Under the revised trusted-backend design, N must load its native sysnet backend
without loading node or daemon-runtime code. C must still exclude that backend.
The final dependency tests must distinguish these roles. On Linux, trusted
bootstrap can become N in place through `linuxprofile.PrepareBackend`; it must
start restricted children before that preparation and accept requests only
after preparation succeeds. Production composition is not connected yet.

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
   storage, and complete the durable record.

An ordinary error rolls the completed phases back before it returns. A later
`Recover` rolls an interrupted incomplete record back, or finishes a record
that reached the complete phase. An explicit `Rollback` removes only validated
migration outputs and restores the preserved backup. The code never recursively
changes ownership and never adopts an existing file or directory.

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
go test ./internal/isolation/bootstrap -run '^$' -fuzz '^FuzzStorageLayoutValidation$' -fuzztime=30s
go test ./internal/isolation/bootstrap -run '^$' -fuzz '^FuzzLaunchEnvironmentValidation$' -fuzztime=30s
just test-total winvm:m23::trusted-bootstrap-storage
```

Complete child launch, protected-policy distribution to every broker,
Linux/Windows sandbox installation, and production storage-path selection
remain later checkpoints.

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
