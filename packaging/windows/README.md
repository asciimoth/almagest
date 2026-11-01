# Windows MSI package

This directory defines the unsigned Almagest MSI. `almagest.wxs` defines the
product, service, dependencies, and retention behavior. `payload.wxs` contains
the generated components for tracked locales and examples. `contract.json`
freezes the identities and package policy.

The first package supports Windows Server 2022 build 20348 on amd64. This target
is manually tested. The package does not support Windows client editions,
Windows 11, arm64, or 386. The support contract in `contracts/windows/` records
the platform status and feature limits.

## Installed files and retained data

The MSI installs immutable files below `%ProgramFiles%\Almagest`. These files
include `almagestd.exe`, `almagest-cli.exe`, the optional `almagest-app.exe` and
its `WebView2Loader.dll`, the official amd64 `wintun.dll`, locales, examples,
package documentation, and a staging directory for the complete Mullvad
split-tunnel driver package.

The installer adds `%ProgramFiles%\Almagest` to the machine `PATH`. New command
prompts can run the daemon and CLI by name. When the desktop app is installed,
the installer also creates Start menu and public desktop shortcuts.

## Desktop app installer choices

An interactive install selects the desktop app and its sign-in autostart entry
by default. The feature tree lets the user disable autostart, or disable the
desktop app and install only the required daemon service and command-line tools.
The default autostart command is:

```text
"%ProgramFiles%\Almagest\almagest-app.exe" --background
```

This command starts the app with its window hidden and keeps it available in the
notification area. For a silent service-only install, select only the root and
backend features:

```powershell
msiexec.exe /i almagest_VERSION_windows_amd64.msi /qn `
  ADDLOCAL=AlmagestFeature,BackendFeature
```

To install the app without autostart, include `DesktopAppFeature` and omit
`DesktopAppAutoStartFeature` from `ADDLOCAL`.

## Installer license

An interactive install shows the project license before feature selection. The
package build converts the repository `LICENSE` file to deterministic RTF for
the Windows Installer dialog. The MSI also installs the original text as
`%ProgramFiles%\Almagest\LICENSE.txt`.

The daemon uses these mutable paths:

| Purpose                 | Path                                          |
| ----------------------- | --------------------------------------------- |
| Editable configuration  | `%ProgramData%\Almagest\alm.hujson`           |
| Protected configuration | `%ProgramData%\Almagest\protected_alm.hujson` |
| Storage                 | `%ProgramData%\Almagest\store`                |
| Logs                    | `%ProgramData%\Almagest\logs`                 |

Repair and upgrade must not replace existing configuration or state. A normal
uninstall removes the service and immutable files but keeps all files under
`%ProgramData%\Almagest`. It also keeps the `Almagest Administrators` local
group and its administrator-managed membership. There is no purge option in the
first package.

Install, repair, and upgrade reconcile the retained ProgramData owner and ACL
before the service starts. They remove stale access entries and grant full
control only to LocalSystem and built-in administrators. This repair keeps all
configuration, logs, and daemon state.

## Administration access

The package creates the local `Almagest Administrators` security group. The
LocalSystem-owned machine administration pipe permits LocalSystem, built-in
administrators, and members of this group. Group membership grants access to the
Almagest administration API. It does not grant Windows service-control or file
permissions.

An interactive install selects **Allow current user to use the admin API** by
default. This option adds the user who started the installer to the local
`Almagest Administrators` group. Clear the option in the feature tree if the
current user must not get access. For a silent install, omit
`CurrentUserAdminAccessFeature` from `ADDLOCAL` to disable this access. An
unattended install that runs as LocalSystem does not add LocalSystem to the
group. Set `ALMAGEST_ACCESS_SID` to the intended user SID when deployment
software runs the MSI as LocalSystem and selects this feature.

Add a local or domain principal with an elevated PowerShell session:

```powershell
Add-LocalGroupMember -Group 'Almagest Administrators' -Member 'CONTOSO\Almagest Operators'
```

Windows puts group membership in an access token when the user signs in. After
installation, the user must sign out and sign in again before an unelevated app
or CLI can use the pipe. A Windows restart also creates a new access token, but
sign-out and sign-in is sufficient.

## Service

The package registers the own-process `almagestd` service as
`Almagest Network Daemon`. It runs as LocalSystem, starts automatically, and
invokes `almagestd.exe --isolation=off`. The service name cannot change because
the executable uses it in its SCM dispatcher contract.

Recovery restarts the daemon after 5 seconds, 15 seconds, and then 60 seconds.
Later failures repeat the 60-second action. The failure count resets after 24
hours. Recovery also applies to daemon error exits and never reboots Windows.
The machine administration endpoint is `pipe://machine`. LocalSystem, an
elevated administrator, or a signed-in member of `Almagest Administrators` can
connect to it.

Windows Installer 5.0 fails its standard variable-delay service recovery table
on Windows Server 2022. The package therefore uses deferred, non-impersonated
`sc.exe failure` actions to set the three delays. A paired rollback action
restores the package policy if the install transaction fails. The MSI smoke gate
checks the resulting SCM values.

The installer stops the service before it replaces files. It starts the service
after a successful install, repair, or upgrade. Uninstall stops and removes the
service before it removes immutable files.

The daemon enters its SCM dispatcher before it prepares logging, parses
arguments, or reads configuration. During startup and shutdown, it reports a new
SCM checkpoint every two seconds and a 30-second wait hint. Stop and
system-shutdown controls cancel the same daemon context. Normal cleanup has at
most two minutes to finish before the service returns a failure to SCM. Startup
and cleanup errors also return a failure, but an expected stop that completes
normally returns success. The daemon reports `Running` only after the admin API
and engine are ready.

## Service log

An SCM service writes its detailed operator stream to
`%ProgramData%\Almagest\logs\almagestd.log`. The sink starts before argument
parsing and runtime dependency checks, so startup failures are retained. Log
timestamps use UTC, and packaged Windows services use debug level. A file
rotates at 20 MiB and the daemon keeps five old files (`almagestd.log.1` through
`almagestd.log.5`). The current file and its directory grant access only to
LocalSystem and administrators.

The daemon writes synchronously to this sink and flushes it during normal
shutdown. A normal uninstall keeps the log directory and all rotated files. An
interactive daemon continues to write to its console and does not open the
service log.

## Installer and desktop-app logs

The MSI enables verbose logging automatically. Windows Installer keeps a
randomly named `MSI*.LOG` file in the installing user's Temp directory. This
file remains available when setup fails and rolls back.

The desktop app writes its connection, tray, navigation, startup, and panic
diagnostics to `%LocalAppData%\Almagest\logs\almagest-app.log`. It rotates the
file at 5 MiB and keeps three old files. A non-admin app does not write into the
protected daemon log directory.

The Windows package output includes `collect-windows-support.ps1`. Run it from
the same directory as the MSI. It requests UAC elevation and creates one ZIP
that contains recent Almagest MSI logs, daemon logs, the original signed-in
user's desktop-app logs, event logs, and the diagnostic report. Add
`-RunExperiments` to include a controlled service restart and monitor period.

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
Authenticode signatures. The MSI uses the WiX v4 DifxApp extension to stage the
unchanged signed driver package. DifxApp does not register this primitive
driver's kernel service because the INF has no device models. The MSI therefore
copies the unchanged signed SYS to `%SystemRoot%\System32\drivers` and uses
deferred, non-impersonated SCM actions to register it as demand-start. A
rollback action removes a service created by a failed first install. The MSI
does not start the driver during installation, and Windows does not start it at
boot. The daemon has no hard dependency on the driver and can run without
executable-tree exclusions.

Normal uninstall removes the driver package and its copied driver files after
the daemon stops. Windows Installer rollback removes a driver package that was
added by a failed installation. The staged INF, SYS, and CAT files under Program
Files are also removed. ProgramData remains in place.

WiX v4 marks DifxApp as deprecated, but it is the open-source WiX v4 facility
that supplies transactional driver-package install, uninstall, and rollback. The
package gate tests this fixed choice on Windows Server 2022 before release.

## WiX source maintenance

Run this command after a tracked locale or example is added, removed, or moved:

```sh
python3 packaging/windows/generate-payload-wxs.py
```

The generator uses the UUIDv5 namespaces in `contract.json`. A file keeps its
component GUID while its content changes. A path change gets a new component
GUID. The contract test fails if `payload.wxs` is not current.

`almagest.wxs` requires WiX preprocessor values for `Version`, `ProductCode`,
`SourceDir`, `PayloadDir`, `WintunDir`, and `MullvadDir`. `ProductCode` is
stable for all builds of one product version and changes for a new product
version. `PackageCode` changes when a hashed MSI input changes.
`create-build-manifest.py` supplies the identities from the version, source
record, image lock, and complete package input list. After WiX links the MSI,
the build uses the pinned WiX DTF library to set the PackageCode and fixed
summary creation/save times. It also fixes payload CAB timestamps and compound
file root-storage timestamps, which otherwise use the time of the build.

The host stages the locked Cargo dependencies in the isolated payload. The WinVM
uses its pinned Rust 1.90 GNU toolchain to build `almagest-app.exe` offline
before it creates the final input manifest and MSI. The same MSI contains the
daemon and optional desktop app; Windows does not use a separate desktop-app
package.

Build and harvest the real package with the locked Windows VM:

```sh
just package-windows
just test-windows-packaging
just winvm-package 1.2.3
just winvm-package-repro 1.2.3
just winvm-package-image
just winvm-package-smoke 1.2.3
just winvm-package-lifecycle 1.2.3
just winvm-package-integration 1.2.3
```

`just package-windows [VERSION]` is the release-facing command. It copies the
MSI, its `.sha256` file, and `.build.json` evidence into `dist/`. Without an
explicit version, it uses an exact `vX.Y.Z` or `X.Y.Z` Git tag. An untagged
local snapshot uses `0.0.0`. By default, `just package` creates detached OpenPGP
signatures for the MSI, its checksum and build evidence, the package guide, and
the support collector. Verify one with the public key in `ascii-moth.asc`, for
example:

```text
gpg --verify almagest_VERSION_windows_amd64.msi.sig almagest_VERSION_windows_amd64.msi
```

`just package false` assembles these files without detached signatures beside
the GoReleaser OSS Linux outputs. GoReleaser Pro is not required.

`just test-windows-packaging [VERSION]` runs reproducibility, lifecycle, and
installed-package integration qualification. The complete normal test gate runs
it only when `RUN_WINDOWS_PKG_TESTS=1`; direct use is always explicit and does
not need that variable. Build the clean package image first with
`just winvm-package-image`.

The first command writes the MSI, its SHA-256 checksum, and build evidence to
the run directory. The second command uses two clean overlays and requires
byte-for-byte equality. See `dev/winvm/README.md` for image setup and artifact
locations.

The image command creates a separate clean package-test base image. It carries
the locked build inputs but has no installed Almagest service, runtime files, or
active Mullvad driver package. The smoke command installs the MSI silently in an
overlay of that image. It verifies exact installed file hashes, package and
dependency versions, Authenticode signatures, service and recovery settings,
protected ACLs, the machine pipe, CLI authorization, and the durable startup
log. It returns the MSI log and structured evidence to the host.

The lifecycle command derives the next valid MSI version from the supplied base
version and builds both packages from identical payload inputs. In one clean VM
overlay, it verifies service restart, Windows reboot, SCM failure recovery,
repair, major upgrade, downgrade rejection, uninstall, and reinstall. It also
verifies that administrator config edits and daemon state remain usable through
the complete sequence. Normal uninstall must remove the Mullvad driver service,
system file, and driver-store package, and must retain ProgramData.

The integration command probes all stable API surfaces through the installed CLI
and SCM service. It checks packaged documentation and examples,
LocalProxy/OutNetwork traffic, retained KV and secret state, an example across a
restart, and Wintun adapter creation and cleanup. It validates the signed Wintun
driver that Windows loads and returns structured host-validated evidence.

For focused project checks, use `winpkg:build`, `winpkg:smoke`,
`winpkg:lifecycle`, or `winpkg:integration` with `just check` or
`just test-total`. Set `ALMAGEST_PACKAGE_VERSION=X.Y.Z` for these selectors; an
unset value uses the local snapshot version `0.0.0`.

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
