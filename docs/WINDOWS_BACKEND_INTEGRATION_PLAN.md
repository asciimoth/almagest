# Windows backend integration plan

Status: proposed implementation plan. Milestones 1 through 6 are complete.

## Objective and scope

Integrate `github.com/asciimoth/sysnet-windows` as Almagest's native Windows
network backend. Add Windows config, storage, access control, administration,
process lifecycle, and certificate-store behavior. Add reproducible Windows
builds, disposable VM tests, and native test coverage.

Keep Windows backend and OS integration code in `bin/`. Put shared graph and
node lifecycle changes in `lib/`, through the existing `sysnet.System`
contract. Keep Linux behavior covered by the existing tests.

Do not include Wintun or Mullvad driver downloaders, package installers, or
upgraders in `almagestd`. VM provisioning can obtain and install locked test
inputs outside the daemon. Future installers/packages will provide production
dependencies. Their implementation is outside this plan.

Native Wintun adapter creation still uses the Wintun API. Audit and document
that API's driver acquisition behavior separately from package provisioning;
do not promise that adapter creation has no driver-related OS effects.

## Inspection baseline

The initial inspection used these local revisions:

| Project | Revision |
| --- | --- |
| Almagest | `d4955cb` |
| sysnet-windows | `a056c52` |
| mullvad-split-tunnel-go | `c24657e` |
| tor-driver | `1fea88e` |

The existing `lib`, `bin`, and `certcat` modules passed this build check on a
Linux host with Go 1.26.7, before Windows integration:

```sh
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go -C lib build ./...
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go -C bin build ./...
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go -C certcat build ./...
```

This proves compilation with the existing local generated inputs. It does not
prove a fresh-checkout build, test-binary compilation, or native Windows
behavior. Native Windows tests were not run during planning.

| Area | Existing code and integration gap |
| --- | --- |
| Platform selection | [Fallback platform](../bin/almagestd/platform/platform_other.go) uses a debug backend and embedded config. Windows needs a separate implementation. |
| Config and storage | [Linux platform](../bin/almagestd/platform/platform_linux.go) implements file config, private storage, safe reads, and config writes. Windows needs equivalent behavior using its own permission model. |
| Admin access | [CLI endpoints](../bin/almagest-cli/endpoint.go) default to Unix sockets. The fallback protected config requests an unsupported Unix listener. Web peer filters use UID/GID semantics. |
| Capabilities | [TUN capability helpers](../lib/tun/sysnet_capabilities.go) already handle capability reports and validation. Extend their tests instead of adding a second model. |
| Shutdown | [Daemon startup](../bin/almagestd/main.go) discards engine and system close errors. Engine close already closes the system. Startup fatal paths also need an explicit resource audit. |
| Trust stores | [Fallback authority fetcher](../bin/almagestd/platform/tls_authorities_other.go) does not enumerate Windows certificate stores. |
| Build inputs | `go.work`, certificate catalog output, and embedded panel assets are ignored by Git. Source packaging must account for their generation. |

## Reference projects

Use these sources at the recorded revisions. Recheck their contracts when
selecting the dependency version for implementation.

- [sysnet-windows integration contract](https://github.com/asciimoth/sysnet-windows/blob/a056c52/docs/integration.md)
- [sysnet-windows limitations](https://github.com/asciimoth/sysnet-windows/blob/a056c52/docs/limitations.md)
- [sysnet-windows VM harness](https://github.com/asciimoth/sysnet-windows/blob/a056c52/dev/winvm/README.md)
- [Mullvad controller VM harness](https://github.com/asciimoth/mullvad-split-tunnel-go/blob/c24657e/dev/winvm/README.md)
- [Tor driver VM harness](https://github.com/asciimoth/tor-driver/blob/1fea88e/dev/winvm/README.md)
- [Almagest architecture](../ARCHITECTURE.md)
- [Runtime node lifecycle requirements](../RUNTIME_NODE_LIFECYCLE_PITFALLS.md)

The corresponding sibling checkouts are `../../sysnet-windows`,
`../../mullvad-split-tunnel-go`, and `../../tor-driver`, relative to this file.
They are development references, not required build inputs for Almagest.

## Proposed platform contract

These are design choices to implement and verify, not existing behavior:

- Qualify Windows Server 2022/amd64 first. Windows 11/amd64 and Windows
  11/arm64 require separate native evidence before a support claim.
- Resolve machine config through the Windows known-folder API under
  `%ProgramData%\Almagest`. Use `alm.hujson` and `protected_alm.hujson`.
- Use `%ProgramData%\Almagest\store` for service storage and
  `%LocalAppData%\Almagest\store` for interactive user storage. Explicit
  config and storage arguments take precedence. Standard-user tests provide
  explicit user-owned config paths; they do not need to create machine config.
- Use a local named pipe for default CLI administration. Keep web admin
  disabled by default. Support an optional best-effort Windows client-user
  allowlist and warn when web admin is enabled without it. Strong API-token
  authentication is separate work.
- Support console execution and Service Control Manager execution. Keep
  service registration outside the daemon.
- Preserve backend capability distinctions: unsupported, currently
  unavailable, and unknown until an operation performs its final checks.
- Do not silently translate Linux rules or weaken unsupported routing policy.

The backend supports regular/default TUNs, OutNet, loopback-only LocalNet,
managed DNS, non-strict `win-exe-tree` exclusions, and best-effort `win-pid`
and `win-exe-path` matchers. It does not support strict routing, include-only
routing, preferred-source routes, adapter rename, or multicast bypass.
Default routing is not a persistent kill switch. Managed adapter DNS is not
system-wide DNS enforcement.

## Milestone 1: Define support and deployment contracts

### Step 1: Record the support matrix and feature mapping

Implementation: complete. See [the Windows support matrix](WINDOWS_SUPPORT_MATRIX.md),
the [machine-readable feature mapping](windows/support-matrix.json), and the
[required-test manifest](windows/test-manifest.json).

Map Almagest node operations, rules, menus, and daemon functions to backend
capabilities. Record required tests for each Windows/architecture entry.

Acceptance conditions:

- Each feature has a supported, unavailable, or unsupported status.
- Each support claim names its OS, architecture, required dependencies, and
  required native suites.
- Cross-builds and skipped tests cannot qualify native support.

Tests: validate the required-test manifest and capability mapping. Include
negative manifest fixtures for absent, skipped, and wrong-platform evidence.

### Step 2: Define dependency and privilege boundaries

Implementation: complete. See the
[Windows deployment contract](WINDOWS_DEPLOYMENT_CONTRACT.md) and its
[machine-readable form](windows/deployment-contract.json).

Pin a released version or immutable module revision in `bin/go.mod`. Record
the matching Wintun and split-driver inputs, licenses, service requirements,
and trusted DLL location. Define which operations require elevation.

Acceptance conditions:

- Fresh builds do not require sibling replacements.
- `lib/` does not import the Windows backend or daemon packages.
- The daemon has no package download/install/update path. Provisioning owns
  split-driver service registration and startup; the inspected backend only
  verifies the service and opens its device.
- Missing split-driver support affects exclusions, not ordinary networking.

Tests: dependency/import checks and deployment-contract fixtures. Register
native dependency and privilege cases in the manifest for M3 and M4; those
later execution gates do not block this contract milestone.

Milestone acceptance: the support matrix, deployment contract, privilege
requirements, and initial required-test manifest are recorded and validated.

## Milestone 2: Add Windows builds and disposable VM tests

### Step 3: Adapt builds to Almagest's workspace

Implementation: complete. See [Windows cross-builds](WINDOWS_BUILD.md).

Add `build-windows` for amd64 and arm64. Build the daemon, CLI, all three Go
modules, and test binaries, including Windows-only and integration packages.
Discover packages with the target OS, architecture, and build tags.

Acceptance conditions:

- A fresh checkout builds without replacements pointing outside the checkout
  or pre-existing assets. Keep the existing in-repository module replacements.
- The pipeline creates a workspace, certificate catalog output, and embedded
  panel assets without committing generated files.
- The guest toolchain meets Almagest's `go 1.26.6` requirement. Do not copy the
  sibling harness's Go 1.25.14 lock unchanged.
- Test compilation is recorded separately from test execution.

Tests: fresh-checkout amd64/arm64 builds; missing generated inputs; target-only
package discovery; module verification; tidy checks. Record input hashes for
generated catalog and panel assets in build evidence.

Audit existing tests for Unix paths, shell tools, permission bits, and socket
assumptions. Give shared tests portable fixtures and helper processes. Apply OS
build tags only to tests of OS-specific behavior; do not hide shared failures.

### Step 4: Adapt the QEMU/KVM harness

Implementation: complete. See the [VM harness guide](../dev/winvm/README.md).
The smoke command remains separate from the native test gate and records that
tests were not run. Each use of the harness must still produce evidence from
the locked media; checked-in harness code is not native execution evidence.

Reuse the sibling approach: locked inputs, unattended provisioning, QEMU Guest
Agent, verified SSH, stable readiness tokens, cache locks, and per-run overlays.
Package tracked changes and eligible untracked files. Regenerate ignored build
inputs in a staging area or guest from recorded inputs.

Acceptance conditions:

- Each run uses an isolated overlay, firmware state, port, and artifact path.
- Source archive hashes identify the exact tested tree, including dirty runs.
- Media, secrets, VM disks, and local environment files stay outside archives.
- Privileged networking tests run only in disposable Windows guests or
  dedicated disposable Windows test hosts. Failed runs keep diagnostics and
  print a recovery command.
- The Linux QEMU/KVM appliance qualifies Server 2022/amd64 only. Provision
  separate Windows 11 environments, including native arm64 execution. An amd64
  guest or emulated application run cannot qualify Windows/arm64.

Tests: wrong hashes, absent KVM/media, provisioning timeout, reboot readiness,
port contention, concurrent runs, archive exclusions, artifact retrieval
failure, and bounded guest shutdown. Test harness logic without starting a VM.

### Step 5: Separate ordinary and privileged test execution

Implementation: complete. See the [VM harness guide](../dev/winvm/README.md)
and the [active execution manifest](../dev/winvm/test-suites.json). The active
Milestone 2 gate runs portable Go tests as the standard account and verifies a
separate serialized SYSTEM suite. Later milestones add their native cases to
the privileged or ordinary boundary when those cases become mandatory.

Run ordinary tests as a standard account. Run native networking, ACL, service,
and driver cases with the required privileges. Serialize tests that share
global networking resources. Use structured Go test events and required cases.

Acceptance conditions:

- Each suite verifies its execution identity.
- Required tests must pass. A missing test, skip, timeout, or missing artifact
  fails that suite.
- The baseline can run before backend integration. New native requirements
  become mandatory as their implementation milestones complete.

Tests: missing/skipped test fixtures, corrupt events, incorrect identity,
failed artifact collection, and serial execution of exclusive-driver cases.

Milestone acceptance: complete. Current portable behavior has a native Windows
build and execution gate, and the harness reports identity, event, timeout, and
artifact failures reliably. Native backend integration is not claimed.

## Milestone 3: Connect the native backend

### Step 6: Add Windows selection and protected config

Implementation: complete. Windows now selects the pinned sysnet-windows
backend, maps protected `System.Windows` fields into its typed configuration,
and provides Windows-specific embedded defaults. The schemas document the
feature switches, duration and recovery formats, stable GUID behavior, and
preferred-underlay fallback semantics. The native suite registers the stable
GUID collision case as an elevated serialized test.

Add Windows platform files and exclude Windows from fallback build tags. Map
`Protected.System.Windows` into `SystemConfig`: adapter prefix, optional GUID,
underlay preference, feature switches, timeout, recovery policy, and logger.
Add Windows defaults and update both config schemas where applicable.

Acceptance conditions:

- Native selection uses sysnet-windows; explicit debug selection still works.
- Linux retains sysnet-linux. Other unsupported platforms retain their stub.
- The logger is injected by code, not deserialized from user configuration.
- `UnderlaySelector` is documented as a preference, not a hard restriction.

Tests: selection, defaults, explicit fields, invalid GUID/duration/recovery,
disabled families, schema/examples, and stable-GUID collisions across multiple
TUN requests. Verify updates to the correct module and checksums.

### Step 7: Preserve capabilities, warnings, and errors

Implementation: complete. Almagest uses the shared sysnet capability and
validation reports for class selection, menus, and preflight checks. Unknown
capabilities remain advisory, while unavailable and unsupported capabilities
block the operation. Windows contract tests cover unsupported routing,
multicast, Linux rules, deferred split-driver ownership, disabled matchers, and
error identity preservation. DefaultTun publishes backend DNS-scope warnings
as node warning popups.

Use existing capability and option-validation paths. Preserve the split
driver's unknown state until its deferred ownership check runs. Make missing
features and dependency failures visible in node status and menus.

Acceptance conditions:

- Unsupported operations fail before mutation.
- Unknown capability permits a final backend check; unavailable capability
  does not become a successful operation.
- There is no silent debug fallback, unrestricted socket fallback, or policy
  downgrade. Backend error identities survive wrapping.

Tests: strict/include routing, preferred-source routes, rename, multicast,
missing Wintun, no underlay, busy/missing split driver, disabled matchers, and
joined primary/rollback errors. Verify DNS warnings through node status.

### Step 8: Validate runtime dependencies without provisioning them

Implementation: complete. The daemon validates Wintun from an absolute trusted
application-directory or System32 path before and after it changes to file
storage, checks the loaded path and pinned version, and preserves actionable
loader errors. Explicit debug integration does not require native inputs. The
disposable native-test image provisions the locked DLL and split-driver inputs
outside the daemon. Native cases cover hostile and invalid DLLs, stopped,
absent, incompatible, and exclusively owned split drivers, and verify that
rejection does not provision or start the split service.

Verify trusted DLL lookup before and after the daemon changes directory.
Report actionable dependency errors. Inspect the dependency's actual Wintun
loading behavior and address any upstream change required for safe lookup.

Acceptance conditions:

- An untrusted current directory cannot supply the loaded DLL.
- Construction does not create TUNs or install packages. Lazy resource
  operations retain the backend's ownership and validation checks.
- An absent split driver does not block regular/default TUNs without exclusions.

Tests: missing DLL, wrong architecture, hostile lookup location, absent/stopped
or incompatible split service, and a controller already holding the driver.
Check that rejection does not install, start, reset, or replace the service.

Milestone acceptance: complete. Windows selects and configures the native
system, with correct capabilities and no daemon-owned dependency provisioning.

## Milestone 4: Implement config, storage, and access controls

### Step 9: Implement Windows paths and config behavior

Implementation: complete. Windows resolves machine config through ProgramData,
uses ProgramData storage for services and LocalAppData storage for interactive
processes, and honors explicit absolute or pre-resolved relative overrides.
File-backed config is writable; stdin config remains read-only and keeps its
embedded `Protected` behavior. Temporary storage is identity-local and removed
through the daemon's bounded cleanup lifecycle. Unix administration flags are
rejected before filesystem setup.

Implement the proposed known-folder defaults, overrides, config-directory
arguments, stdin config, and source read-only behavior. Resolve explicit
relative paths before changing the working directory.

Acceptance conditions:

- Paths do not depend on the service working directory.
- Regular and protected config remain separate. Stdin retains the existing
  embedded `Protected` behavior and is read-only.
- Missing defaults are created only in a location the identity can safely use.
- Temporary storage is private and has bounded cleanup behavior.

Tests: service/user paths, spaces/Unicode, relative paths, directory arguments,
missing config, stdin, write rejection for read-only sources, temp cleanup, and
unsupported Unix-only flags.

### Step 10: Add Windows ACL and path validation

Implementation: complete. Windows config files are created with protected
ACLs for the current identity, Local System, and Administrators. Storage and
temporary directories use the same ACL with inheritable access for generated
private state. Safe config reads validate owner, DACL, file type, link count,
and reparse status on an open handle. Parent checks reject untrusted
replacement rights. Safe state also rejects traversal, UNC/device paths, and
alternate data streams. `--unsafe-config` affects config checks only.

Define trusted owners and explicit access for config, protected config,
storage, generated secrets, and temporary directories. Use SIDs and Windows
ACLs, not Unix mode bits. Check inherited permissions and parent replacement
rights. Use handles to prevent reparse-point and path-substitution races.

Acceptance conditions:

- Unrelated standard users cannot alter trusted config or read private state.
- Created files are private from creation, not only after a later ACL change.
- Safe mode rejects unsafe owners, access grants, or path traversal.
- `--unsafe-config` does not bypass storage or admin authorization.

Tests: trusted/untrusted owners, writable parents, inherited grants, null or
missing DACLs, denied secret reads, junctions, symlinks, hard-link policy,
alternate streams, and replacement attempts between validation and use.

### Step 11: Port durable writes and snapshots

Implementation: complete. Windows config, TLS CA, snapshot rename, and restored
file writes now use same-volume replacement that preserves the destination
DACL and keeps a recovery copy while `ReplaceFileW` runs. Config writes are
serialized, synced before publication, and validated again after replacement.
Snapshot imports reject case aliases, reserved device names, drive paths,
alternate streams, and trailing-dot or trailing-space aliases on every host.
Windows rejects symbolic-link archive entries before live state changes because
ordinary users cannot be assumed to have symbolic-link creation privilege.
Archive permission bits do not set Windows ACLs; new and replacement files
inherit the private storage ACL, while existing destination DACLs are retained.

Implement safe config replacement with the required ACLs. Audit bbolt,
temporary files, TLS keys, snapshot extraction, restore, and rollback for
Windows path and file-sharing behavior.

Acceptance conditions:

- Failed writes leave a valid prior file and do not weaken its ACL.
- Restore preserves protected/host-local state and restores destination ACLs;
  archive mode bits cannot grant access to private Windows state.
- Archives cannot escape storage or overwrite files through path aliases.
- Unsupported archive entries fail validation before live state changes.

Tests: locked files, interrupted replacement, concurrent saves, persistence,
rollback, case collisions, reserved device names, drive/UNC paths, alternate
streams, reparse escapes, and symlink restore without required privileges.

Milestone acceptance: complete. Config editing, storage, secrets, and snapshots
use the private Windows ACL boundary. The native ordinary and privileged suites
register durable replacement, denied replacement, path-alias, reparse, and
snapshot cases.

## Milestone 5: Provide secure administration

### Step 12: Add a local named-pipe transport

Implementation: complete. Windows services use the machine endpoint
`\\.\pipe\almagest-admin`; interactive daemons use a SID-scoped endpoint.
Pipe ACLs authorize the interactive identity or, for the supported Local
System service, Local System and elevated administrators. The CLI uses
`pipe://user[/NAME]` and `pipe://machine[/NAME]`, rejects remote pipe names,
and verifies the expected pipe owner before it exchanges API messages.
Explicit names provide isolated test endpoints without changing the trust
class.

Reuse the stream codec and API session implementation. Add daemon config and a
CLI endpoint scheme. Use `\\.\pipe\almagest-admin` as the proposed machine
default, with an explicit ACL and remote-client rejection. Define a separate
identity-scoped name for user instances and a unique override for tests.

Acceptance conditions:

- Authorized Windows identities can use the CLI; unrelated users cannot.
- A second server cannot silently claim an occupied endpoint.
- Client connection settings do not allow an untrusted pipe server to
  impersonate an elevated CLI identity.
- User and service endpoints cannot accidentally address each other's state.
- Linux Unix-socket behavior remains unchanged.

Tests: allowed/denied accounts, restricted UAC tokens, remote clients, endpoint
collision and impersonation controls, user/service separation, reconnect,
deadlines, subscriptions, large snapshots, and shutdown with connected clients.

### Step 13: Add owner-filtered Windows web administration

Implementation: complete. See [Windows web administration](WINDOWS_WEB_ADMIN.md).
Web admin stays disabled by default. An optional username filter resolves each
configured account to a SID. For every accepted TCP connection, the daemon
finds the client socket owner, opens the client process token, and compares its
user SID with the configured SIDs. This protects the panel, WebSocket API, and
optional diagnostic routes at the listener. Unknown, ambiguous, inaccessible,
and changed owners fail closed when the filter is enabled. Without a filter,
the listener starts and warns that any local user can access it. Unix UID and
GID web filters are rejected on Windows.

This check has the same best-effort and race-prone character as the Linux TCP
owner filter. It is a useful local-user limit, but it is not a strong security
or authentication boundary. An API token system will provide that boundary in
a separate project. It is outside the Windows backend integration scope.

Acceptance conditions:

- Web admin remains disabled by default. Enabling it without a username filter
  produces an explicit warning that all local users can access it.
- Account names are resolved to SIDs at listener creation and again for each
  accepted connection. The process-token SID must match the original SID.
- Owner lookup failure, ambiguity, process exit, or inaccessible process-token
  data denies the connection.
- No web credential is issued, stored, logged, placed in a URL, or included in
  a snapshot. Cross-origin relaxation does not bypass the owner filter.

Tests: allowed and wrong-user requests, unknown and ambiguous owners, account
remapping, cross-origin and forged Host requests, WebSocket upgrades, panel
operations, pprof access, and invalid filter config.

Milestone acceptance: the CLI uses the native named-pipe authorization
boundary. Panel, WebSocket API, and diagnostics use the documented best-effort
Windows socket-owner filter. Strong API authentication remains separate from
this backend milestone.

## Milestone 6: Complete process, service, and trust behavior

### Step 14: Unify startup and shutdown ownership

Implementation: complete. Console signals and Windows SCM stop/shutdown
controls now cancel the same daemon context. One bounded cleanup owner flushes
configuration, closes graph and native resources, and releases storage on both
normal shutdown and partial startup failure. The SCM service name is
`almagestd`; external tooling remains responsible for registration.

Add console and Service Control Manager execution. Route stop/shutdown events
through one lifecycle. Make each startup acquisition have explicit cleanup,
including failure paths. Replace discarded close errors and accidental double
close with deliberate, bounded backend recovery.

Acceptance conditions:

- Stop flushes config, stops workers, closes the engine/system, releases
  storage, and reports the final process/service status.
- Cleanup errors remain visible even if a later recovery attempt succeeds.
- Recovery retries use fresh bounded contexts and retain ownership checks.
- Service registration stays in external tooling. Native privileges needed
  for later graph changes remain available; do not copy Unix privilege drops.

Tests: console stop, SCM stop/shutdown, partial startup failure, repeated stop,
close failure/recovery, timeout, restart, and failure after system creation
but before a usable engine exists.

### Step 15: Implement Windows child-process lifecycle

Implementation: complete. Protected commands start suspended, enter a
kill-on-close Job Object, and only then resume. Timeout, daemon cancellation,
normal command completion, and startup failure close the complete owned process
tree. Windows rejects protected UID/GID settings before process creation and
keeps executable and argument boundaries in `os/exec`. The pinned tor-driver
version has Job Object cleanup for its ordinary Windows launcher, but strict
Windows network containment is unavailable. Almagest uses tor-driver's
best-effort network policy and logs a warning when it falls back to the ordinary
network adapter. When Almagest has an elevated Windows token, Tor and managed
transport processes start with a verified restricted token that has no enabled
Administrators membership. Failure to create that token stops process startup.

Add process-tree cleanup, using Job Objects where suitable. Preserve command
allowlists and argument boundaries. Reject Unix credentials before launch.
Check experimental Tor behavior against the pinned tor-driver version.

Acceptance conditions:

- Timeout and daemon shutdown leave no owned child processes.
- Children cannot escape tracking during startup before cancellation is set.
- Unsupported Tor network containment selects the best-effort mode and logs a
  warning. An elevated parent never passes administrator privileges to Tor.

Tests: child/grandchild termination, immediate cancellation, argument quoting,
paths with spaces, environment handling, denied credentials, Tor containment
fallback warnings, and elevated-token reduction. Public Tor tests remain opt-in.

### Step 16: Read Windows certificate authorities

Implementation: complete. Windows enumerates public certificates from the
daemon identity's `CurrentUser\Root` store and the `LocalMachine\Root` store.
Gonnect validates and deduplicates the copied DER while preserving both source
records. Almagest keeps the catalog read-only and requires explicit policy
selection. Startup and reload report native store failures; an empty catalog or
the explicit fallback override uses the compiled NSS roots. See
[Windows certificate authority discovery](WINDOWS_TLS_AUTHORITIES.md).

Add a Windows authority fetcher for the selected machine/current-identity
stores. Preserve Almagest's explicit authority-selection model. Document
fallback and reload behavior.

Acceptance conditions:

- Discovery reflects the daemon identity and does not export private keys.
- Discovered candidates do not become implicitly trusted outside existing
  policy. Enumeration is not claimed to reproduce all Windows chain policy.
- Store access failure and fallback behavior are observable and documented.

Tests: temporary CA discovery/removal/reload, duplicates, malformed entries,
cancellation, user/service store differences, fallback, and TLS API behavior.
Remove test certificates from their exact stores during cleanup.

Milestone acceptance: complete. Console/service execution, process cleanup,
and identity-specific trust discovery have native lifecycle and identity test
gates.

## Milestone 7: Validate graph behavior

### Step 17: Test regular and default TUN lifecycle

Implementation: complete. Regular and default TUN graph providers now ignore
late configuration from detached links, distinguish runtime build failures from
invalid configuration, invalidate retired outputs, and recover from a later
equivalent apply. DefaultTun detects a backend that retires its stable native
source before a failed replacement. Native Windows graph tests exercise the
production backend for regular in-place updates and DefaultTun rebuilds.

Exercise creation, config propagation, rewiring, replacement, and close through
Almagest graph operations. Follow the runtime lifecycle requirements.

Acceptance conditions:

- Nodes report `up` only for the current usable attachment.
- Regular TUNs use supported updates. Default-TUN address, route, DNS-address,
  and MTU changes rebuild; provider replacement through `SetDNS` does not.
- Equivalent config does not cause unnecessary replacement.
- Failure after retirement does not leave a false active-state report.

Tests: stale async completions, failed replacement, failed attachment reuse,
unchanged config, rapid rewiring, cancellation, stale handles, and recovery.

### Step 18: Test OutNet, LocalNet, and DNS through graphs

Implementation: complete. Native serialized graph cases keep DefaultTun active
while they exercise IPv4/IPv6 TCP and UDP through OutNetwork, a wildcard
LocalProxy listener through LocalNet, and a WireGuard transport over OutNet.
The managed DNS case covers UDP and TCP queries, live provider replacement,
detach (`SetDNS(nil)`), reattach, and the non-exclusive DNS warning. A graph
source-route case verifies that Windows rejects the requirement and reports the
`DefaultTun.SourceRoutes` error instead of discarding it.

Cover outbound traffic, local proxies, and WireGuard over OutNet while a default
TUN is active. Audit wildcard socket requests and source-route propagation.

Acceptance conditions:

- Outbound transport avoids its own TUN; LocalNet stays on loopback.
- DNS provider changes take effect and warnings reach node status.
- Underlay/DNS failure does not cause an unrestricted fallback.
- Unsupported source-route requirements produce a clear error. Provide a
  working Windows graph example rather than silently dropping requirements.

Tests: IPv4/IPv6 TCP/UDP, mixed-address LocalNet DNS answers, UDP/TCP DNS,
underlay change/loss, no upstream, DNS port conflict, provider detach,
`SetDNS(nil)`, external DNS edits, and shutdown during active requests.

### Step 19: Test Windows rules and menus

Implementation: complete. DefaultTun exposes only capability-valid Windows
routing choices, validates local executable paths through the backend, and
keeps rejected Linux rules visible and saved for repair without replacing the
last valid runtime. TunSplitter applies `win-pid` and `win-exe-path` through its
`RULE` graph program and likewise preserves rejected desired programs while its
last valid router stays active. Native cases cover executable-tree application,
best-effort ownership matchers, menu states, path handling, and persistence.

Cover `win-exe-tree` exclusions and `win-pid`/`win-exe-path` matchers through
config, forms, autocomplete, graph processing, and persistence.

Acceptance conditions:

- Routing exclusions and observed socket ownership remain distinct features.
- Unknown/ambiguous ownership is not a confirmed nonmatch.
- Unsupported Linux rules receive clear errors and remain visible for repair.

Tests: path normalization/case handling, invalid path forms, descendants,
pre-existing IPC targets, inaccessible processes, ambiguous UDP ownership,
capability-dependent menus, and config save/reload.

Milestone acceptance: complete. Representative Almagest graphs pass backend
contract tests for TUN lifecycle, OutNet, LocalNet, DNS, Windows rules, menus,
and persistence. Direct sysnet-windows tests are supporting evidence only.

## Milestone 8: Prove packet paths and resource recovery

### Step 20: Add daemon-level native integration cases

Port applicable Linux E2E scenarios using real Windows daemon/CLI binaries.
Cover startup, config editing, snapshots, TLS, subscriptions, local proxies,
TUN routing, and command lifecycle. Use safe config in access-control cases.

Acceptance conditions:

- Native cases use the production backend and executable startup path.
- Required cases run under the intended standard/elevated identity.
- Tests measure owned native state before and after privileged operations.

Tests: normal workflows, denied access, dependency failures, startup errors,
restart persistence, cleanup, and the complex cases below. Record explicit
reasons for Linux-only cases that have no Windows equivalent; do not count
their skips as Windows coverage.

#### Case: `windows_real_life_stack`

Add a Windows counterpart to
[`local_real_life_stack`](../e2e/cases/local_real_life_stack/README.md). Run
the production daemon in a disposable Windows guest and place the test
services on an isolated fixture guest. Do not use the public Internet or host
services. Give the Windows guest separate management and test adapters so that
management traffic cannot satisfy a data-path assertion.

The fixture guest provides an HTTP origin, an HTTPS origin with a private test
CA, authoritative UDP/TCP DNS, a WireGuard peer, and a self-hosted control
service plus tailnet peer when the Windows-compatible Tailscale graph is
available. Generate certificates, keys, addresses, and control-plane state for
each run. Return the observed destination, source address, protocol, and a
unique request marker from each origin. Capture the fixture underlay and the
WireGuard link separately.

Use one graph that is close to a workstation configuration:

```text
OutNetwork direct
  +-- direct HTTP client
  +-- direct SOCKS proxy -> destination router
  `-- managed VPN -> VTun transport
       +-- DNS client -> persistent DNS cache
       +-- VPN HTTP client
       +-- VPN SOCKS proxy -> TLS interception
       `-- nested WireGuard/Tailscale transport

DefaultTun
  `-- TunSplitter applications
       +-- UDP/TCP port 53 -> Tun2Dns -> persistent DNS cache
       +-- selected destination or process -> DROP
       `-- application VTun -> outer splitter
            +-- ordinary destination -> managed VPN
            `-- private peer prefix -> nested WireGuard/Tailscale transport
```

Use the managed Amnesia path if it runs on Windows without an unsupported
process or route requirement. Otherwise, use a static WireGuard node as the
outer managed VPN. Record this reduction in the case manifest; do not replace
the VPN with the debug backend or direct `OutNetwork`. The reduced case must
still cross two real TUN boundaries for the nested-private path.

The case must verify all of these behaviors:

- Every required node becomes `up`, stays stable for a bounded interval, and
  remains usable after the traffic matrix. A ready label alone is not a pass.
- Direct HTTP and SOCKS traffic has the direct source identity. VPN traffic
  has the fixture gateway identity. The nested peer is reachable only through
  the expected outer tunnel.
- UDP and TCP DNS through the graph return fixture answers. DefaultTun captures
  an application query sent to an unrelated DNS address, and the answer enters
  the persistent cache.
- HTTP and HTTPS interception reach the internal HTTP service. The client
  trusts only the exported per-run test authority, and SNI, destination, and
  response markers match.
- `win-exe-path` selects one test helper for a block or alternate slot.
  A `win-exe-tree` exclusion lets another helper and its child use the direct
  path. A same-name executable in a different directory does not inherit the
  path rule. When a stable PID is available, a `win-pid` check covers one
  connection and then proves that PID reuse or process exit does not retain
  stale ownership.
- Removing and restoring destination and process rules through the admin API
  changes real traffic without restarting the daemon. Saving and reloading the
  graph preserves the Windows rule forms.
- Replacing DNS providers, rewiring the outer splitter, and switching between
  automatic and supported manual routing leave unrelated flows usable. Do not
  request include-only, strict, preferred-source, or multicast behavior.
- A snapshot made after cache population restores the graph and cache after a
  deliberate live edit. A daemon restart preserves storage and restores all
  required paths.
- Graceful stop removes owned adapters, routes, DNS state, and split-driver
  state. The test compares native state before and after the case and fails on
  an unexplained difference.

Run the normal traffic matrix as a standard user against a daemon instance
started with the privileges required for native networking. Run named-pipe
administration and snapshot operations through the production CLI. Keep
Windows Firewall enabled. Use explicit timeouts and readiness probes; fixed
sleeps are not evidence.

#### Case: `windows_real_life_spoofer_stack`

Add a Windows counterpart to
[`local_real_life_spoofer_stack`](../e2e/cases/local_real_life_spoofer_stack/README.md)
for the workstation graph that uses Spoofer as its outer transport. Use a
Windows guest with two test adapters connected to two isolated underlay
networks. Both networks reach the same fixture services through different
gateways. Keep the management adapter outside the tested routes.

Use this graph shape:

```text
OutNetwork direct
  +-- direct clients and SOCKS proxy
  `-- Spoofer transport <- TunJoiner transport
       +-- VTun transport
       |    +-- DNS cache and HTTP clients
       |    +-- SOCKS router and TLS interception
       |    `-- nested WireGuard/Tailscale transport
       `-- default slot from the application splitter

DefaultTun
  `-- application and DNS splitter
       +-- UDP/TCP DNS -> Tun2Dns
       +-- Windows process and destination rules
       `-- application VTun -> automatic or supported manual splitter
```

First run the same DNS, HTTP/HTTPS, SOCKS, nested-private-network, live-rule,
snapshot, restart, and native-state checks required by
`windows_real_life_stack`. Then, without restarting Almagest, move through
adapter A, no test underlay, adapter B, no test underlay, and adapter A. For
each connected phase, require direct OutNetwork, DefaultTun/Spoofer, DNS, and
nested tunnel traffic to recover. During each disconnected phase, existing
and new requests must fail closed within a bounded time; the management path
may remain available only for control and evidence collection.

Packet captures must prove which adapter carried each unique marker and that
Spoofer traffic crossed its VTun. A successful response on the management
adapter, the wrong test adapter, or both adapters fails the phase. Validate
route and DNS state after every transition, and verify final cleanup against
the pre-test native-state inventory.

If reliable dual-adapter switching is not available on an initial VM target,
the minimum acceptable case uses one test adapter and cycles link down/up plus
gateway loss/recovery. Mark it as the reduced variant in evidence. This
variant can satisfy daemon integration coverage, but it cannot satisfy the
multi-underlay recovery claim or replace the full packet-flow qualification.

#### Complex-case evidence and reduction rules

Treat both complex cases as required tests for Server 2022/amd64 once their
used features are implemented. Register every required phase and assertion in
the required-test manifest so a skip, missing event, missing capture, or
reduced variant cannot appear as a full pass. Run each case repeatedly from a
clean overlay and at least once across daemon restart. Keep all failure
artifacts: daemon and service logs, structured test events, graph and worker
snapshots, native adapter/route/DNS inventory, process identities, and packet
captures.

The Windows cases can omit Linux namespaces, nftables, bpffs, `p-mark`,
`cmd`/`comm` matching, Unix sockets, and Unix permission checks. Replace them
with isolated VM links, Windows Firewall, Windows executable/PID rules, named
pipes, and ACL checks where applicable. A simplification is valid only when
the manifest names the omitted Linux behavior, the Windows limitation that
requires it, and the remaining end-to-end path. Do not omit a layer only to
make the case easier to pass. Shared fixture or harness failure is an error,
not a product skip.

### Step 21: Add isolated packet-flow tests

Adapt the sibling two-guest/two-link topology. Run Almagest graphs and capture
the underlay and tunnel links independently. Keep management traffic separate
from tested routes and test any firewall relaxation only inside the guests.

Acceptance conditions:

- IPv4-only, IPv6-only, and dual-stack profiles prove included, excluded,
  descendant, and IPC-target process paths.
- Underlay failure does not cause unrestricted fallback.
- Every absence assertion has a positive control. Missing profiles, captures,
  markers, or controls fail independent evidence validation.
- Shared Windows resolver traffic is not attributed to the requesting app.

Tests: TCP/UDP markers, nested WireGuard transport, link failure/reconnect,
application-owned DNS, shared resolver DNS, and incorrect-link markers. Include
negative fixtures for the independent capture validator.

### Step 22: Add resource and recovery qualification

Adapt the sibling resource gate: warm-up, repeated lifecycle batches,
cancellation, and a 30-minute transfer/reconfiguration soak. Add forced daemon
termination and restart, separate from graceful in-process cleanup.

Acceptance conditions:

- Normal operation and successful recovery leave no owned adapter/route/DNS
  leaks or damage to foreign state.
- Record handle, goroutine, and private-memory samples. Set numeric growth
  limits and settling windows before qualification; do not choose them after
  observing a failing run.
- Uncertain split cleanup remains visible and retains recovery information.
- Do not claim a persistent cross-process split/WFP recovery journal unless
  the pinned backend provides one. Record manual recovery where required.
- Failure-injection cases state which resources must be retained and why.
  Validate that exact state and its recovery separately; do not treat an
  unexplained leak as an expected cleanup failure.
- A shortened diagnostic soak cannot qualify a release.

Tests: repeated graph changes, cancellation, DNS external edits, adapter-name
reuse, abandoned adapters, busy/Zombie driver, failed reset, changed driver
identity, close retry, forced exit, and daemon restart. Use fault injection for
unsafe or nondeterministic failure states and identify it in the evidence.

Milestone acceptance: packet captures and native state measurements prove
routing and cleanup on each qualified matrix entry. Ordinary lifecycle tests
also pass with normal Windows Firewall policy enabled.

## Milestone 9: Integrate gates and operation documentation

### Step 23: Connect checks to project commands and CI

Extend `just lint` and `just test-total` with the relevant platform checks.
Keep Linux Docker E2E coverage. Add explicit Windows commands:

| Proposed command | Purpose |
| --- | --- |
| `just build-windows` | Cross-build packages, executables, and test binaries for amd64/arm64. |
| `just winvm-doctor` | Check host prerequisites and locked inputs. |
| `just winvm-image` | Build or reuse the locked base image. |
| `just test-windows-vm` | Standard-user native tests and builds. |
| `just test-windows-e2e` | Privileged daemon/backend integration tests. |
| `just test-windows-flow` | Isolated packet captures and validation. |
| `just test-windows-resource` | Resource, recovery, and soak tests. |
| `just qualify-windows ENTRY EVIDENCE` | Validate all required evidence for one support claim. |

Acceptance conditions:

- `just lint` checks Windows-only code through the target environment.
- `just test-total` runs the existing host suite. On configured VM hosts,
  `RUN_WINVM_TESTS=1` adds the required Windows baseline and integration gates.
  Native Windows dispatch uses the Windows runner rather than attempting Linux
  Docker cases.
- A documented host-only command remains available without KVM/media, but its
  result cannot stand for the full Windows gate. Full requested gates fail on
  missing prerequisites; they do not silently skip them.
- Qualification additionally requires flow/resource suites and matching source,
  build-input, OS, architecture, toolchain, and driver/DLL identities.
- Keep per-platform unit and instrumented-daemon coverage. A merged percentage
  cannot mask untested Windows code. Record any non-instrumented flow cases.

Tests: Linux `just lint` and `just test-total`; native Windows aggregate tests;
target builds/vet/lint; harness checks; required-suite failure; stale evidence;
source mismatch; unexpected binary identities; and missing coverage artifacts.

### Step 24: Document operation and packaging inputs

Update architecture notes, schemas, CLI help, Windows examples, developer/VM
setup, troubleshooting, and dependency/license records. Define installer inputs
without implementing the installer.

Acceptance conditions:

- A developer can reproduce a build and VM run from a fresh checkout.
- Documentation states qualified platforms, feature limits, path/ACL rules,
  admin authorization, service identity, dependencies, and recovery procedures.
- No generated panel files, VM state, or private test inputs enter Git.
- The final evidence set comes from one recorded source revision and its exact
  generated inputs. Backend qualification is not substituted for Almagest tests.

Tests: schema validation, example startup, documented command checks, a clean
build, and complete qualification for each claimed platform.

Milestone acceptance: required project gates pass and operation documentation
matches the tested Windows behavior. Installer/package delivery remains out of
scope.

## Execution order and completion rule

Use this delivery order:

```text
M1 contract -> M2 build/VM -> M3 backend -> M4 files/ACLs -> M5 admin
           -> M6 lifecycle/trust -> M7 graphs -> M8 native evidence -> M9 gates/docs
```

Tests are added with their implementation steps, not deferred until M8. M2
introduces the harness and baseline; M8 adds full daemon flow and recovery
qualification. Update the test manifest as each milestone completes.

For every milestone, run the applicable existing regression tests and its new
checks. Use `just lint` and `just test-total` as the project aggregate gates.
Record unavailable prerequisites and failed checks explicitly. They cannot be
reported as passes. A milestone is complete only when every step's acceptance
conditions and tests pass, together with its milestone acceptance conditions.

Build success alone is never a Windows support qualification.

## Review notes and remaining design decisions

The document review checked scope, step dependencies, backend limits, and test
evidence requirements. It corrected these points from the initial outline:

- Distinguish dependency provisioning from Wintun's native adapter behavior.
- Assign split-driver service startup to external provisioning.
- Preserve required in-repository module replacements and regenerate ignored
  build inputs from recorded sources.
- Separate early contract/baseline gates from later native integration gates.
- Require separate Windows 11/arm64 execution evidence.
- Distinguish in-process recovery from recovery after process termination.
- Protect pipe clients as well as the server and separate user/service names.
- Keep expected fault-injection state separate from resource leaks.

These choices remain implementation work with explicit completion points:

| Decision | Resolve by | Required result |
| --- | --- | --- |
| Service identity and privilege requirements | M1, step 2 | Document the supported identity and permissions for each operation. |
| Trusted owners, config/storage ACLs, and accepted path forms | M4, step 10 | Define the policy and prove allowed/denied access with native tests. |
| User/service pipe names, default client selection, and allowed SIDs | M5, step 12 | Define a deterministic endpoint and authorization policy. |
| Web client-owner user boundary | M5, step 13 | Support and test the optional best-effort Windows account allowlist and the unfiltered warning; keep API-token authentication outside this plan. |
| Windows authority stores and fallback policy | M6, step 16 | Define identity-specific discovery and test reload/failure behavior. |
| Resource budgets and settling windows | M8, step 22 | Set numeric criteria before collecting qualification evidence. |

These decisions do not block work on earlier independent steps. They must be
resolved before their owning milestone can pass.
