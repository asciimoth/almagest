# Linux role restriction profiles

Status: implemented and enabled by default for packaged Linux amd64 builds.
`--no-isolation` selects the supported direct composition. Unsupported systems
and incomplete installations fail with explicit direct-mode guidance; they do
not fall back to a weaker profile.

See the [architecture](DAEMON_ISOLATION_ARCHITECTURE.md),
[qualification contract](DAEMON_ISOLATION_QUALIFICATION.md), and
[implementation plan](DAEMON_ISOLATION_IMPLEMENTATION_PLAN.md).

## Trust boundary

The production composition has these roles:

- N is the trusted, node-free native network broker.
- R contains the engine and node code and runs with the full restricted profile.
- C is the optional fixed-target config writer.
- H is the optional fixed-executable command helper.

N keeps only the launch authority required by the selected native backend. It
can use native DNS providers, backend utilities, BPF, process inspection, and
existing-socket marking. It exposes bounded typed operations instead of a
generic host-operation API. R cannot create native sockets or host TUN devices.

The `network` role identifier names N's cgroup. It is not a valid restricted
launcher or `Seal` role. R, C, and H have separate identities, cgroups,
filesystems, policies, and channels.

## Trusted bootstrap

`linuxprofile.ProvisionCgroups` creates fixed `network`, `runtime`, and optional
`writer` and `command` domains below an empty delegated cgroup-v2 instance. It
requires CPU, memory, and task controllers and finite aggregate limits.
Restricted children enter their cgroups with `CLONE_INTO_CGROUP`, before their
first instruction.

After the restricted children start, `linuxprofile.PrepareBackend` moves N into
its bounded cgroup and prepares it in place. It validates held state and cgroup
descriptors, applies hard resource limits, and changes the working directory
through the held descriptor. It does not enter R's namespaces or install R's
filters. An error is fatal because a hard limit can already have changed.

Bootstrap loads protected authority, opens and validates fixed inputs, recovers
the native-resource journal, binds role channels, and publishes admin listeners
only after restriction proofs and application readiness succeed.

## Native restricted launcher

[`bin/native/linux-role/launcher.c`](../bin/native/linux-role/launcher.c) is a
small static Linux amd64 executable. It is never installed setuid and has no
file capabilities. It uses only the authority of trusted bootstrap.

Bootstrap supplies held descriptors instead of path strings:

| Descriptor | Object |
| --- | --- |
| 3 | Connected Unix control stream |
| 4 | Root-owned static role executable |
| 5 | Private role directory owned by the selected role identity |
| 6 | Already limited cgroup-v2 directory |
| 7 | H's fixed static command, when enabled |
| 8 | Native launcher executable |
| 9-14 | Optional fixed data, diagnostic, admin, config, command, and callback lanes |

The launcher independently verifies object types, ownership, permissions, ELF
headers, channel types, cgroup membership, and resource ceilings. It rejects
dynamic executables, root role identities, unknown roles, nonzero capabilities,
unexpected descriptors, and arbitrary environment or path input.

Restriction order:

1. Create private PID, mount, network, IPC, and UTS namespaces.
2. Build a bounded tmpfs root, attach the held role directory, copy only the
   fixed static executables, pivot into the new root, and detach the host root.
3. Apply hard resource limits and an empty stdin. Close setup descriptors.
4. Drop supplementary groups, UID/GID, all capability sets, and the capability
   bounding set. Lock securebits, disable dumpability, and set
   `no_new_privs`.
5. Require Landlock ABI 3 or newer. Permit writes only in the private state and
   temporary directories.
6. Install the Linux amd64 seccomp allowlist before Go starts.
7. The Go role entry applies the final all-thread TSYNC filter before it accepts
   application input. H keeps only the fixed-command execution operation.

The waiting native wrapper has no application channels or writable authority.
It binds the namespace child's lifetime, waits for that child, and does not
provide restart or later-launch operations.

## Current test coverage

Linux isolation uses the normal Docker E2E suite. Every isolation-compatible
case starts the packaged production composition and requires an active
isolation status before it runs application assertions.

The focused `linux_isolation_profile` case adds native launcher checks for:

- R, C, and H identity, namespace, capability, Landlock, and seccomp rules;
- all-thread final sealing and later Go threads;
- malformed arguments, descriptors, role selection, static ELF input, and
  resource ceilings;
- TSYNC divergence and alternate syscall ABI rejection;
- production startup, readiness-gated admin publication, and reported roles.

The normal E2E cases also cover the production network bridge, config writer,
command helper, snapshots, cleanup, and peer-loss behavior. The
`sysnet_dns_debian_resolvconf`, `sysnet_dns_openresolv`, and
`sysnet_dns_systemd_resolved` cases cover native DNS providers. The
`tun_splitter_process_routing` case covers process-mark lifecycle and routing
with the released backend.

Run the combined selected gate with:

```sh
just linux-isolation-profiles
```

Run only the focused profile case with:

```sh
just check e2e:linux_isolation_profile
```

The old checkpoint-specific Docker, QEMU, benchmark, and hostile-handle
harnesses were removed after their decisions were implemented. Their reviewed
results remain in the historical experiment and decision documents. They are
not current release gates.

## Supported scope

The released profile requires Linux amd64, cgroup v2 with delegated CPU,
memory, and task controllers, the reviewed static Go build, Landlock ABI 3 or
newer, and the required namespace and seccomp operations. There is no permissive
fallback. Other architectures, dynamic role binaries, rootless containment,
or materially different Go thread behavior require a separately reviewed
profile.

Persistent storage quotas are outside the released scope. Existing protocol,
process, file-size, memory, CPU, task, descriptor, and queue limits remain
mandatory. Direct mode does not start role processes or use isolation IPC.
