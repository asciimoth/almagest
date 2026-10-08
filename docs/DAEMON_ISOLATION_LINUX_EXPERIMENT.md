# Daemon isolation Linux experiment

Status: checkpoint 10 implementation and one Linux amd64 qualification run are
complete. This is development evidence, not a production isolation profile.
The production daemon remains unchanged and requires `--no-isolation`.

## Scope and reproduction

The harness is in [`dev/linux-isolation`](../dev/linux-isolation/README.md).
Run it with:

```sh
just linux-isolation-experiment
```

All restriction, TUN, BPF, DNS-provider, and native-network experiments run in
disposable Docker containers. The harness saves raw JSON and environment data
under `artifacts/linux-isolation/<run-id>/`.

The qualified runtime profile uses a single-threaded C launcher before the Go
runtime starts. It creates private mount, network, IPC, UTS, and PID namespaces,
pivots to a tmpfs root, detaches the old root, and exposes only the static probe,
private writable storage, private temporary storage, and one inherited Unix
sequence-packet endpoint. It does not mount `/proc`, `/sys`, `/run`, `/etc`, or
`/dev`.

The launcher then does the following operations in order:

1. It clears supplementary groups and drops to UID/GID 65534.
2. It clears effective, permitted, inheritable, ambient, and bounding
   capabilities and locks securebits.
3. It sets `no_new_privs`.
4. It queries Landlock and requires ABI 3 or newer so that truncate operations
   are mediated. It grants read/execute only to the probe and grants application
   writes only below `/storage` and `/tmp`.
5. It installs a seccomp allowlist before `execve`. The allowlist has the exact
   amd64 flags that the tested Go runtime uses to create threads. `clone3`
   returns `ENOSYS` so Go can use its reviewed fallback.
6. The Go probe locks four goroutines to four distinct OS threads and installs
   a final filter with seccomp TSYNC. This removes `execve` from every thread.

The launcher receives `CAP_SYS_ADMIN` and credential-management capabilities
only for namespace and identity bootstrap in the disposable container. The
runtime verifies that it retains none. No native feature profile receives
`CAP_SYS_ADMIN`.

## Qualified result

The accepted run used source revision `5203d0b23a216ac2166a543fd8caa5683b1f218f`
plus the checkpoint 10 working change, Docker 29.8.1, Linux 7.2.8 x86-64,
Landlock ABI 10, and Go 1.26.6. The command was
`./dev/linux-isolation/run.sh`. The retained local evidence directory was
`artifacts/linux-isolation/20261008T234536Z-3703992`; later validation runs can
replace this ignored directory without changing the recorded result.

Ordinary Go allocation, garbage collection, timers, writable-storage creation,
rename, truncate, and inherited Unix-channel I/O succeeded. The following
operations failed from every applicable restricted thread:

- reads outside the Landlock view and access to host files, DNS helpers, and
  `/dev/net/tun`;
- IPv4 stream and raw-socket creation, BPF, mount, namespace entry, and other
  host administration;
- fork, exec, ptrace, process-memory reads, `pidfd_open`, and `pidfd_getfd`;
- access through an unknown descriptor.

The process was PID 1 in its PID namespace, used the private UTS identity, had
`no_new_privs` and seccomp mode 2, and had empty capability and supplementary
group sets. Four distinct Go OS threads independently observed the Landlock and
seccomp denials after the TSYNC filter was installed.

## Native feature matrix

The matrix uses the real `sysnet-linux` v0.8.1 constructor. `available` below
means that the direct syscall probe succeeded inside the disposable container.

| Profile | TCP socket | TUN | Raw socket | BPF map | Route netlink | Backend result |
| --- | --- | --- | --- | --- | --- | --- |
| UID 65534, no capabilities | available | denied | denied | denied | opens | ordinary outbound/local operations remain available; TUN, routing, and DNS control report unavailable |
| `CAP_NET_ADMIN` | available | available | denied | denied | opens | ordinary TUN operations become available; default-TUN operations remain partly unavailable |
| `CAP_NET_ADMIN` + `CAP_NET_BIND_SERVICE` | available | available | denied | denied | opens | same result in this container; the low-port distinction is not qualified because the container permits unprivileged low ports |
| `CAP_NET_ADMIN` + `CAP_BPF` + `CAP_PERFMON` | available | available | denied | available | opens | BPF creation works without `CAP_SYS_ADMIN`; default-TUN operations remain partly unavailable |

This matrix confirms that missing TUN or BPF authority does not remove ordinary
socket support. A route-netlink socket opening is not proof that route mutation
is authorized.

## Compatibility gaps and helper requirements

- The current `sysnet-linux` route monitor enters another network namespace
  while it creates a netlink socket. It failed without `CAP_SYS_ADMIN` in every
  partial-capability profile. `CAP_SYS_ADMIN` must not be retained as a
  workaround. The backend must avoid this namespace entry when N already uses
  the host network namespace, or route monitoring must move to a narrowly
  reviewed helper. Until then, the full Linux native broker profile is not
  qualified.
- The container had neither the system D-Bus socket nor a `resolvconf` helper.
  The root feature profiles detected direct `/etc/resolv.conf` control, but N
  must not receive general `/etc` write access. Production DNS control needs
  the fixed-method D-Bus/helper or fixed-target DNS writer described by the
  architecture. This container does not qualify those host-specific providers.
- The runtime seccomp clone rule is Linux amd64 and Go-version specific. Other
  architectures and material Go runtime changes require a new traced and
  reviewed allowlist. There is no permissive fallback.
- Landlock ABI below 3 is rejected because it cannot mediate truncate. An older
  kernel can be supported only by a separately reviewed equivalent profile.
- This run qualifies the privileged bootstrap profile. It does not qualify an
  unprivileged launcher on hosts without delegated namespace or cgroup support.
- Cgroup limits, a dedicated installed account, broker filesystem restrictions,
  and the production role launcher remain later implementation work. This
  experiment does not claim that isolated daemon mode exists.

These gaps are explicit experiment results. They do not reduce direct-mode
features and do not cause elevation, an unrestricted helper, or hidden native
fallback.
