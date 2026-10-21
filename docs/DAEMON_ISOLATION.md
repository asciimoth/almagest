# Daemon process isolation

This document describes the current daemon isolation design. It is an
implementation reference, not a record of the experiments or delivery steps that
produced the design.

## Status and scope

Linux uses automatic isolation by default. On Linux/amd64, `auto` selects one of
two qualified profiles: the complete privileged `linux-full-v1` profile or the
non-root `linux-user-v1` profile. The packaged systemd unit selects `auto`
explicitly. Windows uses the direct, single-process daemon by default because
its complete service startup and packaging are not implemented. Other targets
select isolation and fail closed when no qualified profile exists. Use
`--isolation=off` or its compatibility alias `--no-isolation` to select direct
mode explicitly. `--experimental` requires explicit direct mode.

Isolation limits damage from a compromised graph runtime. It keeps native host
network objects, public listener ownership, protected launch authority, config
file replacement, and configured process creation outside that runtime. It does
not make the trusted broker harmless. The broker starts with the service
credential and owns all approved host effects. Isolation also does not protect
against a compromised kernel, backend library, installed role binary, package
manager, or root administrator.

For `linux-user-v1`, an unrelated hostile process that already runs as the same
login user is outside the profile guarantee. Such a process can already own or
replace per-user installation and state objects. The profile prevents a
compromised R or C from acquiring that unrestricted same-UID access; it does not
turn all processes of the login account into separate security principals. An
external container or service sandbox remains useful, but its restrictions are
not reported as an equivalent Almagest role boundary.

The design is fail-closed after it selects a profile. `auto` can omit only
optional protections that the selected profile defines as optional. A missing
mandatory kernel feature, invalid trusted path, failed restriction, lost role,
uncertain host effect, or protocol mismatch stops startup or the current process
set. No isolation policy falls back to direct mode, requests elevation, or adds
privileges.

## Processes and binaries

The process names below are also used in code comments: N is the network broker,
R is the runtime, C is the config writer, and H is the command helper.

| Role                             | Linux binary                  | Responsibility                                                                                                                         | Authority                                        |
| -------------------------------- | ----------------------------- | -------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------ |
| N: network broker and supervisor | `almagest-network-role`       | Trusted bootstrap, native network backend, public admin listeners, policy, role supervision, recovery journal, and trusted diagnostics | Service credential and native network authority  |
| R: graph runtime                 | `almagestd`                   | Config parsing after bootstrap, engine and nodes, admin protocol and panel, snapshots, application DNS, and storage data               | Private runtime storage and fixed IPC lanes only |
| C: config writer                 | `almagest-config-writer-role` | Revision-checked durable replacement of one regular config file                                                                        | One fixed writer directory and target name       |
| H: command helper                | `almagest-command-role`       | Approved node commands and native-backend callbacks                                                                                    | One held executable and fixed protected profiles |

`almagestd` is the installed entry point. On Linux/amd64 it opens the sibling N
binary by a trusted file descriptor and replaces itself with N before it reads
config. N then starts R and starts C and H only when their services are needed.
`almagest-linux-role-launcher` is a small native launcher used for R, C, and H.
It is not a resident role and must not be setuid. The
`almagest-isolation-storage` program runs before the service to prepare or
migrate the storage layout.

Direct mode uses the same daemon behavior without the internal roles or IPC. It
remains a supported explicit compatibility and development mode. It keeps its
launch identity for its full lifetime. It does not try to change process
credentials after the Go runtime has started threads. In the isolated Linux
profiles, the single-threaded native launcher installs each role's final UID,
GID, supplementary groups, and capability state before it executes a Go role.

## Trust and ownership rules

Protected config and command-line listener overrides are launch authority. N
reads them before R starts. N resolves secret environment variables and
certificate files, selects the native backend, opens trusted executables and
directories, and creates all IPC endpoints. R receives only literal secret and
certificate values plus the small protected subset that it must use. It does not
receive source paths, environment variable names, listener addresses, peer
filters, system configuration, command profiles, or unknown protected fields.

R builds its default TLS client policy from the public root catalog embedded in
the daemon. It does not need access to host certificate-store paths. Direct mode
can also import platform certificate stores. Protected TLS configuration remains
the way to add private or deployment-specific authorities.

Regular config is untrusted application input. It can select graph behavior, but
it cannot select a role, executable, host path, native API, credential, IPC
endpoint, or isolation policy. Native objects stay in N and are referred to by
session-bound opaque IDs. C owns the mutable regular config target. H owns
process creation. R owns the engine, snapshots, databases, and runtime files.

N owns public web, Unix-socket, and named-pipe listeners. It applies the
configured peer gate and relays accepted byte streams to R. R continues to own
HTTP, WebSocket, admin API, browser-origin, and panel behavior. The relay does
not give R a public listener handle or a client impersonation token.

## IPC contract

Protocol version 1 uses private connected byte streams that trusted bootstrap
binds to specific roles. A session contains a random ID, epoch, protocol
version, and digest of normalized protected policy. Peers must agree on that
complete record. Each request also carries the session, graph epoch, request ID,
and, for mutations, an operation ID. Resource references include the session,
type, generation, and graph epoch.

The N/R control API is a closed typed interface. It permits:

- capability and status reads;
- address and subnet reservation;
- TUN and default-TUN creation and typed updates;
- managed DNS endpoint creation and host DNS configuration;
- numeric socket and listener creation;
- interface and resolver snapshots;
- five-tuple matcher creation and lookup;
- resource close and graph-epoch release;
- mutation-result lookup and runtime readiness.

Separate typed services permit C to read and revision-replace its fixed config
target and H to run a configured command ID with validated slot values. Native
backend callbacks use a separate broker-originated request type. Config bodies
can use acknowledged bounded chunks, but a transfer is not permission to write a
file.

The interface does not permit arbitrary paths, programs, arguments, handles,
file descriptors, environment names, syscalls, `ioctl`, socket options, registry
operations, driver calls, native error text, or generic host callbacks. Unknown
operations and fields are rejected. Runtime requests cannot expand protected
authority. A canceled or timed-out mutation is not assumed to have had no
effect; operation receipts and the recovery journal resolve known and uncertain
outcomes.

Control messages are strict length-prefixed JSON with bounded depth, size,
in-flight work, arrays, strings, resources, and receipts. Control, data, and
diagnostic traffic use separate lanes so data or logging pressure cannot take
the control lock. TUN, TCP, UDP, DNS, and accepted-connection data use bounded
relay frames with typed packet metadata, explicit read/write grants, sequence
numbers, flow control, deadlines, half-close, accept/claim/reject, and
generation checks. No native data handle crosses the isolation boundary.

Diagnostics are bounded structured records. N binds each lane to its process
origin, sanitizes control characters, redacts known secrets again at receipt,
and reports dropped records. A full diagnostic queue drops diagnostics; it does
not block control or packet progress.

The exact limits and request fields are part of the code contract in
`bin/internal/isolation/protocol`, `transport`, `relay`, `diagnostics`, and
`admingate`. Keep detailed wire rules in those packages instead of duplicating
them here.

## Configuration and command line

Isolation mode is selected with these public switches:

| Option               | Behavior                                                                           |
| -------------------- | ---------------------------------------------------------------------------------- |
| `--isolation=strict` | Require `linux-full-v1`. Missing prerequisites stop startup.                       |
| `--isolation=auto`   | Linux default. Select a qualified profile and report omitted optional protections. |
| `--isolation=off`    | Select the direct daemon.                                                          |
| `--no-isolation`     | Compatibility alias for `--isolation=off`.                                         |

`ALMAGEST_ISOLATION` accepts `strict`, `auto`, or `off`. An explicit CLI value
overrides the environment. Conflicting explicit CLI selections and unknown
values are errors. `--no-isolation --isolation=off` is consistent. Policy is
launch authority; regular config, snapshots, and admin updates cannot change it.
On Windows, `off` is the default. Explicit `strict` or `auto` fails on an
unqualified platform.

For a root caller, `auto` selects only `linux-full-v1` and fails if its
prerequisites are unavailable. It never starts a same-UID root runtime. For a
non-root caller, `auto` selects `linux-user-v1` when the full profile is not
available. `strict` never selects the user profile.

Linux isolation resource ceilings can be set at startup through CLI options or
environment variables. CLI values override environment values. These options
have no effect in direct mode or on Windows. Changes require a restart.

| CLI option                 | Environment variable              | Default               | Accepted range (inclusive)            |
| -------------------------- | --------------------------------- | --------------------- | ------------------------------------- |
| `--isolation-memory-bytes` | `ALMAGEST_ISOLATION_MEMORY_BYTES` | 8589934592 (8 GiB)    | 33554432–1099511627776 (32 MiB–1 TiB) |
| `--isolation-tasks`        | `ALMAGEST_ISOLATION_TASKS`        | 4096                  | 8–4096                                |
| `--isolation-cpu-quota`    | `ALMAGEST_ISOLATION_CPU_QUOTA`    | 6400000 (64 CPUs)     | 1000–6400000                          |
| `--isolation-descriptors`  | `ALMAGEST_ISOLATION_DESCRIPTORS`  | 65536                 | 32–65536                              |
| `--isolation-file-bytes`   | `ALMAGEST_ISOLATION_FILE_BYTES`   | 1099511627776 (1 TiB) | 1048576–1099511627776 (1 MiB–1 TiB)   |

Values are integers without unit suffixes. CPU quota is CPU time in microseconds
per 100000 microseconds of elapsed time: 100000 permits one CPU, and 200000
permits two. Tasks include threads. In `linux-full-v1`, memory, task, and CPU
limits apply per role cgroup; descriptor limits apply per process, and file size
is the maximum size of each file, not total storage use. Limits stay enabled
with high defaults. Zero and values outside the accepted ranges stop isolated
startup. Swap remains disabled for each role cgroup.

The sandbox role has one extra cgroup level. The trusted supervisor runs in
`sandbox/supervisor`. The empty `sandbox` parent delegates controllers to gobox
workload cgroups and applies the aggregate role ceiling. This structure keeps
the cgroup v2 delegation point free of processes.

In `linux-user-v1`, per-role cgroups are an optional extension. The status
record reports inherited process-set or external limits when delegation is not
available, but these observations are not equivalent to per-role enforcement. An
explicit memory, task, or CPU setting requires the complete per-role cgroup
extension and stops startup when it is unavailable. Explicit descriptor and
file-size settings require verified process limits. An inherited hard limit
below a requested value is accepted as a stronger ceiling and is reported as
`inherited-lower`.

For example, to set 2 GiB memory and two CPUs per role:

```sh
almagestd --isolation-memory-bytes=2147483648 --isolation-cpu-quota=200000
```

### Direct-mode performance contract

`--isolation=off` and `--no-isolation` select the in-process daemon before
runtime services are constructed. The direct network service returns the
platform `sysnet.System`, its outbound and local networks, and the TUN objects
that it creates without an isolation adapter. Direct-mode network and TUN I/O
does not use isolation IPC, serialization, relay queues, packet copies, or relay
goroutines. Calls through the `sysnet.System` interface can have normal Go
interface-dispatch cost.

The reusable graph library can add wrappers required by configured nodes and
link lifetime ownership. For example, a TUN graph link uses detachable TUN
semantics. This is graph data-path behavior, not direct-mode isolation overhead,
and it is also present when the library receives a `sysnet.System` directly.

For systemd, set environment variables in a service drop-in, then reload the
units and restart the service:

```ini
[Service]
Environment=ALMAGEST_ISOLATION_MEMORY_BYTES=2147483648
Environment=ALMAGEST_ISOLATION_CPU_QUOTA=200000
```

These ceilings do not reserve resources or require that much physical RAM.
Limits on ancestor cgroups still apply to the process set as a whole, even if
they are lower than the sum of the role ceilings. Provisioning also checks the
immediate instance cgroup's memory and task capacity against that sum; the
production bootstrap creates this instance with no additional ceiling. Inherited
file-descriptor and file-size hard limits can reduce the effective per-process
ceilings.

Neither config schema has an `Isolation` object. Existing protected sections
provide authority:

- `Admin` selects public endpoints, peer filters, pprof, and browser-origin
  behavior. Isolated startup requires at least one fixed endpoint.
- `System` must select the native platform backend. The debug backend requires
  direct mode.
- `CommandRunner` is converted to fixed H profiles in `linux-full-v1`. That
  profile permits one shared absolute executable, direct argv execution, bounded
  slot substitution, and no per-command UID or GID selection. `linux-user-v1`
  does not support H; configured node commands or native callbacks that require
  H stop startup instead of running in R or N.
- `Secrets` and `TLS` file or environment sources are resolved by bootstrap. R
  receives only their bounded literal values.

Normal CLI config, protected-config, storage, snapshot, logging, and admin
overrides still apply. In the full production Linux layout, omitted paths
resolve to:

- regular config: `/var/lib/almagest/config/alm.hujson`;
- protected config: `/etc/almagest/protected_alm.hujson`;
- runtime storage: `/var/lib/almagest/runtime`;
- broker state: `/var/lib/almagest/broker`;
- command-helper state: `/var/lib/almagest/helper`;
- Unix admin socket: `/run/almagest/admin.sock`.

Isolated Linux startup does not support `--tmp-file-storage` or
`--tmp-unix-admin`. Fixed protected paths are required. With `--stdin`, the
top-level `Protected` member supplies protected input and no C process is
started, so the config API is read-only. `--stdin` and `--snapshot -` cannot
both consume standard input.

The user profile uses a separate private instance root. By default it is
`$HOME/.local/share/almagest/isolation`, where the home directory comes from the
account database and not from an untrusted environment value. Its `runtime`,
`broker`, and `config` children are disjoint private directories; the config
directory can contain only the fixed config target and the writer's private
`.tmp` directory. If no safe account home exists, startup requires an explicit
root.

`--isolation-state-dir` selects an explicit user-profile instance root. It is
valid only with explicit non-root `--isolation=auto`. `--file-storage` can
override only R's storage directory; it does not move broker or writer state.
All role roots, protected inputs, staged code, and admin-socket directories must
be disjoint. The broker holds an exclusive instance lock.

The default user-profile admin socket is in a validated private child of
`XDG_RUNTIME_DIR`, or in a private `run` child of the instance root when no safe
runtime directory exists. N validates ownership, mode 0700, canonical identity,
filesystem objects, and path separation before it publishes the socket. It does
not require access to `/run/almagest`.

## Bootstrap and startup

Linux production startup first resolves the policy and probes prerequisites in
disposable children under the intended credentials. The probes do not create
TUNs, change routes, or edit host DNS. N selects an immutable profile and
optional extension set before it launches an application role. A failed
application or verification step never causes a retry with fewer protections.

Startup then has these stages:

1. For the full packaged profile, `almagest-isolation-storage prepare` creates
   or validates role-owned directories and performs a stopped legacy storage
   migration when required. The user profile creates and validates its private
   instance tree without changing ownership or moving direct-mode data.
1. `almagestd` selects isolation, opens the sibling N binary without following
   an untrusted replacement, and executes that held object.
1. N parses CLI inputs, loads regular and protected config once, resolves
   protected sources, validates the storage layout, opens trusted role files,
   and opens the broker recovery journal.
1. N constructs immutable policy, admin endpoints, native backend settings,
   supported C/H profiles, resource enforcement, and private IPC lanes.
1. N starts R through the selected native launch plan. Bootstrap copies regular
   config, resolved protected values, and an optional startup snapshot into
   reserved files in R's private storage. In the user profile, N also creates
   and verifies a fresh per-launch executable copy outside writable role trees.
   R applies final restrictions before it reads its bootstrap record or builds
   the application.
1. N starts supported optional C and H roles, waits for every role to return a
   versioned applied-profile record and pass policy-denial probes, and then
   starts the native backend and broker.
1. Recovery of recorded host effects completes. R reports application readiness.
   Only then does N publish public admin listeners and report the service ready.

Startup snapshot parsing and restore occur in R. Config updates, autosave, and
snapshot restore use C when C exists. The fixed writer uses content revisions,
held directory identity, durable replacement, and retained mutation results. A
live restore has a journal and rollback path in R. Host network effects have a
separate broker-private recovery journal in N.

If a required role exits, an IPC identity is lost, recovery is incomplete, or
the admin relay fails, N cancels the process set. Shutdown is bounded. N closes
resources, waits for native exit observation, and kills a role at its platform
boundary when graceful shutdown does not complete.

The user profile launches R and C as direct children. N pins each lifetime with
a pidfd, binds IPC peer credentials to that child, and uses pidfd-based
termination and exit observation. The native child installs a parent-death
signal after its capability transition. N creates children on a dedicated locked
OS thread and keeps that thread alive until its children exit. Cleanup does not
remove staging state until child exit is proved.

## Linux implementation

Linux/amd64 has two versioned profiles. N remains the trusted native backend and
keeps the authority supplied by the caller. R and C always remove their
effective, permitted, inheritable, and ambient capability sets before their Go
program starts. N creates native TUNs, sockets, routes, firewall state, DNS
changes, matchers, and address reservations and records recoverable host
effects.

| Property            | `linux-full-v1`                                      | `linux-user-v1`                                      |
| ------------------- | ---------------------------------------------------- | ---------------------------------------------------- |
| Selection           | `strict`, or root `auto` when all prerequisites pass | Non-root `auto` when the full profile is unavailable |
| Role identities     | Separate provisioned accounts                        | Caller UID and GID in separate Landlock domains      |
| Filesystem boundary | Private mount namespace and Landlock                 | Mandatory Landlock ABI 3 or newer                    |
| Syscall boundary    | Full role filters with final thread seal             | Same-UID role filters with final thread seal         |
| Role cgroups        | Mandatory                                            | Optional complete extension                          |
| Private namespaces  | Mandatory                                            | Not created                                          |
| Supervision         | Namespace-aware full-profile supervision             | Direct-child pidfds and parent-death signal          |
| Config writer       | Supported                                            | Supported with a fixed private target                |
| Command helper      | Supported                                            | Not supported                                        |

### Full profile

R, C, and H use the separate non-root accounts `almagest-runtime`,
`almagest-writer`, and `almagest-command`. N opens each executable and directory
before launch. The native launcher checks held objects, accepts no path or
environment arguments, enters the role cgroup at process creation, changes
identity, sets resource limits, creates a private mount namespace, binds only
approved objects, and applies `no_new_privs`, Landlock, and a role-specific
seccomp filter. It synchronizes all Go threads before the final restriction
step.

Each role has cgroup v2 CPU, memory, and task limits. The default ceilings are 8
GiB memory, 4096 tasks, and 64 CPUs of quota. File descriptors default to 65536
per process, and file size defaults to 1 TiB. R can access only its private
storage and IPC objects. C can access only its writer directory and one target
name. H sees its state directory and one held executable at a fixed path. A
failure in namespaces, cgroup placement, Landlock, seccomp, identity, object
validation, or thread synchronization stops launch.

### User profile

`linux-user-v1` does not require root, `CAP_SYS_ADMIN`, provisioned role
accounts, private mount namespaces, or delegated cgroups. Landlock, seccomp,
`no_new_privs`, an exact descriptor allowlist, empty active capability sets, and
applied-profile verification are mandatory. Landlock and seccomp failure stops
startup; they are not optional best-effort mechanisms.

The same-UID seccomp policy denies native socket creation, process control,
arbitrary execution, namespace entry, native network interfaces, unsafe
descriptor operations, and unreviewed syscall families. It permits the measured
Go runtime thread behavior and the inherited private IPC lanes. The role applies
the final filter with TSYNC before it reads application input. Post-exec probes
verify that a forbidden file read, native internet socket, and dumpability
restoration are denied.

N validates each installed role executable through a held descriptor. For each
launch, it copies the executable into a fresh bounded file in a private code
directory outside all writable role trees. It verifies type, ownership, mode,
link count, size, and SHA-256 content identity before it accepts the role's
readiness record. A role can read and execute only its own disposable copy. N
removes identity-matching staging objects only after it proves child exit.

R receives Landlock access only to its runtime data and private temporary tree.
C receives access only to its config directory and fixed target. Mode 0700 is
not treated as a same-UID boundary: separate Landlock domains, syscall filters,
and the descriptor contract provide that boundary. Bootstrap rejects symlinks,
special files, hard-linked regular files, nested mounts, unsafe ownership or
modes, path overlap, bind-mount aliases, and identity changes during validation.

The broker can retain an ambient `CAP_NET_ADMIN` grant from a service manager,
but the profile does not require it. Network feature availability comes from
typed backend reports and real operation results, not from the profile name or a
capability bit alone. The daemon does not add capabilities or use file
capabilities to improve the selected profile.

Without the optional cgroup extension, inherited limits constrain the process
set but do not provide per-role CPU, memory, or task isolation. Without mount
and PID namespaces, some host metadata remains visible. These limits make the
profile reduced relative to `linux-full-v1`; they do not make its mandatory
Landlock and seccomp boundary unverified.

### Status and warnings

Before application readiness, the trusted broker writes a bounded selection
summary and one warning for each omitted optional protection. After it verifies
role records and application readiness, it writes the final effective summary.
Warnings include `role`, `reason`, `effect`, and `feature`; required diagnostics
go to broker stderr even when application log filtering would hide them.

N records status schema version 1, requested policy, profile ID and revision,
verification state, reduced state, plan digest, observation time, per-role
mechanisms, credentials, capability sets, resource limits, and bounded warnings.
Each warning includes a role, stable reason code, practical effect, and example
affected feature. Resource records distinguish requested and effective values,
scope, source, and applied versus observed enforcement.

`verification_state=verified` means the selected profile's mandatory checks
passed. It can coexist with `reduced=true` and unavailable optional features.
The admin `hello` response and panel show this record instead of treating all
isolated processes as equivalent. The record is verified launch-time state, not
a live kernel attestation, and can become stale while the process set is
shutting down. The trusted broker's startup log remains the authoritative launch
record.

## Windows implementation

Windows has implemented isolation components, but no released complete daemon
composition. The production daemon therefore remains in direct mode.

The implemented component profile is limited to Windows Server 2022 build 20348
on amd64. It can provision persistent, distinct AppContainer identities and
private directories for R, C, and H. Those roles use restricted tokens, Low
integrity, no network capability, exact inherited connected pipes, suspended
launch, process mitigations, and one-process kill-on-close Job Objects. Trusted
N retains its caller or service token and uses a bounded Job Object because it
owns native networking and approved backend helpers.

Windows path code rejects relative, device, UNC, alternate-stream, reparse,
hard-link, short-name, and unsafe ACL cases. It holds checked path components
through launch. Role environments are constructed from a small fixed set and do
not inherit `PATH` or loader controls. The common broker, protocol, relay,
writer, supervision, and recovery code has Windows implementations and native
component tests.

The missing release work is the installed service bootstrap, end-to-end role
composition around the daemon runtime and admin gate, deployment lifecycle,
complete hostile-runtime qualification, and atomic packaging. Component tests
must not be interpreted as a claim that Windows daemon isolation is active.

## Packaging and migration

Run `just build-isolation-linux` to build the six-file Linux payload. Install
all files as root-owned, non-writable files in one directory, normally
`/usr/libexec/almagest`. Package updates must replace the complete set while the
service is stopped. A mixed or partial set fails validation.

Install the systemd units from `packaging/linux`: the service file, sysusers
file, and tmpfiles file. Run the normal distribution sysusers and tmpfiles steps
before first start. Do not add setuid bits or file capabilities to role
binaries. The reference service starts with `--isolation=auto`, delegates cgroup
controllers to N, and uses an explicit capability bounding set. Because the
reference service starts as root and provisions the role accounts, `auto`
selects `linux-full-v1` or fails.

An ordinary user can start the same payload directly with `auto`. For a per-user
installation, user-profile executables can be owned by root or by the launch
UID, but their ancestors and files must not be group- or other-writable; set-ID
bits, file capabilities, and hard-link aliases are rejected. The user profile
does not run the privileged storage migration helper or change ownership of an
existing deployment.

The pre-start storage helper creates the new role-owned layout. If the legacy
`/var/lib/almagest/store` exists, it moves it to runtime storage, retains the
original as `store.pre-isolation`, writes an identity-bound migration receipt,
and copies the regular config to the writer-owned directory. Protected config
stays root-owned under `/etc/almagest`.

For an interrupted migration, keep the service stopped and run
`almagest-isolation-storage recover`. Rollback requires a clean broker journal
and proved storage identity; then run `almagest-isolation-storage rollback`
before starting direct mode. See `packaging/linux/README.md` for the operator
commands.

## Code map

- `bin/almagestd/isolated_runtime.go` adapts R services to the normal daemon.
- `bin/cmd/almagest-network-role` is Linux production bootstrap and N.
- `bin/cmd/almagest-config-writer-role` and `bin/cmd/almagest-command-role` are
  C and H entry points.
- `bin/cmd/almagest-isolation-storage` owns Linux deployment migration.
- `bin/native/linux-role` is the native Linux launcher.
- `bin/internal/isolation/bootstrap` loads trusted input and prepares storage.
- `bin/internal/isolation/launchconfig` converts protected config to fixed
  broker inputs.
- `bin/internal/isolation/selection` resolves public policy and selects a
  versioned profile from preflight results.
- `bin/internal/isolation/protocol`, `transport`, and `relay` define IPC.
- `bin/internal/isolation/network` owns network resources and typed dispatch.
- `bin/internal/isolation/runtime` adapts IPC resources to runtime interfaces.
- `bin/internal/isolation/admingate`, `configwriter`, `commandhelper`, and
  `diagnostics` implement the other ownership boundaries.
- `bin/internal/isolation/linuxprofile` and `windowsprofile` implement native
  restriction profiles.
- `bin/internal/isolation/networkrole`, `roleentry`, and `supervision` compose
  and supervise roles. `roleentry` also validates the bounded public status and
  versioned applied-profile records.
- `bin/almagestd/daemonservices` contains the process-neutral service interfaces
  and direct-mode implementations.

Isolation stays in `bin/`. The reusable `lib/` module does not depend on role,
IPC, daemon config, or platform-launch code.

## Security evidence and limits of claims

Tests prove behavior only on the platform and configuration where they run. They
do not prove freedom from all sandbox escapes or kernel defects.

The normal Go tests and fuzz tests check strict decoding, duplicate and unknown
field rejection, frame and queue limits, session/role/resource identity,
operation receipts, stale generations, cancellation, relay flow control, config
target replacement, command allowlists, admin peer gates, storage path
validation, recovery journals, and supervision. Import-boundary tests keep
trusted bootstrap free of daemon runtime and node packages and keep direct mode
free of isolation IPC.

`just linux-isolation-profiles` runs the production Linux composition in the
privileged E2E harness. Its native tests execute every role through the shipped
launcher and check unsafe object rejection, alternate ABI rejection, seccomp
thread synchronization, namespaces, capabilities, Landlock, cgroups, private
storage, inherited descriptors, fixed config writing, fixed command execution,
native resource ownership, cleanup, recovery, and startup readiness. The E2E
case then starts the packaged process set with resource-limit environment
overrides and checks the effective role cgroups, process limits, and public
isolation status. The selected native DNS and process-routing E2E cases run
through the same production composition.

The Docker `linux_adaptive_isolation` case runs `linux-user-v1` as an ordinary
user with no capabilities and, when supported by the container tools, with only
ambient `CAP_NET_ADMIN`. It checks default and explicit policy selection, strict
failure, explicit direct mode, private socket selection, reduced-profile
warnings and status, role credentials and capability sets, mandatory mechanisms,
config update, snapshot creation and live restore, persistence after restart,
and fail-closed invalid inputs. Manual mechanism probes under
`e2e/experiments/linux_adaptive_isolation` retain focused Landlock, seccomp,
same-UID process-control, executable-copy, cgroup, and capability evidence.

`just windows-isolation-profiles` runs the Windows component profile as both a
standard caller and Local System in the locked disposable VM. These tests check
AppContainer and token identity, Low integrity, job limits, exact handle
inheritance, path and ACL attacks, child and native-network denial in R/C/H,
trusted N network access, native resource cleanup, persistent deployment
open/remove, and fail-closed supervision. This proves only the component profile
on Windows Server 2022 build 20348/amd64; it does not prove an active Windows
isolated daemon.

`just check` runs portable tests, static checks, the configured Docker E2E
suite, and optional WinVM gates. Optional native suites that are skipped do not
prove their platform claims. The admin `hello` response reports the active
execution mode, protocol and status versions, selected profile, verification and
reduced state, role mechanisms and credentials, capability sets, effective
resource limits, warnings, and recovery state. Tests and operators can
distinguish the full profile, reduced user profile, and direct mode.

## Future work

- Complete and package Windows service isolation, then enable it only after a
  full production-composition and hostile-runtime gate passes.
- Qualify additional Windows releases and arm64, and additional Linux
  architectures, as separate profiles.
- Qualify older Landlock versions only through a new reviewed profile. The
  current user profile requires ABI 3 or newer.
- Extend user-profile qualification for delegated per-role cgroups, snapshot
  import and interrupted restore rollback, and broader syscall, alias, and
  lifecycle fault variants.
- Add a same-UID command-helper profile only after fixed-executable, signal,
  cancellation, and descendant-lifetime behavior has a complete boundary and E2E
  qualification. Do not run unsupported commands in R or N.
- Improve relay throughput and allocation behavior. Any new shared-memory or
  direct-handle path needs a new authority, lifetime, revocation, overload, and
  cleanup review; benchmark results alone are not sufficient.
- Connect reviewed protected-config restrictions that can reduce the default
  network operation set and resource counts. Regular config must never expand
  them.
- Add packaging formats beyond the current systemd reference deployment.
