# Windows MSI package

This directory defines the unsigned Almagest MSI. `almagest.wxs` defines the
product, service, dependencies, and retention behavior. `payload.wxs` contains
the generated components for tracked locales and examples. `contract.json`
freezes the identities and package policy.

The first package is for Windows Server 2022 build 20348 on amd64. It does not
support Windows client editions, Windows 11, arm64, or 386. Package availability
does not make Windows a supported Almagest platform. The qualification contract
in `contracts/windows/` controls support status.

## Installed files and retained data

The MSI installs immutable files below `%ProgramFiles%\Almagest`. These
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

The package registers the own-process `almagestd` service as
`Almagest Network Daemon`. It runs as LocalSystem, starts automatically with a
delay, and invokes `almagestd.exe --isolation=off`. The service name cannot
change because the executable uses it in its SCM dispatcher contract.

Recovery restarts the daemon after 5 seconds, 15 seconds, and then 60 seconds.
Later failures repeat the 60-second action. The failure count resets after 24
hours. Recovery also applies to daemon error exits and never reboots Windows.
The machine administration endpoint is `pipe://machine` and requires an elevated
CLI or LocalSystem.

The installer stops the service before it replaces files. It starts the service
after a successful install, repair, or upgrade. Uninstall stops and removes the
service before it removes immutable files.

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
Authenticode signatures. The MSI uses the WiX v4 DifxApp extension to install
the unchanged signed driver package. It keeps the INF demand-start setting and
does not explicitly start the driver during installation. Windows does not
start it at boot. The daemon has no hard dependency on the driver and can run
without executable-tree exclusions.

Normal uninstall removes the driver package and its copied driver files after
the daemon stops. Windows Installer rollback removes a driver package that was
added by a failed installation. The staged INF, SYS, and CAT files under
Program Files are also removed. ProgramData remains in place.

WiX v4 marks DifxApp as deprecated, but it is the open-source WiX v4 facility
that supplies transactional driver-package install, uninstall, and rollback.
The package gate tests this fixed choice on Windows Server 2022 before release.

## WiX source maintenance

Run this command after a tracked locale or example is added, removed, or moved:

```sh
python3 packaging/windows/generate-payload-wxs.py
```

The generator uses the UUIDv5 namespaces in `contract.json`. A file keeps its
component GUID while its content changes. A path change gets a new component
GUID. The contract test fails if `payload.wxs` is not current.

`almagest.wxs` requires WiX preprocessor values for `Version`, `ProductCode`,
`SourceDir`, `PayloadDir`, `WintunDir`, and `MullvadDir`. `ProductCode` must be
stable for all builds of one product version and must change for a new product
version. Phase 5 supplies these values from the package build manifest.

On a Windows host with WiX v4 and the pinned Util and DifxApp extensions, use
the fixed fixture to validate the source and build a test MSI:

```powershell
./packaging/windows/build-fixture.ps1 `
  -WixExecutable C:\tools\wix\wix.exe `
  -Output C:\temp\almagest-fixture.msi
```

The fixture is for compiler validation only. Do not install it.

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

## Operator command line

The package is unsigned. Windows can show an unknown-publisher or SmartScreen
warning. Use these commands with one explicit MSI path:

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
