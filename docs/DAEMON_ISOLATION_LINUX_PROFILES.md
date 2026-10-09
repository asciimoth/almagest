# Linux role restriction profiles

Checkpoint 24 adds the native launcher in `bin/native/linux-role` and the Go
launch contract in `bin/internal/isolation/linuxprofile`. These are components
for the later isolated composition. The production daemon still uses direct
mode. This change does not enable a complete Linux isolation profile.

## Launch boundary

The launcher is a static Linux amd64 executable. Never install it setuid or
with file capabilities. It uses only the caller's authority. Unsupported
rootless launches fail with explicit `--no-isolation` guidance. The current
qualification uses static Go 1.26 binaries; other architectures and build modes
need separate qualification.

Trusted bootstrap supplies these held descriptors:

| Descriptor | Object |
| --- | --- |
| 3 | Connected Unix stream control channel |
| 4 | Root-owned, non-writable-by-others, static role executable |
| 5 | Private, pre-provisioned role directory, owned by the selected UID/GID |
| 6 | The launcher's current, already limited cgroup v2 directory |
| 7 | H's fixed static command, or N's optional Linux TUN device |
| 8 | The held native launcher executable used by the Go launch adapter |
| 9 | Optional separate N/R data lane |
| 10 | Optional separate diagnostic lane |
| 11 | Optional separate N/R admin lane |

Bootstrap must validate trusted paths before opening these objects. Runtime
requests cannot select them. The native launcher checks types, ownership,
permissions, ELF headers, connected channel type, cgroup filesystem and cgroup
membership again. It rejects dynamic ELF files, interpreters, root identities,
unknown roles, unbounded limits, and capability expansion. There is no path,
library search, or arbitrary environment field in its argument contract.

The Go adapter enters the held launcher through `/proc/self/fd/8`. It supplies
an empty environment and a fixed working directory. The native launcher checks
stdout and stderr as pipes or connected Unix streams. It replaces stdin with
an EOF pipe. `close_range` removes all other descriptors before role exec,
including descriptors above the new hard descriptor limit. Bootstrap must
close its copy of each child channel after `Start`.

## Restriction order

All mount, identity, capability and initial filter work occurs in the native,
single-threaded launcher. No Go callback runs between fork and exec.

1. Require finite cgroup memory, task and CPU limits, with swap disabled.
   Require that the launcher already belongs to the supplied cgroup.
2. Create a private PID namespace, mount namespace, IPC namespace and UTS
   namespace. R, C and H also get private network namespaces. N keeps the
   launch network namespace.
3. Make mount propagation private. Create a bounded tmpfs root, copy the
   validated static executable, and mount only the held role directory at
   `/state`. `open_tree` and `move_mount` keep this operation bound to the held
   object across namespace creation. The storage mount is nodev, nosuid and
   noexec. Do not import nested mounts. Provide private `/tmp` and detach the
   old root. There is no host proc, sys, run, etc, or general device view.
4. Close setup descriptors. Apply hard descriptor, per-file size, locked-memory
   and core-dump limits. Clear supplementary groups and install the role's
   non-root UID/GID. Drop setup rights and lock securebits. R, C and H retain
   no capabilities. N retains only the intersection of its requested
   `NET_ADMIN`/`NET_BIND_SERVICE` rights and the launch permitted set. Ambient
   rights carry that exact set through exec; further ambient raises are locked.
5. Set `no_new_privs`. Require Landlock ABI 3 or newer, including refer and
   truncate mediation. Permit executable reads/exec only at the fixed files,
   and application writes only below `/state` and `/tmp`.
6. Install the reviewed seccomp allowlist before Go starts. Permit Go's exact
   amd64 thread clone flags; return `ENOSYS` for `clone3`. Block namespace
   changes, mount, ptrace, process-memory access, descriptor theft, BPF, perf,
   keyrings, io_uring, and unapproved device ioctls. R/C/H cannot create sockets.
7. The minimal trusted role entry calls `linuxprofile.Seal` before reading
   application input or constructing the application. This resets dumpability
   after exec and removes exec from every existing Go thread with TSYNC.
   A positive TSYNC return is a failure even when errno is zero. Future threads
   inherit the restrictions. H retains only the execution ability needed by
   its fixed command dispatcher.

The static role's trusted package initialization must not read regular config,
snapshots or runtime-controlled environment before `Seal`. A profile is not
established by an environment marker or by `Seal` alone. The native setup and
the final filter are both required.

The waiting native parent removes the empty setup mount point, closes all role
channels, and drops every capability before waiting. It has no restart or
later launch operation. Complete daemon supervision remains checkpoint 26.

## Role-specific behavior

R can use private storage, bbolt, timers, garbage collection and inherited
streams. It has no host socket, device, process or service access.

N can create IPv4, IPv6, Unix and netlink sockets in the launch network context.
Missing `NET_ADMIN` does not prevent ordinary TCP/UDP operations. An optional
held character device is accepted only for Linux TUN major 10, minor 200. N
gets that exact device at `/dev/net/tun`, plus the reviewed TUN ioctl set.
Persistent TUN creation, device ownership changes and generic ioctls remain
denied. The qualification creates a nonpersistent TUN, sets its MTU, link state
and address through netlink, then receives a real UDP packet through the TUN.

C sees only its provisioned target directory. The existing config writer runs
after the final filter, including `openat2`, metadata checks, compare-and-replace,
file sync and directory sync. Targets requiring a different owner must be
provisioned for the writer identity; this profile does not retain chown rights.

H sees its own state directory and one additional static command at `/command`.
It has no network capabilities or socket creation. The existing protected
command dispatcher continues to enforce command IDs, argv slots, environment,
deadlines and output limits. Child processes inherit H's namespace, Landlock,
seccomp, identity and cgroup restrictions. The helper uses an EOF input pipe,
so it does not require a host `/dev/null` mount. Dynamic commands and commands
requiring other host resources need separately reviewed profiles.

## Qualification and open gates

Run `just linux-isolation-profiles`. All native operations run in disposable
Docker containers with isolated networks. The runner requires `/dev/net/tun`;
an absent device is a failed qualification, not a successful skip. Logs are
saved under `artifacts/linux-profiles/<run-id>/`.

The suite checks four simultaneous distinct OS threads created before TSYNC,
then repeats the checks from a later pinned goroutine. It checks capability sets, bounding sets,
securebits, namespaces, syscall denials, path and symlink escapes, environment
clearing, high descriptor closure, resource limits, actual bbolt transactions,
config replacement, native sockets, TUN packets, and fixed command execution.
It repeats the role suite with and without network capabilities. Negative
tests cover descriptor confusion, fake cgroups, invalid identities, malformed
ELF files, arithmetic overflow and unmet limits. Separate containers remove
namespace setup rights or deny Landlock; both must stop every role before
application code runs. A diverged-thread test checks the positive TSYNC failure
result, and an x32 attempt must terminate the process with SIGSYS. Portable and
native parser fuzz campaigns run for 30 seconds each.

The following remain release blockers, not silently disabled native features:

- The full sysnet-linux default-TUN composition still needs the route-monitor
  namespace-entry issue from checkpoint 10 resolved. The TUN primitive test is
  not qualification of the full default-TUN backend.
- BPF/process-rule loading and host process inspection need narrow helper
  profiles. N never receives `SYS_ADMIN`, `BPF`, `PERFMON` or debug rights as a
  compatibility workaround.
- systemd-resolved, NetworkManager, resolvconf and direct-file host DNS need
  fixed-method/fixed-target helper qualification. No general D-Bus socket or
  `/etc` write access is exposed here.
- Bootstrap/service integration must allocate separate role identities and
  cgroups, reserve broker capacity, and provision a storage quota for each
  persistent directory. The native launcher verifies its cgroup ceilings, but
  does not provision cgroups or claim that a per-file limit is a disk quota.
- The complete daemon, all required native features, command profiles and
  failure/recovery behavior still need the later composition and qualification
  gates. Direct mode has no launcher, IPC or profile overhead.

`CheckFeatures` makes the known rootless, BPF, host-DNS and process-inspection
gaps explicit to future composition. It must be applied to the selected
protected backend requirements; an unsupported feature must stop that isolated
composition rather than being relabeled as missing native launch authority.

## Checkpoint results, 2026-10-10

- `bash dev/linux-profiles/run.sh` passed on Linux 7.2.8 amd64, Docker 29.8.1
  and Go 1.26.6. Final evidence is in
  `artifacts/linux-profiles/20261009T223454Z-2021222/`.
- All four role tests passed with no network capabilities and with available
  `NET_ADMIN`/`NET_BIND_SERVICE`. The separate data, diagnostic and admin
  channels survived setup; the injected host-file descriptor did not.
- The separate native TUN case passed after the final restrictions. Rootless
  rejection, missing namespace setup rights, unavailable Landlock, diverged
  TSYNC state and the x32 denial all passed their required checks.
- Both 30-second fuzz campaigns passed: 83,470 native argument-parser
  executions and 2,492,852 Go plan executions. The race-enabled package tests
  passed with 80.0% Go statement coverage for `linuxprofile` and 77.3% for
  `commandhelper`. Native launcher assertions are additional to those figures.
- Focused `just check` passed in Docker. It ran formatting, spelling, secret
  scanning, shell checks, Linux and Windows amd64/arm64 lint, vet, panel asset
  generation, and the selected profile and command-helper race tests. The
  log is `artifacts/linux-profile-check.log`. The full project test suite was
  not run for this additive component change.
- `just test-total winvm:m20::restricted-command-helper` passed in the ordinary
  user Windows VM. The helper input-pipe change passed all registered command
  tests and fuzz seeds. Evidence is in `artifacts/winvm/run-dSh0ChmG/`.
- Initial runs found that binding a held directory across a mount namespace
  needs a detached mount, that Go requires an open stdin, and that the final
  filter must permit read-only resource-limit queries. The final implementation
  fixes those failures and repeats the native checks. Earlier development
  runs are not used as final qualification evidence.

No host route, DNS, firewall or TUN state was changed. These results qualify
the listed restriction components and primitives; the release blockers above
remain open.
