# Linux package contents

This file is a guide for persons who inspect the release artifacts. It is next
to the packages in the release directory and at the root of the portable tar
archive. It is not a payload file in the deb, RPM, or Arch Linux package, so
installation of a native package does not copy this file to the system.

Use one of these commands to inspect a native package without installation:

```text
dpkg-deb --contents almagest_VERSION_linux_ARCH.deb
rpm --query --package --list almagest_VERSION_linux_ARCH.rpm
tar --list --file almagest_VERSION_linux_ARCH.pkg.tar.zst
```

The portable archive omits the `/usr` prefix. Copy its `bin`, `lib`, `etc`, and
`share` directories to the matching system prefix paths. For example, archive
path `libexec/almagest/almagestd` maps to `/usr/libexec/almagest/almagestd`.

## Installed files

| Installed path                                       | Purpose                                                                                 |
| ---------------------------------------------------- | --------------------------------------------------------------------------------------- |
| `/usr/bin/almagest-cli`                              | Administration command-line client.                                                     |
| `/usr/libexec/almagest/almagestd`                    | Main network daemon.                                                                    |
| `/usr/libexec/almagest/almagest-network-role`        | Isolated network runtime role (amd64 only).                                             |
| `/usr/libexec/almagest/almagest-config-writer-role`  | Isolated regular-config writer role (amd64 only).                                       |
| `/usr/libexec/almagest/almagest-command-role`        | Isolated external-command role (amd64 only).                                            |
| `/usr/libexec/almagest/almagest-isolation-storage`   | Prepares, recovers, and rolls back isolated storage (amd64 only).                       |
| `/usr/libexec/almagest/almagest-linux-role-launcher` | Starts trusted Linux isolation roles (amd64 only).                                      |
| `/usr/libexec/almagest/almagest-subprogram-launcher` | Starts sandboxed subprograms (amd64 only).                                              |
| `/etc/almagest/alm.hujson`                           | Regular daemon configuration. Package updates preserve local changes.                   |
| `/etc/almagest/protected_alm.hujson`                 | Protected daemon and sandbox configuration. Package updates preserve local changes.     |
| `/usr/lib/systemd/system/almagestd.service`          | Starts the daemon as root and sends its output to the systemd journal.                  |
| `/usr/lib/sysusers.d/almagest.conf`                  | Defines the administrator group and service accounts used by optional daemon isolation. |
| `/usr/lib/tmpfiles.d/almagest.conf`                  | Defines the persistent and runtime data directories.                                    |
| `/usr/share/almagest/locales`                        | Localized user text and user documentation pages.                                       |
| `/usr/share/almagest/examples`                       | Example workspaces that the daemon can load.                                            |
| `/usr/share/doc/almagest/README.md`                  | Project introduction and basic use information.                                         |
| `/usr/share/licenses/almagest/LICENSE`               | Software license.                                                                       |

The tmpfiles definition creates `/var/lib/almagest` for persistent daemon data
and `/run/almagest` for runtime files, including the default administration
socket. These directories are not regular package payload files.

The package creates the `almagest` system group. Add an administrator to this
group to permit access to the Unix administration socket:

```text
usermod --append --groups almagest USER
```

The user must start a new login session before the new group membership takes
effect. Membership gives full control of Almagest and its network configuration.
An upgrade preserves an existing protected config. On such a system, add
`"UnixGroup": "almagest"` to the protected `Admin` object and restart
`almagestd` before you use the group.

## Build and test

GoReleaser creates Linux amd64 and arm64 archives and deb, RPM, and Arch Linux
packages. Run `just package`. The output is in `dist/` and includes a SHA-256
checksum file, detached OpenPGP signatures, and `ascii-moth.asc`. Import the
key, then verify an artifact before you install or extract it:

```text
gpg --import ascii-moth.asc
gpg --verify almagest_VERSION_linux_ARCH.deb.sig almagest_VERSION_linux_ARCH.deb
```

The primary key fingerprint is
`C5C8 4658 CCFD 7E8E 71DE E933 AF3A E54F C3A3 5C9F`.

`just package` signs release artifacts by default. Use `just package false` for
an unsigned local build that does not require confirmation from a hardware key.
`just test-packaging` uses this unsigned mode automatically. Use
`just test-packaging true` to build signatures and test their verification.

The packages start `almagestd` as root with daemon isolation off. Subprogram
sandbox mode is `auto`, and supported system programs are discovered at startup.
The qualified Linux isolation profiles and native sandbox launcher are amd64
only. The arm64 packages omit their role and launcher binaries. Use direct
daemon mode on arm64 until those security boundaries are ported and qualified.
The systemd unit sends standard output and standard error to the journal with
the `almagestd` identifier. Read the service log with
`journalctl --unit almagestd`. The regular and protected config files are
package-managed config files. A package update keeps a locally changed config
when the package format supports this behavior.

Run `just test-packaging` to build the artifacts and test each architecture and
native package format in a clean Debian, Rocky Linux, or Arch Linux container.
The test installs and reinstalls the package, then exercises the installed
daemon and CLI. It covers config replacement through the API, KV and secret
persistence, packaged example loading, local SOCKS proxy traffic, host-network
egress, and a real DefaultTun path. The DefaultTun check uses private network
namespaces, so the test containers run with `/dev/net/tun` and Docker privileged
mode. Set `RUN_PKG_TESTS=1` to include this suite in a full `just check`. A
selector-based `just check`, such as `just check go:lib/http:TestName`, always
skips the package suite. On a single-architecture host, Docker must have
`binfmt_misc` emulation for the other architecture. Set
`ALMAGEST_PACKAGE_TEST_ARCHES` to a space-separated subset only when you
intentionally run a partial local test. QEMU user-mode emulation can omit the
XFRM and netfilter netlink families. In that environment, the arm64 package test
skips only its DefaultTun routing segment; a capable native arm64 host runs the
complete segment.

## Optional daemon isolation

On amd64, install these root-owned, non-writable files in one directory,
normally `/usr/libexec/almagest`:

- `almagestd`
- `almagest-network-role`
- `almagest-linux-role-launcher`
- `almagest-subprogram-launcher`
- `almagest-config-writer-role`
- `almagest-command-role`
- `almagest-isolation-storage`

The amd64 packages already install this complete set. To enable daemon
isolation, change the service command to use `--isolation=auto`, add the
`almagest-isolation-storage prepare` pre-start command, and complete the storage
migration described below. Do not make the native launcher setuid and do not add
file capabilities to any role binary.

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
