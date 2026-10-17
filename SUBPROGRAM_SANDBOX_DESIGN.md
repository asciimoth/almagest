# Subprogram sandbox design

Status: partially implemented. See `docs/SUBPROGRAM_SANDBOX_STATUS.md` for the
implemented scope and remaining work. `XTorDriver` migration is deferred.

## 1. Decisions and scope

Add a daemon-owned sandbox service, with the standalone capability interface
from `github.com/asciimoth/gobox`. Inject it with a new
`lib.WithSandboxes(...)` build option.
Nodes receive an owner-bound view, not the daemon's policy or host paths.
Per-node instances survive rename through rebinding of the same stable owner.

The service starts only protected, named program profiles in protected, named
sandbox templates. It owns process trees, writable storage, and local socket
operations. When network containment is applied, all external network traffic
must pass through application code
that the sandbox owner explicitly supplies. For Tor, this is the driver's
upstream proxy over its selected graph Network.

Sandbox mode and daemon isolation mode are independent. A direct daemon can
use strict subprogram sandboxes. An isolated daemon can use sandbox mode off;
this does not permit the runtime role to start programs itself.

Use a trusted sandbox supervisor, S, separate from the graph runtime R and
from the existing command helper H. A small per-sandbox worker W owns the
sandbox's local sockets and launch operations. In direct mode the policy
service can live in the daemon, but native launch and namespace work still
use separate processes. Never change the Go daemon's namespaces or credentials.

Per-node sandbox instances survive link changes, retries, and
reconstruction of the same logical node. Persistent storage also survives a
daemon restart. Processes, sockets, and temporary storage do not survive a
daemon restart or loss of the owning runtime session. This is an explicit
design assumption, not a promise of live process recovery.

### Threat model

Treat the child program, its descendants, and their output and files as
untrusted. The desired full boundary prevents them from acquiring the daemon's
credentials, host files, host sockets, control channels, or authority to create
other sandboxes. Applied guarantees follow the effective profile; OS exclusions
and auto omissions explicitly leave the corresponding exposure in place.
Sandbox peers in one instance can communicate and share its writable data;
they form one trust domain. Separate instances must not share those rights.

In isolated mode, also treat R and all request fields as untrusted. S checks
each operation against protected policy. A compromised R can use all sandbox
capabilities granted to that runtime session; node scoping is an application
lifetime boundary, not a security boundary between Go code in the same R.
Untrusted future plugins must run as separate sandboxed programs.

The trusted computing base includes bootstrap, S, W, native launch code, and
the OS. This design does not protect against their compromise, kernel faults,
or an administrator. It does not prevent exfiltration through an upstream
proxy that the owner intentionally provides. Proxy access is a deliberate
application capability, not direct host network access.

### Modes

| Mode | Required behavior |
| --- | --- |
| `off` | Apply no child sandbox restrictions. Use the launch authority's normal credentials, environment, filesystem, and network. Still enforce named program selection, API path bounds, ownership, and process bookkeeping. |
| `auto` | Probe and select the available, compatible protection groups before creating an instance. Apply all groups in the selected plan. Report every omission. |
| `strict` | Require every protection in the versioned strict profile for the target OS. Exclude only that profile's explicit OS-specific unsupported properties. If a required protection is unavailable in the current environment, reject creation or launch before third-party code runs. |

The desired full boundary includes removal of elevated authority, prevention of new
privileges, a confined filesystem, private temporary storage, the configured
persistent storage, an explicit environment, a daemon-only local network
context, protection of other processes and IPC, and containment of descendants.
Configured mandatory resource limits must also be enforced. Every profile used
by Tor must provide usable local Dial and Listen, not merely deny all sockets.
When private networking is excluded for an OS, those operations use host
loopback and status explicitly reports that exposure.

### OS-specific strict requirements

Strict means complete enforcement of the target OS's declared requirements;
it does not mean identical guarantees on all operating systems. Exclusions
are reviewed, versioned platform policy, never the result of a failed probe
on the current machine. Nodes, regular config, and launch retries cannot add
exclusions. An unsupported OS with no qualified profile still fails strict.

| Property | Linux strict | Native Windows strict v1 |
| --- | --- | --- |
| Remove elevated authority and prevent acquisition of new authority | Required | Required through qualified token, object, and descendant policy |
| Construct environment and close inherited authority | Required | Required |
| Private writable temporary and configured persistent storage; bounded Files API | Required | Required |
| Restrict all child filesystem access to the exact allowed view | Required | OS-profile exclusion; native token/ACL controls do not provide the exact view required here |
| Private daemon-only network context, including usable local sockets | Required | OS-profile exclusion for the native backend described in section 6; host loopback is used |
| Protect supervisor and other processes from child control; contain descendants | Required | Required using qualified Windows object/token/job controls |
| Explicitly required resource limits | Required | Required |

These Windows exclusions describe the supported native platform contract,
not a claim that no VM, container, or future Windows mechanism could provide
the missing boundary. A missing implementation of an otherwise required
Windows protection is not an OS exclusion: strict remains unavailable until
that protection is implemented and qualified. Linux does not inherit Windows
exclusions. Missing Linux namespace permissions or kernel features are local
prerequisite failures, so Linux strict still fails.

Each feature has one of `applied`, `omitted-auto`, `excluded-os`, or `off`, with a
stable reason and profile revision. Report OS exclusions before launch and in
admin status even when strict succeeds. Report `strict_satisfied` separately
from `full_boundary`; Windows native strict can have the former true and the
latter false. Auto applies compatible available protections even for features
outside the OS strict minimum. Strict also applies qualified available
extensions; once selected, their application failure is fatal.

Protected templates may strengthen the OS minimum through `RequiredFeatures`.
An explicitly required feature cannot be waived by an OS exclusion. Thus a
template that requires private networking fails on native Windows until that
feature is available, while the default Windows strict profile can run.

`auto` is intentionally different from daemon isolation's existing `auto`:
subprogram `auto` can run with only partial protection. An omitted privilege
drop can thus leave a privileged child. Report this as a prominent warning
before launch, including effective identity. Sites that cannot permit this
must select `strict`. Environment filtering and exact handle inheritance are
normally available even when native confinement is absent.

An unavailable prerequisite can cause an omission during preflight. An invalid
policy, unsafe path, failed application of a selected restriction, mismatched
verification record, or uncertain mutation is an error in every mode. Never
retry a failed launch with fewer protections. A plan is immutable for the
instance; restart is required to select a new plan.

## 2. Interface available to lib

Use the platform-neutral contracts from `github.com/asciimoth/gobox`. Put owner
lifetime handling in `lib/` and its node-facing view in `lib/nodekit`. Put all
OS implementation, protected policy parsing, and IPC in `bin/`. Do not import
daemon packages from `lib/`.

The following is an API overview. The canonical source is the `gobox` module.
Standard library imports are omitted for clarity.

```go
type TemplateID string
type ProgramID string
type Root uint8
const (
    Temporary Root = iota
    Persistent
)

// A relative path below a writable root; never a host path.
type Path struct {
    Root Root
    Rel  string
}

// Engine-only identity, assigned by the host integration.
type Owner struct {
    Workspace string
    Key       string // durable opaque identity; not a node name or numeric ID
    Epoch     uint64 // lifetime fence, including deletion and restore
    Class     string
    Name      string // mutable graph name; never used as storage identity
    NameRevision uint64 // independent of the lifetime epoch
}

// Injected at engine construction. Nodes never receive this object.
type Service interface {
    BindOwner(context.Context, Owner) (OwnerSandboxes, error)
    RebindOwner(context.Context, OwnerRename) (Owner, error)
    ReleaseOwner(context.Context, Owner) error
    Close() error
}

type OwnerRename struct {
    Owner Owner // key, epoch, old name, and expected name revision
    NewName string
    OperationID string // idempotent across retries and IPC reconnects
}

type OwnerSandboxes interface {
    // Idempotent for (owner, slot, template). Slot is a bounded logical name.
    Acquire(context.Context, string, TemplateID) (Sandbox, error)
    Remove(context.Context, string) error
}

type Sandbox interface {
    Status() Status
    FS(Root) (Files, error)
    // Returns a path in the child's view; it grants no host filesystem access.
    ChildPath(Path) (string, error)
    ProgramPath(ProgramID) (string, error)
    ProcessContext() ProcessContext
    NewCommand(ProgramID, Launch) (Command, error)
    DialContext(context.Context, string, string) (net.Conn, error)
    Listen(context.Context, string, string) (net.Listener, error)
    ListenPacket(context.Context, string, string) (net.PacketConn, error)
    DialUnix(context.Context, Path) (net.Conn, error)
    ListenUnix(context.Context, Path) (net.Listener, error)
    Done() <-chan struct{}
    Err() error
    Close() error
}

type ProcessContext struct {
    Platform string
    ControllerPID int // valid in the child's PID view; not a host process handle
}

type Launch struct {
    // Typed values for slots defined by the selected program profile.
    // No executable path, raw argv, credentials, or SysProcAttr.
    Inputs map[string]Value
    Directory Path
}

type Value struct { // tagged union; exactly the selected member is valid
    Kind string // text, integer, boolean, path, program, endpoint
    Text string
    Int  int64
    Bool bool
    Path Path
    Program ProgramID
    Endpoint string
}

type Command interface {
    // Pipes can be requested once each, before Start.
    StdinPipe() (io.WriteCloser, error)
    StdoutPipe() (io.ReadCloser, error)
    StderrPipe() (io.ReadCloser, error)
    Start(context.Context) error
    Wait() (Exit, error)
    Signal(Signal) error // fixed portable set: interrupt, terminate, kill
    Kill() error        // kills the command tree, not just its root PID
    Done() <-chan struct{}
}

type Exit struct {
    Code int
    Signal Signal
    Killed bool
}

type Files interface {
    MkdirAll(context.Context, string, fs.FileMode) error
    TempDir(context.Context, string, string) (string, error)
    Open(context.Context, string, OpenOptions) (File, error)
    ReadFile(context.Context, string, int64) ([]byte, error)
    WriteFile(context.Context, string, []byte, fs.FileMode) error
    Rename(context.Context, string, string) error
    Stat(context.Context, string) (fs.FileInfo, error)
    ReadDir(context.Context, string, int) ([]fs.DirEntry, error)
    Remove(context.Context, string) error
    RemoveAll(context.Context, string) error
}

type File interface {
    io.Reader
    io.Writer
    io.Seeker
    io.Closer
    Stat() (fs.FileInfo, error)
    Sync() error
}
```

`Status`, `OpenOptions`, and `Signal` are closed portable types. Status reports
the effective plan, feature results, warnings, and terminal state. It cannot
change them. Open options allow only read, write, create, exclusive, truncate,
and append, with validated combinations. Files do not expose native handles,
`Fd`, `SyscallConn`, ownership changes, arbitrary ioctl, or host paths.

Provide convenience functions for Run, bounded Output/CombinedOutput, and
copying stdin/stdout/stderr. These use the above contract; they do not return
`exec.Cmd` or `os.Process`. Preserve familiar command behavior where possible:

- Start is single-use and returns only after setup and exec success are known.
  It does not imply application readiness.
- The Start context controls process lifetime after launch, as with
  `exec.CommandContext`. Cancellation kills the owned tree and is recorded.
  The Tor adapter must use its own lifetime context, because Tor passes a
  startup timeout to `Processes.Start` that ends after startup.
- Wait has one stable result and can be called again. A nonzero exit has a
  typed exit error. It observes termination and bounded pipe shutdown.
- Signals use tracked process identity, never a caller-supplied PID. Unsupported
  soft signals return `ErrUnsupported`; Kill must remain available.
- Unrequested stdin is closed. Unrequested output is discarded or sent to a
  bounded configured log sink. Requested pipes have bounded buffers and normal
  backpressure; a caller must drain both output streams. Cleanup cannot wait
  forever for a reader or a descendant that holds a pipe open.
- Command cancellation does not destroy a reusable sandbox. Sandbox close
  cancels all commands, pending calls, sockets, and file streams.

Errors distinguish policy denial, unsupported operation, unavailable feature,
closed owner, stale generation, quota, and launch failure. Include only bounded,
sanitized native details. A nil service means `ErrUnavailable`, never an implicit
host `exec.Command` fallback.

### Program authority and arguments

A program profile binds an ID to a held executable and an argument builder.
Builders are versioned daemon code, with typed input schemas. A fixed-argv
builder can substitute bounded validated slots, without shell interpretation.
Do not provide a generic raw argv profile to untrusted nodes.

The `tor-v1` builder accepts a driver working directory and reviewed Tor
settings. It validates the two config paths and generates or validates the
complete Tor configuration at launch. A path slot alone is insufficient:
otherwise a writable torrc could request arbitrary managed executables.
Security-sensitive launch configuration is sealed for launch in daemon-owned
read-only storage. Runtime files such as the cookie and control-port file
remain writable by Tor. The Tor adapter redirects its generated config writes
to a bounded staging operation, then the daemon validator commits the sealed
version. Never validate a writable file and later execute using its mutable
contents.

For v0.1.2 compatibility, the adapter can use ordinary Files.WriteFile for the
two staging files. Start passes their typed Path references to `tor-v1`. The
builder reads bounded content into private memory, parses a closed set of Tor
directives, validates every file/program/endpoint reference, and writes that
validated copy to the sealed launch area. It substitutes the sealed paths in
the final argv. The original staging files are never used by Tor for config.
The launch area is an explicit read-only profile input and is absent from the
node's writable Files API. Reject includes, unknown directives, and executable
options outside the approved transport set. The builder's accepted directive
set must track the pinned tor-driver version and have compatibility tests.

Templates list allowed descendants, such as Tor's managed `lyrebird` process.
All descendants inherit the same boundary and lifetime. Strict Linux profiles
grant execute rights only to approved images and their required loaders; data
mounts and writable trees are not executable. Read-only does not imply
executable. An interpreter or approved executable can still interpret input;
program allowlisting is not a claim to prevent arbitrary computation after a
child compromise. The confinement boundary must hold in that case.

### Filesystem and path contract

`ALMAGEST_SANDBOX_TEMP` identifies the temporary root in the child's view.
`ALMAGEST_SANDBOX_STATE` identifies persistent storage when configured; otherwise
it is absent. Set TMPDIR/TMP/TEMP to temporary storage. Set HOME and application
cache locations to approved writable subdirectories as required by the
template. These variables are reserved and cannot be overwritten by a node.

Each instance gets a new temporary directory; its data is removed only after
all processes and file handles are closed. Persistent storage is an exclusive
per-owner, per-slot subtree below a protected root. Another owner cannot select
it. Close preserves persistent data. Retention after owner deletion is a
protected policy choice, described below.

All Files names use a portable relative format. Reject absolute paths, `..`,
NULs, platform device names, alternate streams, and invalid separators. Empty
or `.` paths can identify a root for reading but cannot remove or rename it.
Mutations use directory handles and identity checks, not string-prefix tests.
The initial API rejects symlinks and reparse points in traversed components,
and rejects hard-link aliases for broker operations. Bound reads, directory
pages, transfers, open files, and total storage use. No device or FIFO creation
API is exposed. Rename stays within one root and uses safe replacement rules.
FileInfo and directory entries expose sanitized metadata only: Sys returns
nil, and no host paths, owner tokens, or native handles are included.

The child can create hostile files. Never follow them with the supervisor's
host authority. For Unix sockets use a worker in the same confined filesystem
view with an unprivileged identity; bound traversal and reject symlinks. Do
not resolve a socket path under a privileged host cwd and then connect after
the child can replace its parent. This remains true in mode off: the API is
still restricted even though the child itself is unrestricted.

`ChildPath` and `ProgramPath` are representation functions, not escape hatches.
The daemon accepts them back only after conversion to a known typed reference.
No interface grants nodes direct host-path access, even in direct mode.

### Local network contract

Dial and Listen accept numeric loopback TCP addresses only in version 1;
Listen supports port zero and returns the actual address. Packet operations
support loopback UDP if the selected template and backend support them.
Reject DNS names, wildcard addresses, non-loopback addresses, IPv4-mapped
bypasses, zone identifiers, and unapproved protocols. UDP sends validate every
destination, including WriteTo. Unix sockets use the separate Path API.

The address belongs to the sandbox's context. It must never be interpreted as
host loopback when network containment is reported as applied. Return normal
Go network interfaces with deadlines, half-close where supported, and bounded
backpressure. Provide a `gonnect.Network` adapter for the supported operations;
unsupported DNS/raw operations fail explicitly.

In mode off, and in auto when network containment is omitted, these operations
use host loopback. Status must say so: children can then reach unrelated host
listeners and the outside network. A Linux private namespace permits traffic
among programs in that sandbox and owner-created endpoints; it does not
identify an arbitrary destination by the literal PID of almagestd.

## 3. Node lifetime and persistent identity

Add an engine-owned registry keyed by workspace identity, durable node key,
and sandbox slot. Bind nodes through an owner-specific service at construction
or Init; do not let node code choose another owner's key. Add a durable opaque
node key to engine-managed graph metadata. A node name or recycled Pasta ID is
not an identity. Imported metadata is validated, and copy/paste generates new
keys. A caller cannot claim a storage directory by inserting a key into config;
the daemon's registry must authorize the binding.

| Event | Instance behavior | Persistent data |
| --- | --- | --- |
| Link detach/attach or outbound replacement | Keep instance; node decides whether its command restarts | Keep |
| Equivalent config apply | Reuse instance and healthy command | Keep |
| Node rename | Rebind the name to the same owner key; keep commands, sockets, handles, and instance generation | Keep at the same path |
| Same logical node reconstruction | Fence old node lease; transfer instance to new lease | Keep |
| Explicit Remove or Sandbox.Close | Stop tree and remove temporary resources; next Acquire creates a new instance | Keep |
| Node deletion | Revoke owner immediately; close every slot automatically | Retain by default, or delete under protected policy |
| Undo deletion | New lifetime epoch and new instance; recover retained data only through authorized identity history | Retained data may be reused |
| Copy/paste or unrelated node with same name | New key and new instance | New data |
| Engine/session shutdown, crash, daemon restart | Close or recover-kill all old instances; never adopt old commands | Reopen authorized data on next Acquire |
| Snapshot restore | Quiesce all sandboxes; fence old graph epoch; create fresh instances | Apply the explicit snapshot policy below |

Node object destruction alone must not trigger owner deletion. Reconciliation
first records the logical operation (replacement or deletion), fences the old
lease, then destroys/replaces the object. Stale Close, completion, and retry
calls cannot affect the new lease. Lifetime operations must cover all workspace
mutation paths, including internal deletion, batch operations, undo/redo,
restore, and shutdown, not just public `Engine.RemoveNode` calls.

### Rename and atomic rebinding

A rename is an owner metadata change, never Remove followed by Acquire.
The stable owner key, lifetime epoch, sandbox slots, native identities,
temporary and persistent paths, live processes, and open sockets remain
unchanged. Increase only NameRevision. Existing capability handles remain
valid; their operations resolve ownership by key and epoch, not by the old
name. Status, worker labels, audit records, and name lookup use the new binding.
Historical log records retain the name observed when they were written.

RebindOwner compares the expected key, epoch, old name, and name revision,
validates workspace name uniqueness, and atomically replaces the registry name
binding. Replaying the same operation returns its retained result. A stale
rename, duplicate-name request, or delete/rename race returns a conflict and
cannot move another node's resources. After commit, the old name is no longer
an alias; a new node that takes that name receives its own owner key and data.
ReleaseOwner and ordinary resource operations compare key and lifetime epoch,
not name revision, so cleanup through a handle issued before rename still
closes the correct owner. The coordinator supplies name uniqueness reservations
to the service; nodes cannot invoke RebindOwner directly.

Use one transaction for graph name and owner binding wherever the graph and
registry share a transaction boundary. Do not hold the workspace lock during
IPC or disk I/O. Where they cannot commit atomically, use a serialized rename
coordinator: reserve the name and expected graph revision, durably record the
operation, apply the idempotent registry rebind, then commit graph metadata
under a revision check. Publish rename success and notifications only after
both agree. During the transition, keep sandbox I/O usable by stable handles
but reserve both names against conflicting name-based mutations.

On failure, query the operation receipt before retry or compensation. Complete
the graph update or restore the old registry binding only if its expected
revision still matches. If the result is uncertain, retain the reservation
and pending record for recovery; do not close or recreate the sandbox. This
provides atomic visibility through the coordinated API, not an unsupported
claim of atomic disk commits across processes. Startup resolves pending rename
records before permitting owner lookup or reuse of either reserved name.

All rename paths, including `SetNodeName`, locked/internal workspace changes,
config reconciliation, and undo/redo, must use this coordination. The current
void SetNodeName API cannot acknowledge a durable distributed commit; add an
error-returning coordinated operation for callers that need confirmation and
route legacy entry points through the same coordinator. Do not rely only on
an asynchronous name-change observer to repair ownership after the fact.

Tor adapters must also update name-based secret access and generated-secret
ownership through the existing secret-name integration. Prefer binding these
lookups to the same stable owner key. Until that migration is complete, include
their name mappings in the rename coordinator and delay success until they
agree. Renaming must not lose onion keys or restart Tor. A one-time migration
of old name-hashed state directories can occur while stopped; normal renames
never rename live storage directories.

Do not wait for native process exit under the workspace lock. Revoke access
synchronously, then close asynchronously with bounded shutdown. An old command
must be proved stopped before a replacement can acquire its exclusive state
directory. Pending delete cleanup and new Acquire are serialized by the
registry. Engine Close waits for cleanup or reports a terminal cleanup failure.

Acquire is idempotent for the same owner/slot/template. A different template
in the same slot requires Remove first. Failed instances are terminal and
cannot satisfy Acquire as if healthy. Explicit replacement creates a new
generation. Cancellation or a stale completion disposes of partial resources.

Sandbox persistent roots stay outside R's ordinary file storage. Version 1
does not include them in existing snapshots. Restore stops programs but keeps
their current external state; it does not roll that state back with the graph.
Document this in snapshot metadata and the admin UI. Tor onion keys remain in
the existing secrets store, while guard state has this external-state behavior.
A later backup API can export quiesced, validated data; do not recursively copy
live sandbox trees with the daemon's general snapshot code.

Retained data from deleted owners has an explicit orphan record. Only a
protected administrative cleanup operation can purge it or authorize
attachment to a recovered owner. Nodes cannot list or claim orphan
directories. Automatic deletion, when configured, follows process exit
verification and does not follow links in the old tree. Persistent storage has
no byte quota in the current contract; operators must monitor its filesystem.

## 4. Daemon engine: common part

Use proposed packages `bin/internal/subprogram/{policy,manager,storage,launch}`
and platform backends under that tree. Reuse reviewed low-level isolation
launch, path, relay, and supervision primitives where appropriate. Do not
reuse R's restrictive role profile for programs with different needs.

Bootstrap compiles protected configuration into immutable templates. It holds
trusted executable and source directory identities, resolves identities, and
checks for overlapping roots. Writable sandbox roots cannot overlap daemon
config, runtime storage, broker state, executable trees, another sandbox, or
protected read-only sources. Never recursively chown an arbitrary supplied
directory. Create private children under a validated parent.

An instance moves through:

```text
absent -> preparing -> ready -> closing -> closed
                  \-> failed -> cleanup
```

Preparing selects a plan, allocates storage and identity, establishes the
process-tree boundary, starts W, installs restrictions, and verifies the plan.
Ready means the sandbox service is usable, not that Tor has bootstrapped.
Launch separately verifies each child's restrictions before execution.

Use session, owner epoch, instance generation, and command generation on all
resources. Mutations carry operation IDs and retained receipts. A timed-out
Start must be queried or canceled by ID, not repeated with a new ID that can
create a second process. In-flight Acquire/Start/Listen operations cannot
publish resources after their owner is revoked.

S keeps a private recovery journal for storage identities, process boundaries,
platform rules, and cleanup progress. Cleanup uses held or verified identities,
not old numeric PIDs or unvalidated saved paths. On restart, prove an old tree
dead before deleting temporary data, releasing its identity, or reusing state.
If cleanup cannot be proved complete, quarantine the instance and fail its
replacement. Do not silently leak running programs.

Set limits on instances, commands, descendants/threads, memory, CPU, temporary
storage, open files, sockets, outstanding operations, and relay buffers.
Distinguish per-command, per-instance, and global ceilings. Auto can report an
unavailable optional OS resource limit, but service-side allocation bounds
always apply. Resource limits explicitly marked required cause creation to
fail if absent. Persistent storage has no byte quota in the current contract.

Environment policy is an allowlist of names with optional protected literal
values, applied to the trusted launch environment. PATH is a sanitized fixed
search path by default; program lookup still uses held executables. Do not
inherit loader injection, shell startup, credential, proxy, or daemon secret
variables. Include the minimal Windows runtime variables when needed. Close
all unlisted handles/descriptors, including activation sockets, control IPC,
directory handles, tokens, and namespace handles. Only requested standard I/O
reaches the third-party child.

Expose bounded status through the admin API and worker UI: requested mode,
template and profile revision, effective identity, applied/omitted protections,
reasons, limits, process state, and cleanup state. Protected host paths and
secrets are not node-facing status. Record the verified launch summary before
third-party code runs. No node-facing API can turn a feature off.

## 5. Linux backend

### Full profile

Use a small native launcher before any third-party code runs. Its reviewed
sequence must establish all of the following before exec:

1. Place the instance/command in its delegated cgroup v2 subtree and create
   private mount, network, PID, IPC, and UTS namespaces. Use a private user
   namespace only where the mapped-identity profile is qualified. Keep a small
   trusted PID-1 reaper alive for the namespace lifetime.
2. Build a minimal filesystem root from held sources. Bind only template
   system/runtime files and explicit additional inputs read-only. Make mounts
   private; enforce recursive read-only behavior for nested mounts. Supply
   temporary and persistent trees as separate writable mounts. Pivot into the
   new root and remove access to the old root.
3. Supply only necessary synthetic OS interfaces: a private proc view, a
   minimal /dev with null/zero/random devices, and private /dev/shm if required.
   Treat these as explicit profile exceptions, not arbitrary host filesystem
   grants. Do not mount host /proc, /sys, /run, home directories, or all of /dev.
4. Bring up loopback only. Create no veth, default route, host-network socket,
   or network forwarding path. W creates sockets here and relays bytes to the
   owner over its pre-established private channel.
5. Clear supplementary groups, select the unprivileged GID/UID, clear all
   capability sets including bounding and ambient authority, lock the relevant
   securebits, and set `no_new_privs`. Order these operations so needed setup
   authority is removed only after setup, and no authority remains at exec.
6. Apply Landlock filesystem rights and the qualified amd64 seccomp profile,
   with process-control and alternate-ABI bypasses denied. Block
   namespace creation/joining, mounts, ptrace/process memory access, kernel
   administration, raw/packet sockets, and unneeded socket families. Keep
   ordinary threading and approved descendant execution usable. Account for
   clone3 and indirect syscall paths such as io_uring explicitly.
7. Set limits, cwd, umask, and environment; close inherited authority; verify
   effective state; execute the held approved image. Report setup/exec failure
   through an exec-status channel that closes on success.

The full profile combines mount confinement with Landlock and syscall controls.
Landlock alone does not hide all filesystem metadata or cover all other kernel
interfaces. Its supported rights vary by ABI. Probe the ABI and require every
right used by the selected profile; do not claim that a port allowlist proves
daemon-only networking. See the [kernel Landlock documentation](https://docs.kernel.org/userspace-api/landlock.html).

The read-only runtime view includes the executable, dynamic loader, required
libraries, and selected data such as CA certificates and timezone files.
Avoid exposing all of /usr if a smaller packaged runtime works. If a template
does expose /bin or /usr/bin for compatibility, read access and execute access
remain separate. Validate host symlink aliases when constructing the view.

Read-only mounts can still expose live Unix sockets or other host IPC objects.
Reject device, socket, and FIFO sources and do not bind directories that can
expose such host endpoints to the child. Where a source tree cannot meet that
rule, stage a validated regular-file view or use a qualified policy that denies
socket resolution. Private network namespaces alone do not block pathname
Unix sockets. Abstract Unix sockets must be contained by the private network
namespace or explicitly denied in a partial profile.

W enters its namespace once at startup and stays there. Its socket service has
no host egress. Privileged launch setup is kept out of the general socket/file
request loop. Child processes cannot access W's private control channel or
signal/trace W. The reaper supplies a stable PID visible to Tor for its initial
`__OwningControllerProcess` check; the host runtime PID would be wrong in a
private PID namespace. This PID is a trusted adapter bootstrap value, not a
general process-control API.

Use a distinct worker credential where available. In a same-UID profile,
non-dumpability plus qualified signal/ptrace and IPC restrictions must protect
W; sharing an address space or relying on an undisclosed socket pathname is
insufficient. Program seccomp rules can permit the controller's required
existence check without permitting delivery of signals to W or the reaper.

Cgroup membership, tracked process handles, and the namespace reaper provide
tree termination and exit observation. Process groups alone are insufficient:
a descendant can call setsid. Deny cgroup filesystem access and migration. A
lost supervisor channel makes W terminate the instance; a lost W makes S kill
the cgroup. On shutdown, use cgroup.kill where supported and wait for the group
to empty. Parent-death signals supplement this behavior but do not replace it.

### Identity selection and partial profiles

For a privileged daemon, allocate a non-root identity from a provisioned pool
or use an explicitly configured non-root service account. Do not silently use
the global `nobody` account. Reusing an identity requires proven cleanup;
distinct concurrent identities are preferred. An account pool is deployment
authority, not something nodes can create.

For an ordinary user, keep that host UID when no approved identity transition
is possible. This is already unprivileged; clear any capabilities and groups
that can be removed. A qualified user-namespace profile can provide additional
separation with appropriate ID mappings. Linux strict requires all semantic
protections, not necessarily a different host UID. If same-UID process control,
group authority, mounts, or tree cleanup cannot be contained, it is partial.
Never install setuid helpers or request elevation to improve auto mode.

Examples of independently reported groups are sanitized environment, exact
descriptor inheritance, privilege removal/no-new-privileges, filesystem access
restriction, private filesystem view, network context, process/IPC separation,
and process-tree/resource enforcement. Dependencies matter: a namespace cannot
be reported as usable network containment unless W can provide local sockets.
Do not treat a half-built group as applied.

A non-root system with Landlock but no usable namespaces can apply a filesystem
access policy and no_new_privs, while reporting private filesystem visibility
and network containment unavailable. It uses host loopback for owner sockets.
Landlock network rules alone cannot distinguish the intended daemon from all
other peers at an allowed port. Do not replace private networking with proxy
environment variables, a chosen UID, or a best-effort route change.

## 6. Windows backend

Use a Windows launcher that creates the process suspended, applies the selected
token and process attributes, assigns it to a Job Object with no breakaway,
verifies the plan, and only then resumes the initial thread. Use explicit
handle inheritance, a constructed environment, and safe command-line quoting.
Job ownership spans Tor and all managed transports; close/kill waits for the
whole job to empty. If assignment fails, terminate the suspended process.

Privilege removal uses a restricted primary token, disabled administrative
groups, removed privileges, and an integrity level suited to the profile.
Validate ordinary-user, elevated administrator, and LocalSystem launches
separately. A filtered LocalSystem token is not assumed equivalent to a
separate ordinary account. Use a provisioned account or a qualified AppContainer
profile when required; do not prompt for UAC or account passwords at runtime.

The implemented `windows-native-v1` profile uses a deterministic 128-bit
owner-and-slot restricting SID and a Low-integrity restricted primary token.
World and Users restricting SIDs permit the normal Windows runtime surface.
Sandbox writable roots have neither broad ACE, so the token's second access
check requires its instance SID. The daemon holds each root and executable by
verified file identity while the instance is active. The profile is qualified
on Windows Server 2022 build 20348 amd64 for ordinary-user, elevated, and
LocalSystem launch identities.

An AppContainer identity per instance can add process, credential, and object
restrictions. Grant only that identity access to its writable directories;
give S the access required for management. Use low-integrity labels where
required. Keep persistent storage identity stable through the owner registry,
while instance identity and ACL grants are renewed safely at restart.
Windows has no directly interchangeable Linux `no_new_privs` switch: the
complete token, child inheritance, object, and launch policy must establish
the corresponding result. [Microsoft's AppContainer overview](https://learn.microsoft.com/en-us/windows/win32/secauthz/appcontainer-isolation)
describes these boundaries, but does not establish an exact filesystem
allowlist for this application.

### Filesystem limits

Hold validated path components and reject reparse, UNC, device, alternate
stream, hard-link, and case/alias bypasses. Set explicit DACLs on sandbox-owned
directories. Standard AppContainer access includes OS-defined resources and
may include files granted to broad application-package groups. A restricted
token plus writable-directory ACLs is not proof that every other host file is
unreadable. Report this as partial filesystem confinement until an exact view
is qualified. Do not rewrite unrelated host ACLs to simulate a private root.

The native v1 backend rejects mapped `ReadOnly` inputs because it cannot expose
the source at the declared sandbox target without an exact filesystem view.

A full future Windows profile needs a supported container/silo or VM-backed
filesystem and network context, or a separately reviewed equivalent boundary.
The sandbox interface permits such a worker because it exposes streams and
logical paths, not native process or file handles. A VM backend must stage
approved executables and read-only inputs, map the two writable roots, and
provide W inside the guest. It is a distinct backend with explicit installation
requirements, not a silent auto-mode dependency.

### Network limits and target implementation

AppContainer with no network capabilities can deny required Tor loopback too.
A general loopback exemption permits more than the intended owner endpoints.
Neither alone provides the desired daemon-only network property. Windows
Firewall rules keyed by an
executable path also fail to cover descendants running another image; the
current tor-driver Windows containment adapter already rejects that claim.

A possible native backend uses a per-instance enforceable network identity,
WFP policy for IPv4/IPv6 inbound and outbound traffic, and owner endpoint
registration. W would create and relay loopback sockets under the matching
context. Rules must cover every descendant and both directions, deny other
host services and sandboxes, and survive launch/exit races. Tor-created dynamic
control/SOCKS endpoints need authenticated ownership registration before they
can be dialed. Address/port or executable-path matches alone are insufficient.
[WFP ALE layers](https://learn.microsoft.com/en-us/windows/win32/fwp/ale-layers)
provide connection/accept classification points; they do not by themselves
prove this complete design. This path requires a separate prototype and
hostile-child qualification before it is advertised as applied.

Version 1 Windows provides qualified token, process/object, environment, handle,
job, and private-storage protections. Windows strict requires all of these;
auto can omit a group whose prerequisites are unavailable. Exact filesystem
confinement and private daemon-only networking are explicit Windows native
profile exclusions from the strict minimum, as listed in section 1. Owner
sockets use host loopback. Windows strict can therefore start Tor once its
required groups pass qualification, even though the desired full boundary is
not available. Failure of any required group still prevents launch.

A no-network AppContainer profile is usable only for templates that do not
need sockets. It cannot substitute for usable Tor networking. A template with
`RequiredFeatures` that includes private networking or the exact filesystem
view fails on this native backend. Report the exclusions even on successful
strict launches; component tests do not prove a complete production profile.

## 7. Composition with daemon isolation

The current roles are N (trusted network broker), R (runtime), C (config writer),
and H (command helper). Add S as an optional sandbox role, with one or more W
workers. S is not a replacement for H: existing CommandRunner host commands
remain under their existing policy. A node cannot opt an H command into or out
of this sandbox API.

```text
direct:    daemon [engine + policy service] -> W -> approved programs

isolated:  N [protected bootstrap + supervision]
            |-> R [engine, nodes, Tor driver]
            |-> C                 | typed sandbox IPC
            |-> H                 v
            `-> S [policy, storage, launch] -> W -> approved programs
```

N reads the Sandbox protected configuration before launching R. It uses only
the path subset needed for trusted startup checks and sends the exact protected
policy to S in bounded, digest-checked chunks. S performs the strict decode,
normalization, platform probe, and resource opening before it reports ready. R
receives status and an opaque sandbox service. It does not receive executable
source paths, identity selectors, protected roots, environment sources, or
mode-changing operations.

N establishes an authenticated R/S lane bound to the same session, policy
digest, and graph epoch conventions as current isolation IPC. R talks directly
to S on that fixed lane; N does not relay all sandbox data. S never accepts
connections from children. N supervises S and receives bounded status. C and H
have no sandbox API. W receives only its instance launch plan and bounded
operations, never authority to create another instance.

The closed protocol includes BindOwner, RebindOwner, ReleaseOwner, Acquire, Remove, Status,
Start, Signal, Wait, file operations, local socket operations, Close, receipt
lookup, and stream grants. All references carry session/type/generation/owner
binding. Use strict decoding, version negotiation, unknown-field rejection,
bounded input schemas, idempotency receipts, and separate control/data/log
lanes. File and process I/O use bounded relay streams. Reuse existing deadline,
flow-control, half-close, accept/claim, and cancellation patterns. No file,
socket, token, namespace, or process handle crosses into R.

R's request to bind an owner is checked against the session's class/template
grant and the daemon's durable identity registry. It cannot bind another
session's owner or access an orphaned persistent directory by guessing a key.
R can lie about its own graph: quotas and protected program/template policy
must still constrain it. This service does not make assertions about arbitrary
in-process node code being honest.

S needs setup authority that R's profile deliberately denies. Launch S from N
before applying child restrictions, with only the policy-approved directory,
credential, namespace, and process authority. Narrow W after setup. Do not try
to undo R's inherited seccomp, Landlock, token, or job restrictions. S must not
receive host network configuration authority merely because N has it.

For `isolation=off`, the same manager policy and lifecycle run without R/S IPC.
W remains an implementation helper where needed. For `sandbox=off` under
isolation, S starts the approved program with the trusted launch authority's
ordinary execution environment. It must not inherit R's isolation as an
accidental substitute, nor leak N/S IPC handles. This combination is explicitly
unsafe for hostile programs; it is an intentional operator policy.

Missing S support in an isolation profile causes sandbox operations to fail
when requested, even in sandbox off mode. Never execute in R or silently route
through H. Qualify S separately for `linux-full-v1` and `linux-landlock-v1`; the
latter may provide only partial auto protection. Rootless support must not
depend on the currently unsupported H role.

Loss of R revokes all its sandboxes. Loss of S fails the owning isolated
process set, and N kills tracked workers and child boundaries. The supervisor
retains enough process-boundary identity to do this without S's cooperation.
Loss of N triggers bounded S/W shutdown. Bootstrap and package version checks
must include S and its native workers. Windows isolated composition remains
unavailable until the existing daemon isolation release requirements and the
new S composition both pass.

## 8. Protected configuration

Add `Sandbox` to protected config only. Default mode: `auto`. No regular node
field, snapshot, admin edit, or library call can select mode or feature flags.
For the first implementation, use one mode for all templates; do not add
per-node mode exceptions. Policy changes require daemon restart.

Illustrative Linux protected configuration (proposed schema):

```json
{
  "Sandbox": {
    "Mode": "strict",
    "TemporaryRoot": "/run/almagest/subprograms",
    "RegistryRoot": "/var/lib/almagest/subprogram-registry",
    "Limits": { "Instances": 32, "Commands": 64, "RelayBytes": 16777216 },
    "Programs": {
      "tor": {
        "Executable": "/usr/bin/tor",
        "Builder": "tor-v1",
        "Descendants": ["obfs4"]
      },
      "obfs4": {
        "Executable": "/usr/bin/lyrebird",
        "Builder": "tor-transport-v1",
        "Descendants": []
      }
    },
    "Templates": {
      "tor-client": {
        "AllowedClasses": ["almagest/XTorDriver"],
        "Programs": ["tor", "obfs4"],
        "RuntimeProfile": "linux-tor-v1",
        "PersistentRoot": "/var/lib/almagest/tor-state",
        "DeleteStateOnOwnerRemoval": false,
        "ReadOnly": [],
        "Environment": {
          "Inherit": ["LANG"],
          "Set": { "PATH": "/usr/bin:/bin" }
        },
        "Linux": { "UID": 200000, "GID": 200000 },
        "Limits": {
          "Commands": 1,
          "Tasks": 64,
          "MemoryBytes": 536870912,
          "TemporaryBytes": 67108864,
          "Sockets": 512,
          "Required": ["Tasks", "MemoryBytes", "TemporaryBytes"]
        }
      }
    }
  }
}
```

`Commands` counts API-started command trees. On Linux, `Tasks` includes their
descendant processes and threads. On `windows-native-v1`, `Tasks` is the Job
Object active-process ceiling; Windows Job Objects do not provide an aggregate
thread-count ceiling. `TemporaryBytes` is an aggregate temporary-storage limit
in the full Linux profile.

Additional defined options:

| Option | Meaning and validation |
| --- | --- |
| `Templates.*.ReadOnly` | Array of `{Source, Target}` mounts; existing absolute trusted source and validated sandbox target, no overlap with writable/system special paths. Read access only. |
| `Templates.*.PersistentRoot` | Optional protected parent; omission gives temporary-only storage and makes `FS(Persistent)` fail explicitly. |
| `Templates.*.RuntimeProfile` | Installed versioned platform profile defining system files, execute rights, socket support, and syscall/token policy. Unknown profiles are errors. |
| `Templates.*.RequiredFeatures` | Optional list that strengthens the OS strict minimum; for example `private-network` or `exact-filesystem-view`. Required features fail creation when unavailable, including in auto; off is rejected for a template with explicit requirements. No field can remove an OS strict requirement. |
| `Templates.*.Linux.IdentityPool` | Reserved for a future pre-provisioned identity allocator. The current Linux backend fails preflight when it is selected. It is mutually exclusive with an explicit nonzero UID/GID or a current-user profile. |
| `Templates.*.Windows` | Qualified token/profile selection and optional provisioned account reference; no node-provided credentials or passwords. |
| `Templates.*.Limits` | Finite ceilings, including optional CPU, files, transfer size, startup timeout, and graceful shutdown timeout. Zero does not mean unlimited. |
| `Programs.*.Executable` | Absolute trusted image; resolve and hold at startup. No PATH discovery from node input. |
| `Programs.*.Builder` | Installed builder ID and revision. Schemas and argument rules live in reviewed daemon code. |
| `Programs.*.Descendants` | Closed set of approved program IDs, checked for validity and template inclusion. |

Validate unknown fields, duplicate IDs, unsupported modes, invalid bounds,
recursive descendant definitions, unsafe environment names, and all directory
overlaps before startup. Disabled/unused templates are still schema-validated;
platform prerequisite checks can occur when first acquired. Strict creation
failure marks the requesting node failed and must not prevent unrelated graph
nodes from running. Invalid protected configuration fails daemon startup.

Keep `almagestd-protected-config.schema.json`, its reference from
`almagestd-config.schema.json`, Go structs, defaults, and example configs in
sync.

When the deferred XTorDriver migration starts, migrate
`Protected.Experimental.TorDriver` host executable, identity, temporary, and
state settings to the template/program definitions. Retain Tor protocol
timeouts and circuit settings at their existing appropriate authority level.
Tor's own `Sandbox=dynamic|linux` is a different setting: it controls Tor's
internal seccomp behavior and dynamic onion compatibility, not Almagest mode.
Reject conflicting old and new host settings; provide a documented conversion.
Do not silently preserve the old best-effort process fallback under strict.

## 9. Tor-driver integration proof

The current XTorDriver adapter calls `direct.NewBestEffortSystem` and adds a
Windows restricted-token adapter. The new path replaces that system with an
owner sandbox adapter. It must not pass the daemon's ordinary LocalNetwork
as Tor's LocalNetwork when network containment is active.

| tor-driver dependency | Sandbox implementation |
| --- | --- |
| `FileSystem.TempDir` | Temporary Files.TempDir; translate the returned path into child view. |
| `PrivateDir`, `WriteFile`, `ReadFile`, `RemoveAll` | Bound root-relative file operations; template owns permissions/identity; config sealing as described above. |
| `Processes.Start` | Recognize approved Tor launch, validate exact arguments, translate to program ID and typed slots, then Command.Start. Reject arbitrary Launch values. |
| `Process.Wait/Kill/Release` | Stable command result, complete tree kill, idempotent bookkeeping release. Release does not orphan descendants. |
| `Processes.PID` | ProcessContext.ControllerPID, valid in the child PID view. Never use R's host PID blindly. |
| `Processes.Platform` | ProcessContext.Platform, the target execution platform; important for a future VM worker. |
| `Dependencies.LocalNetwork` | Sandbox gonnect adapter, covering both Dial and Listen. |
| `Dependencies.Outbound` | The selected graph Network, held by the driver in R or the direct daemon. Never installed as a child network interface. |
| `OnionKeys` | Existing node-scoped secret storage; no sandbox authority over other secrets. |

Adapter configuration uses ChildPath for temporary/state directories and
ProgramPath for Tor and approved transports. It passes no node-selected
`tor.Identity`; identity is already set by the template. The FileSystem adapter
maps only known child-root prefixes back to Path values, with segment-aware
validation. It cannot accept arbitrary absolute host paths from tor-driver.

The complete startup and traffic sequence is:

1. Acquire the node's `tor` slot from `tor-client`. Create private work files and
   select its persistent guard-state directory if available.
2. Driver calls LocalNetwork.Listen on `127.0.0.1:0`. W binds inside the sandbox
   and returns its real address. Accepted streams are relayed to the driver's
   authenticated upstream SOCKS proxy outside the child boundary.
3. Driver stages torrc/defaults with that upstream address. The daemon validates
   and seals launch configuration, then starts Tor. Its control/SOCKS listeners
   use the same private loopback context, including dynamically assigned ports.
4. Driver reads the control-port file and cookie through Files. It dials the
   control endpoint through the sandbox adapter, authenticates, calls
   TAKEOWNERSHIP, and resets the initial owning-controller PID setting.
5. Client network requests dial Tor's SOCKS endpoint through the sandbox
   adapter. Tor's outbound connections reach the driver's upstream proxy, which
   dials only the attached graph Network. No attached network means proxy
   rejection, not direct fallback.
6. Onion services create further LocalNetwork listeners. Their sandbox-local
   addresses become ADD_ONION targets; accepted connections reach the driver
   and its service consumers through the same relay.
7. Managed obfs4 transports inherit the instance boundary and tree lifetime.
   Their approved images are readable/executable; their state stays within
   writable roots. The transport's upstream use must be covered by E2E tests.
8. Outbound link changes update the driver's outbound gate. They do not replace
   the sandbox. Driver readiness and terminal failures retain existing
   generation checks; a created sandbox or started process is not `up`.
9. Driver Close uses its control protocol first, then a bounded Kill/Wait path.
   Node deletion closes the sandbox even if driver cleanup fails. Persistent
   guard state remains according to protected retention policy.

Linux can support this sequence with the existing v0.1.2 dependency interfaces
and a daemon-side Tor adapter/builder. Test the controller PID behavior, startup
context cancellation, config sealing, and dynamic onion operation explicitly;
these are easy to miss if integration only tests process launch. The initial
A future Windows implementation can support the sequence with reported
host-network exposure in auto mode and an OS-specific strict mode. Strict will
require qualification of every mandatory Windows group. Private networking
will remain an explicit OS-profile exclusion unless the template requires it.

XTorDriver remains experimental until both direct and isolated adapters pass
qualification. Enabling it in isolation needs changes to the current explicit
`--experimental` direct-mode gate and protected bootstrap projection. Remove
that gate only for classes whose required host operations have moved behind
the reviewed interface.

## 10. Qualification and implementation order

The portable contracts, owner registry, protected policy, direct backend,
Linux launch/worker backend, Linux isolated S role, and native Windows direct
backend are implemented. The Windows backend provides `windows-native-v1` on
qualified Windows Server 2022 amd64 hosts. It reports the declared filesystem
and network OS exclusions, so strict can be satisfied while `full_boundary`
is false. Windows isolated composition remains future work. The next node
integration is the explicitly deferred `XTorDriver` migration.
Do not expose a feature as applied until its native tests pass.

Confinement denial tests apply to features required or reported as applied by
the selected profile. For excluded properties, verify truthful status and API
bounds instead of asserting a boundary that the OS profile does not promise.

The complete qualification matrix includes the following tests. Tests for
backends that are listed as future work are gates for those future backends,
not claims about the current implementation:

- All nine mode combinations of isolation and sandbox policies where isolation
  itself is supported. Isolation selection failure takes precedence; sandbox
  off never bypasses isolation role restrictions.
- Root, ordinary user, capability-bearing user, and restricted/containerized
  Linux callers; Windows ordinary user, administrator, and LocalSystem.
- Negative probes for outside reads/writes, read-only writes, nested mounts,
  symlink/reparse/hard-link races, inherited descriptors, credentials, exec
  privilege gain, process tracing/signaling, and unwanted IPC/socket families.
- Direct IPv4/IPv6 TCP/UDP/DNS egress denial; attempts to reach host loopback,
  admin listeners, other sandboxes, raw sockets, and abstract Unix sockets.
  Positive owner Dial/Listen, UDP if enabled, and writable-root Unix sockets.
- Descendant fork/exec/setsid and signal resistance; launch-to-job/cgroup races;
  cancellation before, during, and after exec; children holding pipes open;
  S/W/R death; restart recovery and identity reuse only after proven exit.
- Partial-feature probes, selected-feature application failure, and strict
  refusal before execution. Use a marker executable to prove no child ran.
- Successful Windows strict with explicit OS exclusions; failure when a required
  Windows group is unavailable; unchanged Linux strict requirements; rejection
  of machine-local failures presented as OS exclusions; RequiredFeatures
  overriding exclusions; distinct strict-satisfied and full-boundary status.
- Path and message fuzzing, invalid program/slot selection, guessed owner IDs,
  stale epochs, duplicate mutations, lost receipts, quota exhaustion, and slow
  streams without blocked control or unbounded buffers.
- Every node deletion/reconstruction path; config bursts and equivalent applies;
  late async results; undo/copy/paste; restore; state retention and cleanup.
- Rename while Tor and sockets are live; unchanged process identity, paths,
  and instance generation; refreshed name-based secret access; rename collisions,
  rename/delete races, undo/redo, repeated IPC requests, failure between commits,
  restart recovery, and reuse of the old name by an unrelated node.
- Tor control authentication, initial controller PID, startup-context lifetime,
  client SOCKS, upstream replacement/unlink, onion service listeners, managed
  obfs4 descendants, persistent guard state, and abrupt parent loss. Extend
  `tor_driver_private` and `tor_driver_onion_http`, including isolated runs.

Use focused `just check` selectors during implementation and the full suite
for the cross-project integration. Native platform gates are mandatory for the
claims they cover; skipped tests are not qualification evidence.

## 11. Future improvements

Persistent-storage byte quotas are outside the current sandbox contract.
Persistent roots still use protected ownership, bounded API transfers, and the
selected filesystem boundary. A future implementation can
add an aggregate quota only when it uses an enforceable mechanism, such as a
dedicated volume or filesystem project quota. `RLIMIT_FSIZE` is not a suitable
substitute because it limits one file and also affects temporary and persistent
mounts in the same process.

The qualified native Linux backend currently targets amd64. Support for arm64
and other architectures is future work. Each architecture needs a reviewed
syscall policy, alternate-ABI analysis, native launcher build, hostile-process
tests, and full Docker or machine qualification before it can report the Linux
strict profile. The platform-neutral sandbox interface and worker protocol do
not depend on amd64, so this work does not require a new node-facing API.

## Repository references

- [Architecture](ARCHITECTURE.md)
- [Current daemon isolation](docs/DAEMON_ISOLATION.md)
- [Runtime lifecycle rules](docs/RUNTIME_NODE_LIFECYCLE_PITFALLS.md)
- [Engine build and lifecycle](lib/engine.go)
- [Node runtime interfaces](lib/nodekit/runtime.go)
- [Current Tor dependency adapter](lib/experimental/tor_backend.go)
- [Current Windows Tor token handling](lib/experimental/tor_containment_windows.go)
- [Protected config schema](almagestd-protected-config.schema.json)
