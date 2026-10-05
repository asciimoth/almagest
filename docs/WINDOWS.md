# Windows

Almagest has a native Windows backend, but no Windows platform entry is
qualified as supported yet. The authoritative feature mapping and required
test evidence are in
[`contracts/windows/support-matrix.json`](../contracts/windows/support-matrix.json)
and
[`contracts/windows/test-manifest.json`](../contracts/windows/test-manifest.json).
A cross-build checks portability only. It does not qualify native behavior.

## Deploy

Build the pinned runtime inputs with:

```sh
nix build .#windows-runtime-dependencies
```

Deployment tooling must do these tasks:

- Put the architecture-matched `wintun.dll` beside `almagestd.exe` or in
  `System32`. Only administrators and the deployment identity must be able to
  write the selected directory.
- Install the matching Mullvad split driver as the demand-start
  `mullvad-split-tunnel` kernel service. Its driver path is
  `%SystemRoot%\System32\drivers\mullvad-split-tunnel.sys`.
- Own dependency installation, service registration, start, update, repair,
  and removal. The daemon validates dependencies but does not provision them.

The split driver is an exclusive machine resource. Do not run another VPN
controller that uses it at the same time. If it is absent, busy, or unusable,
executable-tree exclusions are unavailable. Ordinary TUN, OutNet, OutDNS,
LocalNet, and best-effort process matchers remain independent of it.

Network changes require an elevated administrator or service identity. This
includes adapter, route, MTU, DNS, WFP, and split-driver operations. Use Local
System for Service Control Manager operation and the default machine
administration pipe. Service registration stays outside the daemon.

## Paths and private state

The default configuration is `%ProgramData%\Almagest\alm.hujson`; its protected
configuration is `protected_alm.hujson` in the same directory. File storage
defaults to these locations:

| Execution mode | Storage |
| --- | --- |
| Interactive | `%LocalAppData%\Almagest\store` |
| Windows service | `%ProgramData%\Almagest\store` |

Explicit config and storage arguments override these defaults. Unless
`--unsafe-config` is used, the daemon creates and validates private Windows
ACLs for config, storage, generated secrets, and temporary state.

## Local administration

The default CLI endpoint is `pipe://user`. An interactive daemon uses a pipe
named for the current user SID:

```text
\\.\pipe\almagest-admin-user-<SID>
```

The pipe permits the current identity and Local System. The CLI requires the
current identity to own it.

A Local System service uses `\\.\pipe\almagest-admin`. Its ACL permits Local
System and elevated administrators. Connect from an elevated CLI with
`pipe://machine`; the CLI requires Local System to own the pipe. User and
machine defaults are separate.

Set `Admin.Pipe`, `--pipe`, or `ALMAGEST_PIPE_ADMIN` to a complete local pipe
path to change the daemon endpoint. An empty value disables the pipe. Use
`pipe://user/<name>` or `pipe://machine/<name>` to select the same custom name
in the CLI. Pipe names must have one component. The daemon rejects remote names
and fails if another server already owns the name.

The loopback web listener is disabled by default. Enable it with `Admin.Web`,
`--web`, or `ALMAGEST_WEB_ADMIN`. `Admin.WebFilters.Usernames`,
`--web-filter-user`, and `ALMAGEST_WEB_ADMIN_FILTER_USERS` restrict accepted
connections by the owning process token SID. UID and GID filters are invalid
on Windows.

The username filter is best effort, not authentication. Without it, any local
user can access the listener, and the daemon logs a warning. Keep
`AllowCrossOrigin` disabled. Prefer the named-pipe API for local CLI access.

## Certificate authorities

The daemon reads public certificates from the Root stores of its current
process identity and the local machine. A service does not read the interactive
user's store. The catalog then includes the embedded fallback roots. Set
`ALMAGEST_TLS_FORCE_FALLBACK_ROOTS` to skip Windows store access.

Catalog entries are candidates only. A TLS policy must explicitly select one.
Almagest does not reproduce Windows chain policy, distrust, group policy, or
application-specific exceptions. `almagest-cli tls reload-os-certs` refreshes
the catalog without a daemon restart. A failed refresh leaves the current
catalog unchanged.

## Build and test

Build both Windows architectures or one selected architecture:

```sh
just build-windows
just build-windows-arch amd64
just build-windows-arch arm64
```

Output goes to `artifacts/windows` by default. Set `WINDOWS_BUILD_DIR` to
change it. The build covers all Go modules, the daemon, the CLI, default and
`almagest_e2e` test binaries, the certificate catalog, and the embedded panel.

Use these native gates:

```sh
just test-windows-vm
just test-windows-e2e
just test-windows-flow EVIDENCE
just test-windows-resource
just qualify-windows ENTRY EVIDENCE
```

See [`dev/winvm/README.md`](../dev/winvm/README.md) for the disposable Windows
Server 2022/amd64 harness and evidence format. Windows 11/amd64 and Windows
11/arm64 require separate native runs. Emulation does not qualify arm64.
