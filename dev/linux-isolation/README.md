# Linux isolation experiment

This directory contains the development-only checkpoint 10 harness. It does
not connect isolation to `almagestd` and does not change direct mode.

Run it only through Docker:

```sh
just linux-isolation-experiment
```

Pass an evidence directory when you must retain results at a specific path:

```sh
just linux-isolation-experiment artifacts/linux-isolation/manual-run
```

The harness builds two static Go probes and a small native launcher. The
launcher is single-threaded while it creates mount, network, IPC, UTS, and PID
namespaces. It then installs a private root, drops to UID/GID 65534, clears all
capability sets and the bounding set, locks securebits, sets `no_new_privs`,
applies Landlock, and installs the first seccomp allowlist before it executes
Go. The Go probe creates four distinct OS threads and installs a second seccomp
filter with `SECCOMP_FILTER_FLAG_TSYNC`. Each thread tests the important denied
operations.

The Docker runtime grants the launcher `CAP_SYS_ADMIN`, `CAP_CHOWN`,
`CAP_SETUID`, `CAP_SETGID`, and `CAP_SETPCAP` for bootstrap. These capabilities
are explicit and exist only in the disposable container. The restricted Go
process verifies that all capability sets and its bounding set are empty. The
native feature profiles never receive `CAP_SYS_ADMIN`.

The feature matrix runs the real `sysnet-linux` constructor with these profiles:

- ordinary UID 65534 with no capabilities;
- `CAP_NET_ADMIN`;
- `CAP_NET_ADMIN` plus `CAP_NET_BIND_SERVICE`;
- `CAP_NET_ADMIN`, `CAP_BPF`, and `CAP_PERFMON`.

Each run uses Docker's private network or no network. The optional TUN device
is passed only to the feature probes. All created TUN resources and network
changes stay inside the disposable container network namespace.

The runtime profile currently supports Linux amd64, Landlock ABI 3 or newer,
and the tested Go amd64 thread-clone flags. A missing required namespace,
Landlock operation, capability drop, seccomp rule, or private-root operation is
a hard failure. The harness does not fall back to a weaker profile.

JSON results and environment metadata are written under
`artifacts/linux-isolation/`, which is ignored by git. See
[`DAEMON_ISOLATION_LINUX_EXPERIMENT.md`](../../docs/DAEMON_ISOLATION_LINUX_EXPERIMENT.md)
for the reviewed result and known compatibility gaps.
