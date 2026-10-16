# Subprogram sandbox implementation status

The daemon has a protected, owner-bound subprogram service in direct mode on
Linux and Windows, and in Linux isolated mode.
`XTorDriver` does not use this service. It remains an experimental node that
uses `github.com/asciimoth/tor-driver` directly.

Implemented parts of `SUBPROGRAM_SANDBOX_DESIGN.md` include:

- platform-neutral owner, sandbox, command, file, socket, status, and typed
  launch contracts in `lib/sandbox`;
- a `gonnect.Network` adapter that permits supported sandbox loopback calls and
  rejects DNS, raw, multicast, and interface operations;
- node-bound access through `lib/nodekit`, engine injection, durable owner
  metadata, epoch fencing, synchronous revocation, and coordinated public node
  rename;
- protected program and template policy, finite defaults, argument builders,
  environment rules, path overlap checks, feature planning, and schemas;
- a durable owner registry with an exclusive cross-process lock, atomic
  replacement, rename receipts, rollback after failed persistence, orphan
  records, and per-owner quarantine after an unclean active session;
- bounded owner and instance management, idempotent acquisition, stale
  generation checks, cancellation cleanup, and terminal cleanup failures;
- direct `off` and partial `auto` execution with explicit program selection,
  service-wide and per-instance command limits, process-group or Job Object
  bookkeeping, stable command results, bounded output helpers, confined file
  operations, bounded transfers and temporary storage, loopback TCP and UDP,
  and tracked cleanup;
- a qualified Windows Server 2022 amd64 `windows-native-v1` strict profile with
  a restricted Low-integrity primary token, disabled administrative groups,
  removed privileges, a stable owner-and-slot restricting SID, protected
  writable-root DACLs and labels, exact inherited-handle lists, suspended
  launch, nested no-breakaway Job Objects, descendant cleanup, and enforced
  process, aggregate memory, and CPU limits;
- a qualified Linux amd64 `linux-full-v1` profile with a static native
  launcher, a persistent PID 1 worker, private mount, network, PID, IPC, UTS,
  and cgroup namespaces, an exact mount-root view, private proc and device
  views, loopback-only sockets, per-instance and per-command cgroups, Landlock,
  seccomp, identity removal, rlimits, cgroup limits, and exec-status handshakes;
- a partial `linux-landlock-v1` auto profile for ordinary-user deployments
  that cannot create the full native boundary;
- owner and slot persistent storage identity, protected retention or deletion,
  and preservation on normal daemon shutdown;
- bounded preflight status in the admin `hello` response; and
- a supervised Linux isolated-daemon role with policy bootstrap, exact
  runtime peer binding, separate bounded control and data lanes, full sandbox
  API proxies, cancellation, and fail-closed process-set shutdown;
- Docker tests as root and an ordinary user, including the full daemon
  isolation `off|auto|strict` by sandbox `off|auto|strict` matrix, plus Windows
  VM cases for portable, policy, direct-backend, durable-service, native strict,
  and direct-daemon behavior under ordinary-user, elevated-administrator, and
  LocalSystem launch identities. The Windows daemon test covers sandbox
  `off|auto|strict`; Windows daemon isolation remains unavailable.

The direct backend reports only protections supported by its selected path.
The portable `direct-v1` auto profile reports explicit environment construction
and Go handle inheritance control as applied. The partial Landlock profile also
reports filesystem access restriction and writable storage as applied. The
full profile reports every Linux strict feature only after the cgroup controller
probe and native namespace probe succeed. It uses the protected
`LinuxCgroupRoot` delegation when configured. Its temporary filesystem is a
quota-limited tmpfs; process, memory, CPU, descriptor, and task
limits are also enforced in the native boundary. In `off`, all protection
features report `off`, and the child receives the normal host environment.

The Windows native profile reports privilege removal, environment construction,
handle inheritance, private writable storage, process isolation, process-tree
containment, and resource limits as applied. It reports filesystem access,
an exact filesystem view, and private networking as Windows native v1 OS
exclusions. Thus, Windows strict can be satisfied while `full_boundary` is
false. The child uses host networking, and it can read host objects that its
restricted token can access. Templates that explicitly require an excluded
feature fail before launch.

Docker qualification covers strict root-to-non-root launch, mapped read-only
inputs, exact filesystem denial, selected credentials, private TCP and Unix
sockets, IPv4 and IPv6 loopback, UDP creation, host TCP and abstract-Unix
isolation, namespace denial, mount and process-control syscall denial,
unapproved socket-family denial, controller signal denial with signal-zero
existence checks, temporary storage, selected-descendant
execution, and termination of a descendant that creates a new session. The
same suite checks the partial profile as an ordinary user.

Limits listed in `Templates.*.Limits.Required` require the
`resource-limits` feature. A policy that requires a native limit fails
preflight while the selected backend cannot enforce the complete resource
limit group. Persistent storage has no byte-quota field or implied per-file
limit. Aggregate persistent quotas are a possible future improvement that
needs filesystem or volume support.

Linux `strict` succeeds only on a qualified amd64 host with a usable delegated
cgroup v2 subtree, the required capabilities, Landlock ABI 3, recursive mount
attributes, namespace support, and the approved native launcher. A missing
prerequisite fails service preflight. Windows `strict` succeeds with
`windows-native-v1` only on the qualified Windows Server 2022 build 20348 amd64
platform. A missing native prerequisite fails service preflight. The Windows
isolated supervisor role is not implemented. Pathname Unix
sockets below writable roots are available in full and partial Linux profiles;
the partial profile uses the host Unix socket namespace. Read-only
source directories must contain only regular files and directories that are
not writable by the group or other users. Symbolic links, devices, FIFOs,
sockets, and hard-link aliases are
rejected. An active owner found after an unclean daemon stop is fenced
while unrelated owners remain usable. The current recovery state cannot prove
that escaped descendants are dead, so a quarantined owner cannot be reused.
These limits prevent the implementation from claiming confinement that it does
not provide.

## Remaining Windows work

The following Windows work is outside the implemented native direct profile:

- Windows daemon isolation has no sandbox supervisor role. A Windows daemon
  that uses `windows-native-v1` must run in explicit direct mode.
- Exact filesystem confinement and a private daemon-only network context are
  native v1 OS exclusions. A container, silo, VM, or separately qualified WFP
  design is required to remove these exclusions and report a full boundary.
- Mapped `ReadOnly` targets need an exact filesystem-view backend. The native
  profile rejects them instead of exposing the host source under a false target.
- A child-process file-handle count and aggregate temporary-storage byte limit
  are not enforceable by the selected Job Object and NTFS mechanisms. Templates
  that list `Files` or `TemporaryBytes` in `Limits.Required` fail preflight.
  Broker API file counts and transfer bounds still apply.
- The Windows `Tasks` limit is the Job Object active-process limit. The native
  profile has no aggregate thread-count ceiling. Linux `Tasks` continues to
  count processes and threads through cgroup `pids.max`.
- The qualified native matrix is Windows Server 2022 build 20348 on amd64.
  Other Windows releases and architectures need separate hostile-child and
  resource-limit qualification before the profile can report strict success.
- Windows soft process signals remain unsupported. Portable terminate and kill
  behavior uses complete Job Object termination.
- `XTorDriver` still uses `tor-driver` directly. Its sandbox adapter and all
  direct and isolated node qualification remain deferred by the current scope.

## Remaining Linux work

The following Linux work is not implemented or is not fully qualified:

- The ordinary-user profile is partial. It applies Landlock and
  `no_new_privs`, but it has no private mount, PID, IPC, UTS, network, or cgroup
  namespace boundary. A rootless strict profile needs a qualified user
  namespace and cgroup design.
- `Templates.*.Linux.IdentityPool` is reserved for a future provisioned-account
  allocator. Current Linux templates use an explicit nonzero UID and GID or
  `CurrentUser` for an ordinary-user partial profile. A configured pool fails
  preflight.
- The full native profile is qualified only on Linux amd64. Other Linux
  architectures need reviewed syscall policies, native builds, and hostile
  process qualification. They are future improvements, not current backend
  requirements.
- Read-only inputs are held and checked before launch, but they are live bind
  mounts. A trusted host process can change a source after validation. Immutable
  staging or a qualified snapshot mechanism remains future work for inputs that
  can change while a sandbox runs.
- Qualification does not yet cover every environment in the design matrix.
  Missing cases include capability-bearing callers, restricted or nested
  containers, nested read-only mounts, every launch-cancellation point,
  descendants that retain output pipes, forced worker death, and restart
  identity reuse after proven process-tree exit.
- The isolated Linux Docker cases qualify supervisor startup, peer-bound IPC,
  role cgroups, limits, status, shutdown, and an end-to-end graph-node process
  launch through the isolated proxy in all daemon-isolation and sandbox-mode
  combinations.
- Durable recovery quarantines an owner after an unclean active session because
  it cannot prove that escaped descendants are dead. Recovery that can prove
  process-tree death and safely reuse the owner remains future work.
- `XTorDriver` still uses `tor-driver` directly. Its sandbox adapter, launch
  builder, controller-PID flow, and direct and isolated qualification remain
  deferred by the current project scope.

Persistent byte quotas are also deferred. Adding them later requires aggregate
enforcement for direct child writes, such as filesystem project quotas or a
dedicated limited volume. `RLIMIT_FSIZE` cannot provide that contract. The
current API continues to bound transfer size, open descriptors, and temporary
bytes.

The partial Landlock profile has no private mount view. Thus, a read-only
`Target` must equal its `Source` when that profile is selected. The full profile
mounts the held source at the configured sandbox target.

The error-returning `Engine.RenameNode` operation commits the durable owner
rebind before it publishes the graph name. Legacy locked mutations and Pasta
history operations still rely on the workspace observer. If their owner update
fails, the coordinator becomes terminal and refuses later sandbox access and
config saves instead of writing mismatched identity metadata. A future Pasta
transaction hook is required for atomic rollback on every internal history
path.

Sandbox persistent roots remain external to graph snapshots. Snapshot restore
does not roll them back.
