# Subprogram sandbox

Almagest has a daemon-owned sandbox service for programs that graph nodes must
start. The service is implemented for direct daemon mode on Linux and Windows,
and for isolated daemon mode on Linux. `almagest/TorDriver` uses this service
through `github.com/asciimoth/gobox`; it has no host-process fallback.

This document describes the implemented security model, profiles, lifecycle,
configuration, and current limits. The protected-config schema is the canonical
configuration reference.

## Security model

The daemon loads protected program and sandbox-template policy at startup. A
node receives only an owner-bound sandbox view. It can select an allowed
template and program ID and can supply only the typed inputs declared by that
program. It cannot select an executable path, raw arguments, credentials, host
paths, or another node's owner identity.

The service treats the program, its descendants, files, and output as
untrusted. One sandbox instance is one trust domain: its programs can share its
writable data and local sockets. Different instances do not share these
rights. Protected program profiles also declare the descendants that a program
can execute.

The sandbox owns process trees, temporary storage, optional persistent storage,
bounded file operations, and local socket operations. The full Linux profile
has no host-network path. External traffic must use an application capability
that the owner supplies. For Tor, this is the upstream proxy over the graph
`Network` selected by the node.

Subprogram sandbox mode and daemon isolation mode are independent. For example,
a direct daemon can use strict subprogram sandboxes. An isolated runtime cannot
gain direct launch authority when subprogram sandbox mode is `off`; launches
still go through the trusted supervisor.

The trusted computing base includes daemon bootstrap, the sandbox supervisor,
native launch code and workers, and the operating system. The sandbox does not
protect against their compromise, kernel defects, or an administrator. It also
does not prevent data transfer through a proxy that the owner intentionally
provides.

## Modes and reported guarantees

| Mode | Behavior |
| --- | --- |
| `off` | Applies no child confinement. The service still enforces named programs and templates, owner scope, bounded APIs, and process bookkeeping. |
| `auto` | Applies the compatible protections that are available. It reports each omitted protection and can run with a partial boundary. |
| `strict` | Requires all protections in the qualified OS profile. Startup or launch fails before third-party code runs if a required protection is unavailable. Reviewed OS-profile exclusions remain visible. |

The default is `auto`. A selected plan is immutable for the instance. A setup
or verification failure never retries with fewer protections.

Status records each feature as `applied`, `omitted-auto`, `excluded-os`, or
`off`, with a stable reason. `strict_satisfied` means that the selected OS
profile met its strict contract. `full_boundary` means that no feature was
omitted or excluded. These values differ on native Windows: strict can be
satisfied while the full boundary is false.

Templates can strengthen a platform profile with `RequiredFeatures`. An
explicit requirement cannot use an OS exclusion. For example, a Windows
template that requires `private-network` fails before launch.

## Effective profiles

| Effective profile | Availability | Guarantees and limits |
| --- | --- | --- |
| `direct-v1` | Portable fallback and mode `off` | In `auto`, constructs the environment and controls inherited handles. It does not confine the filesystem, identity, processes, or network. Mode `off` reports every protection as off. |
| `linux-landlock-v1` | Linux amd64 ordinary-user path | Applies Landlock, `no_new_privs`, protected writable storage, environment and handle controls. It has no private mount, PID, IPC, UTS, network, or cgroup namespace. It is an `auto` profile and cannot satisfy strict mode. |
| `linux-full-v1` | Qualified Linux amd64 host | Uses a static launcher, PID 1 worker, private mount/network/PID/IPC/UTS/cgroup namespaces, exact mount-root view, private proc and device views, loopback-only networking, Landlock, seccomp, identity removal, rlimits, and per-instance and per-command cgroups. It provides the full boundary. |
| `windows-native-v1` | Windows Server 2022 build 20348, amd64 | Uses a write-restricted Low-integrity token, disabled administrative groups and privileges, an instance restricting SID, protected writable-root ACLs and labels, exact inherited-handle lists, suspended launch, no-breakaway Job Objects, descendant cleanup, and process, memory, and CPU limits. Exact filesystem confinement and private networking are OS exclusions, so it does not provide the full boundary. |

Linux strict mode also requires a usable delegated cgroup v2 subtree, the
required capabilities, Landlock ABI 3, recursive mount attributes, namespace
support, and the approved native launcher. A missing prerequisite fails
preflight. The partial Linux profile uses host Unix-socket and mount namespaces;
its status reports those omissions.

The Windows child uses host networking and can read objects available to the
filtered source identity. Low integrity and the write-restricted instance SID
limit writes to objects that pass both access checks. Windows soft process
signals are unsupported; terminate and kill act on the complete Job Object.

## Protected policy

`Sandbox` in the protected daemon configuration contains:

- `Mode`, service roots, service-wide bounds, and Linux launcher/cgroup paths;
- named `Programs` with a held executable, the `fixed-v1` argument builder,
  typed required inputs, and approved descendants; and
- named `Templates` with allowed node classes and programs, a runtime profile,
  environment rules, read-only inputs, writable-state policy, required
  features, platform identity, and resource limits.

See
[`almagestd-protected-config.schema.json`](../almagestd-protected-config.schema.json)
for all fields, bounds, and defaults. Program lookup and all host paths come
from this protected policy, not graph configuration. Policy validation rejects
overlapping writable roots, unsafe read-only sources and targets, unknown
features, invalid environment inheritance, and incomplete limit requirements.

`DiscoverSystemPrograms` or `--discover-system-programs` can generate the
built-in Tor policy from the host `PATH`. Discovery is off by default and
explicit entries take precedence. Discovery is best effort: failure is stored
as a diagnostic for the affected generated capability and does not disable
unrelated or explicit policy.

On Linux, discovery records the executable's ELF dependency closure and exact
read-only mappings, including absolute content-store paths. On Windows, it
records the exact executable; the native profile uses filtered source-identity
reads for runtime dependencies. See [`lib/tor/README.md`](../lib/tor/README.md)
for the Tor-specific policy and discovery behavior.

## Runtime and owner lifecycle

The `gobox` contracts provide owner, sandbox, command, file, socket, typed
launch, and status APIs. `lib/nodekit` exposes only the calling node's
owner-bound view. A missing service returns `ErrUnavailable`; it never starts a
normal host process instead.

An owner has a durable opaque key and a lifetime epoch. Node names are mutable
labels and are not storage identities. The service fences stale handles and
generations, makes acquisition idempotent for an owner/slot/template tuple, and
applies bounded cancellation and cleanup.

| Event | Instance and state behavior |
| --- | --- |
| Link or equivalent configuration change | Keeps the instance and persistent state. The node decides whether to restart its command. |
| Node rename | Rebinds the same durable owner and keeps its instances and storage. |
| Node reconstruction | Fences the old lease and binds the same logical owner when authorized. |
| Sandbox remove or close | Stops its process tree and removes temporary resources. Persistent data remains. |
| Node deletion | Revokes the owner and closes all slots. Protected policy decides whether persistent state is retained or deleted. |
| Daemon stop or restart | Does not adopt old processes or sockets. Authorized persistent data can be reopened. |
| Snapshot restore | Stops and fences sandboxes, but does not roll persistent sandbox state back. |

The durable owner registry has an exclusive cross-process lock, atomic
replacement, rename receipts, and orphan records. If startup finds an owner
that was active during an unclean stop, it quarantines that owner because it
cannot prove that escaped descendants are dead. Other owners remain usable.

`Engine.RenameNode` commits the durable owner rebind before it publishes the
new graph name. Legacy locked mutations and Pasta history operations use a
workspace observer. If their owner update fails, the coordinator becomes
terminal and refuses later sandbox access and config saves instead of
persisting mismatched identity metadata.

## Files, sockets, and processes

Sandbox file paths are relative to either the temporary or persistent root.
The API rejects traversal, symlinks or reparse points in traversed components,
special files, hard-link aliases, and native handles. It bounds open files,
directory pages, transfers, and temporary storage. The child sees reserved
temporary and state paths through its constructed environment.

The local network API accepts numeric loopback TCP and supported loopback UDP.
Pathname Unix sockets are available below writable roots on Linux. DNS names,
wildcard and non-loopback addresses, raw sockets, multicast, and interface
operations are rejected. With full Linux networking, loopback belongs only to
the sandbox namespace. With a partial, Windows, or off profile, it is host
loopback and status reports the exposure.

Commands use fixed typed arguments and tracked process identity. `Start`
returns only after setup and exec are known to have succeeded; it does not mean
that the application is ready. Context cancellation and `Kill` terminate the
owned tree. `Wait` returns one stable result. Output, relay buffers, commands,
sockets, instances, and outstanding operations have finite bounds.

Template limits cover commands, tasks, broker-open files, sockets, memory,
CPU, transfers, temporary bytes, and output. Limits listed in
`Limits.Required` also require the `resource-limits` feature and fail preflight
if the selected backend cannot enforce the complete group.

## Status and operations

The admin `hello` response includes `subprogram_sandbox` with:

- requested mode, platform, and supervisor identity;
- each template's effective profile and identity;
- `strict_satisfied`, `full_boundary`, feature results, and warnings;
- service limits; and
- active, orphaned, and quarantined owner counts.

In `auto`, a missing privilege drop produces a prominent warning with the
effective child identity. Operators that cannot permit retained launch
authority must use `strict` and verify startup succeeds.

## Current limitations

- Windows daemon isolation has no sandbox-supervisor role. Native Windows
  sandboxes require explicit direct daemon mode.
- Native Windows has no exact filesystem view or private network. In strict
  mode, a read-only source whose target is a different path fails preflight.
  Required child file-count and aggregate temporary-byte limits are also
  unavailable. The Windows `Tasks` limit counts active processes, not threads.
- The ordinary-user Linux profile is partial. It does not enforce `Tasks` with
  `RLIMIT_NPROC`, because that would count unrelated processes for the same
  host UID. `Linux.IdentityPool` is reserved and fails preflight.
- Partial profiles validate a read-only source but do not materialize it at a
  different target. The child must use the source's existing path. Status
  reports that the exact filesystem view is omitted.
- Linux read-only inputs are validated and held before launch, but full-profile
  inputs are live bind mounts. A trusted host process can change a source while
  a sandbox runs.
- Full Linux and native Windows qualification is limited to the platforms in
  the profile table. Other architectures and Windows releases need separate
  hostile-child and resource-limit qualification.
- Persistent storage has no aggregate byte quota. It remains outside graph
  snapshots and must be monitored and backed up separately.
- Recovery cannot yet prove that descendants from an unclean session are dead,
  so it quarantines that owner instead of reusing it.

## Implementation map

- `lib/sandbox_owners.go` and `lib/nodekit/sandbox.go`: engine owner lifecycle
  and node-bound capabilities.
- `bin/internal/subprogram/policy`: protected-policy validation and feature
  plans.
- `bin/internal/subprogram/service` and `manager`: durable owners, instances,
  cleanup, and status.
- `bin/internal/subprogram/direct`: portable, Linux native, and Windows native
  backends.
- `bin/internal/subprogram/isolated`: Linux isolated-runtime proxy and
  supervisor protocol.
- `almagest-subprogram-launcher`: native Linux launcher source.

Qualification includes Linux Docker tests for root and ordinary-user paths,
the daemon-isolation by sandbox-mode matrix, Windows VM security and mode
matrices, and Tor protocol and traffic tests. The Windows tests validate the
documented exclusions and do not claim a full boundary.
