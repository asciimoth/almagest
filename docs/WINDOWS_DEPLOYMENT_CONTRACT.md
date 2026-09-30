# Windows deployment contract

This document defines the Windows backend dependency, provisioning, and
privilege boundaries. The machine-readable source is
[`windows/deployment-contract.json`](windows/deployment-contract.json).

## Pinned inputs

Almagest pins `github.com/asciimoth/sysnet-windows` at immutable pseudo-version
`v0.1.1-0.20260930092946-a056c520f0fa`, revision
`a056c520f0faf966f54dd90ef63b15d9aa7382ec`. The backend is in the `bin`
module only. The `lib` module must not import it or any daemon package. Fresh
builds use the module proxy or the origin repository. They do not use the
sibling development checkout.

The matching native inputs are:

| Input | Locked identity | License record |
| --- | --- | --- |
| Wintun | 0.14.1 archive, SHA-256 `07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51` | Official binary distribution license; Go bindings are MIT |
| Split driver | 1.3.0.0, upstream revision `0a0eb97f67d1dbcb3d08bda66d3b24f465d95475`, binary revision `5b6f46cde692acb77ee74b37b9fd3f1678c45a52` | Upstream driver is GPL-3.0-or-later or MPL-2.0; signed binary distribution terms also apply |
| Backend | Revision above | GPL-3.0-only |
| Split controller | Included through the backend at revision `c24657e180635e885fe97a1c4076c7f6645016e3` | GPL-3.0-or-later |

The JSON contract records the package hash for each split-driver file and
architecture. An installer must verify all files before it installs them.

## Trusted locations and services

Put the architecture-matched `wintun.dll` in the Almagest executable directory.
Only administrators and the deployment identity can write that directory. The
pinned binding restricts DLL lookup to the application directory and
`System32`, in that order. It does not use the current directory or `PATH`.
`System32` is an allowed fallback, but an Almagest package must not replace a
shared copy without an explicit installer policy.

The split driver has this fixed service contract:

- Service name: `mullvad-split-tunnel`.
- Type: kernel driver.
- Start type: demand start.
- Binary path: `%SystemRoot%\System32\drivers\mullvad-split-tunnel.sys`.
- File version: 1.3.0.0.
- The Windows signature and the architecture-specific SHA-256 must be valid.

The backend reads and verifies this state. It does not install, register,
start, stop, update, or remove the service. It opens the existing device only
when an executable-tree exclusion needs the split driver. The stock device is
a global exclusive resource. Independent VPN controllers must not use it at
the same time.

## Provisioning boundary

External deployment tooling owns all downloads and installation. `almagestd`
and the backend have no package download, driver install, service registration,
service start, or update path. This rule includes automatic repair and
best-effort installation after a missing-dependency error.

Wintun adapter creation is different from package provisioning. The pinned
`wintun.dll` can install or update its bundled Wintun kernel driver when the
backend creates an adapter. Thus, adapter creation has privileged driver and OS
effects even though Almagest does not download or install a Wintun package.
Deployment must provide the DLL before this operation.

If the split driver is absent, busy, incompatible, or needs recovery,
`win-exe-tree` exclusions are unavailable. Regular TUNs, default routing
without exclusions, OutNet, OutDNS, LocalNet, and best-effort `win-pid` and
`win-exe-path` matchers must remain available when their own dependencies are
available.

## Privilege boundary

| Operation group | Minimum supported identity | Elevation |
| --- | --- | --- |
| Construct the system, inspect capabilities, allocate candidate addresses, use OutNet, OutDNS, LocalNet, and ownership matchers | Standard user | Not required; a protected process can still produce an unknown matcher result |
| Create or delete Wintun adapters; change adapter addresses, routes, MTU, or DNS; create or remove WFP state; open and control the split driver | Elevated service identity or administrator | Required for the supported deployment |
| Place the DLL; install or update the split driver; register or start its service | External elevated deployment tooling | Required and outside the daemon |

Run the daemon under a service identity that retains the native privileges for
later graph changes. Do not apply the Linux privilege-drop model on Windows.
Standard-user native suites use explicit user-owned config and storage paths
and do not request network mutation.

The supported Service Control Manager identity for the default machine admin
pipe is Local System. Its pipe ACL also authorizes elevated administrators.
Interactive standard-user daemon runs use a separate SID-scoped pipe and do
not qualify the service deployment.

## Failure rules

The daemon must preserve backend capability states and errors. It must not
convert permission denial into unsupported behavior or convert a deferred
split-driver ownership check into an available state. A missing optional split
dependency must not prevent system construction or ordinary networking.

The deployment contract is a design and validation input. Native execution in
the required Windows suites is still necessary before a platform status can
change from `unavailable` to `supported`.
