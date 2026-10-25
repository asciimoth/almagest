# Windows MSI package

This directory defines the contract for the planned unsigned Almagest MSI. The
MSI is not implemented yet. `contract.json` freezes its identities, paths,
service properties, logging, retention behavior, and dependency inputs.

The first package is for Windows Server 2022 build 20348 on amd64. It does not
support Windows client editions, Windows 11, arm64, or 386. Package availability
does not make Windows a supported Almagest platform. The qualification contract
in `contracts/windows/` controls support status.

## Installed files and retained data

The MSI will install immutable files below `%ProgramFiles%\Almagest`. These
files include `almagestd.exe`, `almagest-cli.exe`, the official amd64
`wintun.dll`, locales, examples, package documentation, and a staging directory
for the complete Mullvad split-tunnel driver package.

The daemon uses these mutable paths:

| Purpose                 | Path                                          |
| ----------------------- | --------------------------------------------- |
| Editable configuration  | `%ProgramData%\Almagest\alm.hujson`           |
| Protected configuration | `%ProgramData%\Almagest\protected_alm.hujson` |
| Storage                 | `%ProgramData%\Almagest\store`                |
| Logs                    | `%ProgramData%\Almagest\logs`                 |

Repair and upgrade must not replace existing configuration or state. A normal
uninstall removes the service and immutable files but keeps all files under
`%ProgramData%\Almagest`. There is no purge option in the first package.

## Service

The package will register the own-process `almagestd` service as
`Almagest Network Daemon`. It runs as LocalSystem, starts automatically with a
delay, and invokes `almagestd.exe --isolation=off`. The service name cannot
change because the executable uses it in its SCM dispatcher contract.

Recovery restarts the daemon after 5 seconds, 15 seconds, and then 60 seconds.
Later failures repeat the 60-second action. The failure count resets after 24
hours. Recovery also applies to daemon error exits and never reboots Windows.
The machine administration endpoint is `pipe://machine` and requires an elevated
CLI or LocalSystem.

The installer must stop the service before it replaces files. It must start the
service after a successful install, repair, or upgrade. Uninstall must stop and
remove the service before it removes immutable files.

During startup and shutdown, the daemon reports a new SCM checkpoint every two
seconds and a 30-second wait hint. Stop and system-shutdown controls cancel the
same daemon context. Normal cleanup has at most two minutes to finish before the
service returns a failure to SCM. Startup and cleanup errors also return a
failure, but an expected stop that completes normally returns success. The
daemon reports `Running` only after the admin API and engine are ready.

## Service log

An SCM service writes its detailed operator stream to
`%ProgramData%\Almagest\logs\almagestd.log`. The sink starts before argument
parsing and runtime dependency checks, so startup failures are retained. Log
timestamps use UTC. A file rotates at 20 MiB and the daemon keeps five old
files (`almagestd.log.1` through `almagestd.log.5`). The current file and its
directory grant access only to LocalSystem and administrators.

The daemon writes synchronously to this sink and flushes it during normal
shutdown. A normal uninstall keeps the log directory and all rotated files.
An interactive daemon continues to write to its console and does not open the
service log.

## Runtime dependencies

The pinned package inputs are:

- Wintun 0.14.1: the unchanged official amd64 `wintun.dll` from the official ZIP
  archive. The DLL is installed beside `almagestd.exe`. The MSI must not contain
  separate Wintun SYS, INF, or CAT files.
- Mullvad split-tunnel driver 1.3.0.0: the unchanged, matching amd64 SYS, INF,
  and CAT files at binary revision `5b6f46cde692acb77ee74b37b9fd3f1678c45a52`.
  Windows driver-package facilities must install the package. The daemon service
  has no hard service dependency on the driver.

`contract.json` records all source URLs and SHA-256 values. The values match the
`windows-wintun` and `windows-split-tunnel-drivers` outputs in `flake.nix`. Run
the dependency verifier on Windows before a package build:

```powershell
./packaging/windows/verify-dependencies.ps1 `
  -WintunDirectory C:\inputs\wintun\amd64 `
  -MullvadDirectory C:\inputs\split-driver\amd64
```

The verifier requires amd64 PE files, exact hashes, a matching INF, and valid
Authenticode signatures. Phase 4 must test and record the driver start and
uninstall policies. It must not remove the daemon's ability to run without
executable-tree exclusions when the driver is unavailable.

## Redistribution record

This section records engineering requirements and is not legal advice.

The Wintun 0.14.1 ZIP includes a prebuilt-binaries license. It permits the
unchanged DLL to be distributed with software that uses it only through the
permitted Wintun API. The package must include that license, must not modify the
DLL, and must not use Wintun or WireGuard names as an endorsement. The official
site identifies the signed DLL as the supported distribution unit.

The Mullvad driver source offers GPL-3.0-or-later or MPL-2.0. Almagest selects
MPL-2.0 for the separately installed, unchanged driver files. The package must
include the MPL-2.0 text and a notice that identifies the exact source revision
in `contract.json`. Release publication must keep the corresponding source
available by reasonable means. The Mullvad names and marks are not an
endorsement of Almagest.

Relevant upstream records:

- <https://www.wintun.net/>
- <https://git.zx2c4.com/wintun/about/?h=0.14.1>
- <https://github.com/mullvad/mullvadvpn-app-binaries/tree/5b6f46cde692acb77ee74b37b9fd3f1678c45a52>
- <https://github.com/mullvad/win-split-tunnel/tree/0a0eb97f67d1dbcb3d08bda66d3b24f465d95475>

## Planned command line

The package will be unsigned. Windows can show an unknown-publisher or
SmartScreen warning. Interactive commands are shown now so later phases cannot
change the operator contract without review. They will work after the MSI is
built:

```powershell
# Install and write a verbose Windows Installer log.
msiexec.exe /i .\almagest_VERSION_windows_amd64.msi /qn /norestart /l*v .\install.log

# Repair the same version without replacing ProgramData state.
msiexec.exe /fa .\almagest_VERSION_windows_amd64.msi /qn /norestart /l*v .\repair.log

# Read the durable daemon log after Phase 2.
Get-Content "$env:ProgramData\Almagest\logs\almagestd.log" -Tail 200

# Uninstall but retain ProgramData state.
msiexec.exe /x .\almagest_VERSION_windows_amd64.msi /qn /norestart /l*v .\uninstall.log
```

These command examples do not authorize selecting an MSI through an ambiguous
wildcard. Use one explicit package path in automation.
