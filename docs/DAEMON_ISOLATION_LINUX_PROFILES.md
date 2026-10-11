# Linux role restriction profiles

Checkpoint 24 implements Linux amd64 profile components. The shipping daemon
still uses direct mode; complete isolated daemon assembly and qualification
remain later gates. See the [implementation plan](DAEMON_ISOLATION_IMPLEMENTATION_PLAN.md)
and [qualification contract](DAEMON_ISOLATION_QUALIFICATION.md).

## Trust boundary

R contains the engine and node code and remains fully restricted. N runs the
trusted native backend with its launch identity, namespaces, filesystem view,
and available authority. This permits native DNS providers, backend-selected
utilities, BPF, process inspection, and existing-socket marking. N exposes no
generic host-operation API and executes no node code or application callbacks.
C and node-command H keep their separate restrictions.

The former restricted-N launcher path has been removed. The `network` role
identifier names a cgroup, not a valid launcher or Seal role. No separate DNS
or process helper, upstream API redesign, or reduced backend feature set is
required. Preparation never grants authority missing at launch.

## Trusted backend preparation

`linuxprofile.PrepareBackend` prepares the node-free bootstrap process in
place, after restricted children start and before it accepts their requests.
It does not exec, enter namespaces, change credentials, or install R's filters.

It validates held descriptors for a private state directory owned by N and a
cgroup v2 that contains N. Memory, swap, task, and CPU limits must meet finite
ceilings. It sets hard core, descriptor, and file-size limits, and changes cwd
through the state descriptor. It preserves MEMLOCK for BPF loading. The Go
limit setter also updates the descriptor-limit state used by os/exec.
Preparation errors are fatal: a hard limit may already have changed.

`ProvisionCgroups` creates fixed `network`, `runtime`, and optional `writer`
and `command` domains below an empty delegated cgroup-v2 instance. It requires
CPU, memory, and task controllers and sufficient aggregate limits. Each role
has finite limits and group-OOM handling. CPU weights favor N over R/C/H.
Restricted children enter their held cgroups with `CLONE_INTO_CGROUP`.
Bootstrap enters the network cgroup after child launch, then prepares N.
Cleanup removes only empty cgroups that still match the held device/inode.

Separate identities and persistent storage quotas remain deployment inputs.
A per-file size limit is not a disk quota.

## Restricted launcher: R, C, and H only

The launcher is static Linux amd64 code. Never install it setuid or with file
capabilities. It uses only caller authority. Unsupported rootless containment
fails with explicit `--no-isolation` guidance. The tested build uses static
Go 1.26 binaries; other architectures and build modes need separate evidence.

Trusted bootstrap supplies these held descriptors:

| Descriptor | Object |
| --- | --- |
| 3 | Connected Unix control stream |
| 4 | Root-owned static role executable, not writable by others |
| 5 | Private role directory owned by its selected UID/GID |
| 6 | Already limited cgroup v2 directory |
| 7 | H's fixed static command, when used |
| 8 | Native launcher executable |
| 9 | Optional R data stream |
| 10 | Optional diagnostic stream |
| 11 | Optional R admin stream |

Bootstrap validates paths before opening objects. The launcher checks types,
ownership, permissions, ELF headers, channels, limits, and cgroup membership
again. It rejects dynamic binaries, root identities, unknown roles, and
nonzero capabilities. The reserved capability field remains zero in the v1
argument contract. There is no arbitrary environment or executable-path field.

The Go adapter executes the held launcher through `/proc/self/fd/8`, with an
empty environment and fixed cwd. Cgroup placement occurs before its first
instruction, with no retry outside the cgroup. The launcher validates output
streams, installs an EOF stdin pipe, and closes unrelated descriptors,
including those above the new limit. Bootstrap closes its child-channel copies.

Restriction order:

1. Verify bounded cgroup membership and create private PID, mount, network,
   IPC, and UTS namespaces.
2. Make mount propagation private. Build a bounded tmpfs root with the fixed
   executable, held role storage at `/state`, and private `/tmp`. Storage is
   nodev, nosuid, and noexec; nested mounts and the old root are not imported.
   There is no host proc, sys, run, etc, or device view.
3. Apply hard resource limits. Clear supplementary groups, install nonroot
   UID/GID, drop all capabilities including bounding and ambient sets, lock
   securebits, and set no-new-privileges.
4. Require Landlock ABI 3 or newer. Allow execution only from fixed executable
   files and writes only in approved private directories.
5. Install the seccomp allowlist before Go starts. Allow the exact Go thread
   clone flags; return ENOSYS for clone3. Deny socket creation, namespace and
   mount changes, tracing, process-memory access, descriptor theft, BPF, perf,
   keyrings, io_uring, and unapproved device ioctls.
6. The trusted role entry calls `Seal` before application input or construction.
   It resets dumpability and removes exec across existing Go threads with
   TSYNC. Future threads inherit restrictions. H retains only the execution
   ability needed by its fixed command dispatcher.

Trusted package initialization must not read regular config, snapshots, or
runtime-controlled environment before Seal. Seal alone is not a sandbox.
The waiting native parent closes channels and drops all capabilities. It has
no restart or later launch operation.

R can use private storage, bbolt, timers, garbage collection, and inherited
streams. C can replace only its provisioned config target. H can execute its
fixed static command under inherited restrictions and bounded command policy.
Dynamic commands or additional resources require a separately reviewed profile.

## Test coverage and limits

| Test group | Evidence provided |
| --- | --- |
| Native R/C/H tests | All-thread restrictions, later threads, capability removal with and without inherited network rights, namespaces, syscall/path denials, limits, bbolt, config replacement, fixed commands |
| Rejection tests | Invalid roles including network, malformed descriptors/ELF/arguments, missing setup rights or Landlock, rootless launch, TSYNC failure, x32 rejection |
| PrepareBackend | Eight locked threads retain identity, capabilities, namespaces, and filter state; hard limits, cwd, native sockets, and utility execution; ordinary-user launch |
| Cgroup provisioner | Separate bounded role domains and safe cleanup in a disposable private cgroup namespace |
| TrustedSystem | Equal-authority direct/prepared comparison: direct DNS, OutDNS, regular/default TUN, routes, bypass marks, MTU rebuild, owner matching, cleanup |
| TrustedComposition | Fully restricted R with real N: TCP/UDP, owner matching, TUN, bounded OutDNS and reverse application-DNS relays, session-loss cleanup |
| Restricted C composition | Real static C entry point, post-launch all-thread seal, bounded readiness exchange, control-only config transfer, fixed-target read/replace, and peer-loss exit |
| Upstream DNS matrix | All ten sysnet-linux v0.10.1 provider cases, including fallback and dynamic provider behavior |
| p-mark guest | Released p-mark v0.5.0 guest cases for lifecycle, sockets, exact kernel policy, 64 immediate-exec probes, and churn |
| Race and fuzz tests | Profile plans, native arguments, backend cgroup values, DNS packets and application responses, plus broker/runtime/transport suites |

The DNS matrix and p-mark guest run upstream integration fixtures. They do not
run PrepareBackend or the complete restricted daemon. TrustedComposition uses
one bounded fixture cgroup, not the final per-role deployment. These groups
provide complementary component evidence, not a complete production manifest.

The p-mark probe runs in the guest's initial PID namespace as a child of init.
Kernel tracepoint TGIDs therefore match its procfs view. A private container
PID namespace is not equivalent evidence. The guest has no network device and
does not share the development host's PID or network namespace.

No downstream dependency patch is used. p-mark v0.5.0 supplies the standalone
guest runner. sysnet-linux v0.10.1 contains consumer E2E checks for automatic
p-mark construction, default-TUN routing, user marks, repeated probes, and
teardown. The local wrapper selects evidence directories, restores executable
bits that the Go module cache does not preserve for the E2E scripts, and runs
the released p-mark source in a disposable guest. It does not change dependency
source content. TrustedSystem and TrustedComposition separately exercise the
released sysnet constructor, routing, matcher, and runtime integration paths.

## Running the gate

Run `just linux-isolation-profiles`. Separate runners are
`dev/linux-profiles/run-dns-matrix.sh` and
`dev/linux-profiles/run-pmark-guest.sh`.

Requirements include Docker, TUN, QEMU, Go, timeout, and sha256sum. The p-mark
runner uses KVM when available and otherwise uses QEMU TCG. Its builder image
supplies the Debian kernel and initramfs tools. Missing requirements fail the gate, not skip it.
Network changes run only in disposable Docker networks or the networkless VM.
Logs are written under `artifacts/linux-profiles/<run-id>/`.

The 2026-10-10 cleanup checks passed native R/C/H and trusted-backend tests,
race tests, five 30-second fuzz campaigns, and all ten DNS cases. Evidence is
under `artifacts/linux-profiles/cleanup-review/`. Focused `just check` passed
(`artifacts/cleanup-check.log`); Windows VM script lint was rerun successfully
with its required analyzer setting (`artifacts/cleanup-winvm-lint.log`).
Shell checks passed after the final runner edit. No native Windows test ran.

After updating to p-mark v0.5.0 and sysnet-linux v0.10.1, all four released
p-mark guest cases passed under
`artifacts/linux-profiles/upstream-pmark-v050-final/`. All ten DNS provider
cases passed under `artifacts/linux-profiles/sysnet-v0101-dns/`. The temporary
proposal, custom guest init, and sysnet source patch are no longer present.
The direct/prepared native comparison and restricted-runtime composition also
passed with v0.10.1 under `artifacts/linux-profiles/v0101-native/`. Focused
`just check`, including Windows cross-lint and the affected race tests, passed.

Native network composition now uses the durable broker journal and requires it
to be empty after peer-loss cleanup. The Linux native reconciler resolves an
absent TUN after restart and deliberately leaves a still-present interface for
inspection because an interface index can be reused.

Remaining release gates include complete runtime/admin service assembly,
deployment identities and storage quotas, the full
feature/hostile-runtime manifest and other platform/build targets. Performance
is accepted and is not a profile gate.
All supported backend features, including eBPF process filtering, remain
required. Direct mode has no isolation launcher or internal IPC overhead.
