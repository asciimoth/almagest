# Linux isolated deployment

Install these root-owned, non-writable files in one directory, normally
`/usr/libexec/almagest`:

- `almagestd`
- `almagest-network-role`
- `almagest-linux-role-launcher`
- `almagest-subprogram-launcher`
- `almagest-config-writer-role`
- `almagest-command-role`
- `almagest-isolation-storage`

Install `almagest.sysusers`, `almagest.tmpfiles`, and `almagestd.service` in the
distribution's systemd directories. Run `systemd-sysusers` and
`systemd-tmpfiles --create` before the first start. Do not make the native
launcher setuid and do not add file capabilities to any role binary.

Install the packaged `share/almagest/examples` tree as root-owned, non-writable
files at `/usr/share/almagest/examples`. Keep each example unpacked. The network
role gives this tree to the runtime as a separate read-only mount.

For strict subprogram sandboxes in direct or isolated daemon mode, give the
trusted daemon or sandbox supervisor a separate delegated cgroup v2 subtree with
the CPU, memory, and PID controllers enabled. Set protected
`Sandbox.LinuxCgroupRoot` to that subtree and set `Sandbox.LinuxLauncher` to the
installed `almagest-subprogram-launcher`. The daemon probes both authorities
before it reports the Linux full profile. In isolated mode, the network role
starts and supervises the sandbox role and binds its private service lanes to
the exact runtime process. This native subprogram profile is qualified only on
amd64. Other Linux architectures fail strict preflight until they have an
architecture-specific launcher and qualification suite.

Resource ceilings are set at startup. Defaults per role are 8 GiB memory, 4096
tasks, and 64 CPUs of quota. Each process can open up to 65536 file descriptors,
and each file can grow to 1 TiB, subject to inherited hard limits. Use service
drop-in `Environment=` entries to set `ALMAGEST_ISOLATION_MEMORY_BYTES`,
`ALMAGEST_ISOLATION_TASKS`, `ALMAGEST_ISOLATION_CPU_QUOTA`,
`ALMAGEST_ISOLATION_DESCRIPTORS`, or `ALMAGEST_ISOLATION_FILE_BYTES`. Run
`systemctl daemon-reload` and restart `almagestd` after a change. See
[daemon isolation](../../docs/DAEMON_ISOLATION.md#configuration-and-command-line)
for ranges, units, CLI equivalents, and parent cgroup capacity requirements.

`ExecStartPre` prepares a fresh layout or migrates the stopped legacy
`/var/lib/almagest/store` layout. It keeps the original tree as
`store.pre-isolation` and keeps an identity-bound migration receipt. It also
copies the old regular config to the separate writer-owned directory. The
root-owned protected config remains under `/etc/almagest`.

For interrupted setup, keep the daemon stopped and run:

```text
almagest-isolation-storage recover
```

To return storage to direct mode, first recover all native resources and make
sure the broker journal is empty. Then stop the service and run:

```text
almagest-isolation-storage rollback
almagestd --no-isolation --config /etc/almagest/alm.hujson
```

Rollback refuses to remove an unproved tree or a non-empty broker journal. It
does not silently discard host-effect recovery evidence. Package updates must
replace all listed binaries atomically while the service is stopped. A partial
binary set fails trusted-path or protocol validation; it never falls back to
direct mode.
