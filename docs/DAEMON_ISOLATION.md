# Daemon process isolation

This document describes the current daemon isolation design. It is an
implementation reference, not a record of the experiments or delivery steps
that produced the design.

## Status and scope

Packaged Linux/amd64 uses the isolated process set by default. Windows uses the
direct, single-process daemon by default because its complete service startup
and packaging are not implemented. Other targets select isolation and fail
closed when no qualified profile exists. Use `--no-isolation` to select direct
mode explicitly. `--experimental` requires direct mode on an
isolation-default platform.

Isolation limits damage from a compromised graph runtime. It keeps native host
network objects, public listener ownership, protected launch authority, config
file replacement, and configured process creation outside that runtime. It
does not make the trusted broker harmless. The broker starts with the service
credential and owns all approved host effects. Isolation also does not protect
against a compromised kernel, backend library, installed role binary, package
manager, or root administrator.

The design is fail-closed. A missing binary, invalid trusted path, unavailable
kernel feature, failed restriction, lost role, uncertain host effect, or
protocol mismatch stops startup or the current process set. It does not cause
an automatic direct-mode fallback.

## Processes and binaries

The process names below are also used in code comments: N is the network
broker, R is the runtime, C is the config writer, and H is the command helper.

| Role | Linux binary | Responsibility | Authority |
| --- | --- | --- | --- |
| N: network broker and supervisor | `almagest-network-role` | Trusted bootstrap, native network backend, public admin listeners, policy, role supervision, recovery journal, and trusted diagnostics | Service credential and native network authority |
| R: graph runtime | `almagestd` | Config parsing after bootstrap, engine and nodes, admin protocol and panel, snapshots, application DNS, and storage data | Private runtime storage and fixed IPC lanes only |
| C: config writer | `almagest-config-writer-role` | Revision-checked durable replacement of one regular config file | One fixed writer directory and target name |
| H: command helper | `almagest-command-role` | Approved node commands and native-backend callbacks | One held executable and fixed protected profiles |

`almagestd` is the installed entry point. On Linux/amd64 it opens the sibling
N binary by a trusted file descriptor and replaces itself with N before it
reads config. N then starts R and starts C and H only when their services are
needed. `almagest-linux-role-launcher` is a small native launcher used for R,
C, and H. It is not a resident role and must not be setuid. The
`almagest-isolation-storage` program runs before the service to prepare or
migrate the storage layout.

Direct mode uses the same daemon behavior without the internal roles or IPC.
It remains a supported explicit compatibility and development mode.

## Trust and ownership rules

Protected config and command-line listener overrides are launch authority. N
reads them before R starts. N resolves secret environment variables and
certificate files, selects the native backend, opens trusted executables and
directories, and creates all IPC endpoints. R receives only literal secret and
certificate values plus the small protected subset that it must use. It does
not receive source paths, environment variable names, listener addresses,
peer filters, system configuration, command profiles, or unknown protected
fields.

Regular config is untrusted application input. It can select graph behavior,
but it cannot select a role, executable, host path, native API, credential,
IPC endpoint, or isolation policy. Native objects stay in N and are referred
to by session-bound opaque IDs. C owns the mutable regular config target. H
owns process creation. R owns the engine, snapshots, databases, and runtime
files.

N owns public web, Unix-socket, and named-pipe listeners. It applies the
configured peer gate and relays accepted byte streams to R. R continues to own
HTTP, WebSocket, admin API, browser-origin, and panel behavior. The relay does
not give R a public listener handle or a client impersonation token.

## IPC contract

Protocol version 1 uses private connected byte streams that trusted bootstrap
binds to specific roles. A session contains a random ID, epoch, protocol
version, and digest of normalized protected policy. Peers must agree on that
complete record. Each request also carries the session, graph epoch, request
ID, and, for mutations, an operation ID. Resource references include the
session, type, generation, and graph epoch.

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
can use acknowledged bounded chunks, but a transfer is not permission to
write a file.

The interface does not permit arbitrary paths, programs, arguments, handles,
file descriptors, environment names, syscalls, `ioctl`, socket options,
registry operations, driver calls, native error text, or generic host
callbacks. Unknown operations and fields are rejected. Runtime requests cannot
expand protected authority. A canceled or timed-out mutation is not assumed to
have had no effect; operation receipts and the recovery journal resolve known
and uncertain outcomes.

Control messages are strict length-prefixed JSON with bounded depth, size,
in-flight work, arrays, strings, resources, and receipts. Control, data, and
diagnostic traffic use separate lanes so data or logging pressure cannot take
the control lock. TUN, TCP, UDP, DNS, and accepted-connection data use bounded
relay frames with typed packet metadata, explicit read/write grants, sequence
numbers, flow control, deadlines, half-close, accept/claim/reject, and
generation checks. No native data handle crosses the isolation boundary.

Diagnostics are bounded structured records. N binds each lane to its process
origin, sanitizes control characters, redacts known secrets again at receipt,
and reports dropped records. A full diagnostic queue drops diagnostics; it
does not block control or packet progress.

The exact limits and request fields are part of the code contract in
`bin/internal/isolation/protocol`, `transport`, `relay`, `diagnostics`, and
`admingate`. Keep detailed wire rules in those packages instead of duplicating
them here.

## Configuration and command line

Isolation adds one public switch:

- `--no-isolation` selects direct mode. On Windows it is currently an accepted
  no-op because direct mode is already the default.

It does not add an `Isolation` object to either config schema. Existing
protected sections provide authority:

- `Admin` selects public endpoints, peer filters, pprof, and browser-origin
  behavior. Isolated startup requires at least one fixed endpoint.
- `System` must select the native platform backend. The debug backend requires
  direct mode.
- `CommandRunner` is converted to fixed H profiles. The current Linux profile
  permits one shared absolute executable, direct argv execution, bounded slot
  substitution, and no per-command UID or GID selection. Configurations outside
  this profile must use direct mode.
- `Secrets` and `TLS` file or environment sources are resolved by bootstrap.
  R receives only their bounded literal values.

Normal CLI config, protected-config, storage, snapshot, logging, and admin
overrides still apply. In the production Linux layout, omitted paths resolve
to:

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

## Bootstrap and startup

Linux production startup has these stages:

1. `almagest-isolation-storage prepare` creates or validates the role-owned
   directories and performs a stopped legacy storage migration when required.
2. `almagestd` selects isolation, opens the sibling N binary without following
   an untrusted replacement, and executes that held object.
3. N parses CLI inputs, loads regular and protected config once, resolves
   protected sources, validates the storage layout, opens trusted role files,
   and opens the broker recovery journal.
4. N constructs immutable policy, admin endpoints, native backend settings,
   C/H profiles, separate role cgroups, and private IPC lanes.
5. N starts R through the native launcher. Bootstrap copies regular config,
   resolved protected values, and an optional startup snapshot into reserved
   files in R's private storage. R applies final restrictions before it reads
   its bootstrap record or builds the application.
6. N starts optional C and H, waits for each role to prove that final
   restrictions are active, and then starts the native backend and broker.
7. Recovery of recorded host effects completes. R reports application
   readiness. Only then does N publish public admin listeners and report the
   service ready.

Startup snapshot parsing and restore occur in R. Config updates, autosave, and
snapshot restore use C when C exists. The fixed writer uses content revisions,
held directory identity, durable replacement, and retained mutation results.
A live restore has a journal and rollback path in R. Host network effects have
a separate broker-private recovery journal in N.

If a required role exits, an IPC identity is lost, recovery is incomplete, or
the admin relay fails, N cancels the process set. Shutdown is bounded. N closes
resources, waits for native exit observation, and kills a role at its platform
boundary when graceful shutdown does not complete.

## Linux implementation

The released profile is Linux/amd64. N remains the trusted native backend and
runs with the service credential. R, C, and H use the separate non-root
accounts `almagest-runtime`, `almagest-writer`, and `almagest-command`.

N opens each executable and directory before launch. The native launcher
checks the held objects, accepts no path or environment arguments, enters the
role cgroup at process creation, changes identity, sets resource limits,
creates a private mount namespace, binds only the approved held objects, and
applies `no_new_privs`, Landlock, and a role-specific seccomp filter. The
launcher synchronizes all Go threads before the final restriction step. A
failure in namespaces, cgroup placement, Landlock, seccomp, identity, object
validation, or thread synchronization stops launch.

Each role has a cgroup v2 CPU, memory, and process limit. The default ceilings
are 512 MiB memory, 128 tasks, and one CPU of quota. File descriptors and file
size also have fixed limits. R can access only its private storage root and IPC
objects. C can access only its writer directory and one target name. H sees its
state directory and one held executable at a fixed in-profile path. N creates
native TUNs, sockets, routes, firewall state, DNS changes, matchers, and address
reservations and records recoverable host effects.

Linux production packages require cgroup v2 delegation and the namespace,
Landlock, and seccomp facilities used by the launcher. The service does not
weaken the profile when one is absent.

## Windows implementation

Windows has implemented isolation components, but no released complete daemon
composition. The production daemon therefore remains in direct mode.

The implemented component profile is limited to Windows Server 2022 build
20348 on amd64. It can provision persistent, distinct AppContainer identities
and private directories for R, C, and H. Those roles use restricted tokens,
Low integrity, no network capability, exact inherited connected pipes,
suspended launch, process mitigations, and one-process kill-on-close Job
Objects. Trusted N retains its caller or service token and uses a bounded Job
Object because it owns native networking and approved backend helpers.

Windows path code rejects relative, device, UNC, alternate-stream, reparse,
hard-link, short-name, and unsafe ACL cases. It holds checked path components
through launch. Role environments are constructed from a small fixed set and
do not inherit `PATH` or loader controls. The common broker, protocol, relay,
writer, supervision, and recovery code has Windows implementations and native
component tests.

The missing release work is the installed service bootstrap, end-to-end role
composition around the daemon runtime and admin gate, deployment lifecycle,
complete hostile-runtime qualification, and atomic packaging. Component tests
must not be interpreted as a claim that Windows daemon isolation is active.

## Packaging and migration

Run `just build-isolation-linux` to build the six-file Linux payload. Install
all files as root-owned, non-writable files in one directory, normally
`/usr/libexec/almagest`. Package updates must replace the complete set while
the service is stopped. A mixed or partial set fails validation.

Install the systemd units from `packaging/linux`: the service file, sysusers
file, and tmpfiles file. Run the normal distribution sysusers and tmpfiles
steps before first start. Do not add setuid bits or file capabilities to role
binaries. The service delegates cgroup controllers to N and uses an explicit
capability bounding set.

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
- `bin/cmd/almagest-config-writer-role` and
  `bin/cmd/almagest-command-role` are C and H entry points.
- `bin/cmd/almagest-isolation-storage` owns Linux deployment migration.
- `bin/native/linux-role` is the native Linux launcher.
- `bin/internal/isolation/bootstrap` loads trusted input and prepares storage.
- `bin/internal/isolation/launchconfig` converts protected config to fixed
  broker inputs.
- `bin/internal/isolation/protocol`, `transport`, and `relay` define IPC.
- `bin/internal/isolation/network` owns network resources and typed dispatch.
- `bin/internal/isolation/runtime` adapts IPC resources to runtime interfaces.
- `bin/internal/isolation/admingate`, `configwriter`, `commandhelper`, and
  `diagnostics` implement the other ownership boundaries.
- `bin/internal/isolation/linuxprofile` and `windowsprofile` implement native
  restriction profiles.
- `bin/internal/isolation/networkrole`, `roleentry`, and `supervision` compose
  and supervise roles.
- `bin/almagestd/daemonservices` contains the process-neutral service
  interfaces and direct-mode implementations.

Isolation stays in `bin/`. The reusable `lib/` module does not depend on role,
IPC, daemon config, or platform-launch code.

## Security evidence and limits of claims

Tests prove behavior only on the platform and configuration where they run.
They do not prove freedom from all sandbox escapes or kernel defects.

The normal Go tests and fuzz tests check strict decoding, duplicate and unknown
field rejection, frame and queue limits, session/role/resource identity,
operation receipts, stale generations, cancellation, relay flow control,
config target replacement, command allowlists, admin peer gates, storage path
validation, recovery journals, and supervision. Import-boundary tests keep
trusted bootstrap free of daemon runtime and node packages and keep direct mode
free of isolation IPC.

`just linux-isolation-profiles` runs the production Linux composition in the
privileged E2E harness. Its native tests execute every role through the shipped
launcher and check unsafe object rejection, alternate ABI rejection, seccomp
thread synchronization, namespaces, capabilities, Landlock, cgroups, private
storage, inherited descriptors, fixed config writing, fixed command execution,
native resource ownership, cleanup, recovery, and startup readiness. The E2E
case then starts the packaged process set and checks the public isolation
status. The selected native DNS and process-routing E2E cases run through the
same production composition.

`just windows-isolation-profiles` runs the Windows component profile as both a
standard caller and Local System in the locked disposable VM. These tests
check AppContainer and token identity, Low integrity, job limits, exact handle
inheritance, path and ACL attacks, child and native-network denial in R/C/H,
trusted N network access, native resource cleanup, persistent deployment
open/remove, and fail-closed supervision. This proves only the component
profile on Windows Server 2022 build 20348/amd64; it does not prove an active
Windows isolated daemon.

`just check` runs portable tests, static checks, the configured Docker E2E
suite, and optional WinVM gates. Optional native suites that are skipped do not
prove their platform claims. The admin `hello` response reports the active
execution mode, protocol version, role state, effective restriction labels,
capabilities, and recovery state so tests and operators can distinguish an
isolated process set from direct mode.

## Future work

- Complete and package Windows service isolation, then enable it only after a
  full production-composition and hostile-runtime gate passes.
- Qualify additional Windows releases and arm64, and additional Linux
  architectures, as separate profiles.
- Improve relay throughput and allocation behavior. Any new shared-memory or
  direct-handle path needs a new authority, lifetime, revocation, overload,
  and cleanup review; benchmark results alone are not sufficient.
- Connect reviewed protected-config restrictions that can reduce the default
  network operation set and resource counts. Regular config must never expand
  them.
- Add packaging formats beyond the current systemd reference deployment.
