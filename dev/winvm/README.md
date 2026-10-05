# Disposable Windows VM harness

This Linux QEMU/KVM appliance targets Windows Server 2022/amd64 only. It does
not qualify Windows 11 or arm64. Use separate Windows 11 test environments,
including a native arm64 host, for those targets. An amd64 guest or an emulated
application cannot provide arm64 qualification.

The image, QGA client, and lifecycle helpers are adapted from
`asciimoth/sysnet-windows/dev/winvm` at revision
`a056c520f0faf966f54dd90ef63b15d9aa7382ec`. This adaptation changes the project
paths, source packaging, guest toolchain, dependency provisioning, SSH key
verification, and run/test orchestration.
The Windows, VirtIO, and OpenSSH locks use
the integration plan's reference inputs. Go is locked to 1.26.6; its hash is
from the Go download manifest. No sibling checkout is needed. Image
provisioning installs the locked Wintun DLL and compatible split-driver service
as external test inputs. The daemon does not provision them.
Do not run privileged networking tests on a normal workstation or shared host.

## Host setup

Use an x86_64 Linux host with writable `/dev/kvm`, 8 GiB of available memory,
and at least 90 GiB of free cache storage. Install QEMU, OVMF, xorriso, OpenSSH
client tools, Python 3, jq, curl, Git, tar, flock, coreutils, Go 1.26.6 or newer,
Node.js, npm, ShellCheck, Ruff, PowerShell 7, and PSScriptAnalyzer. The Nix
development shell supplies the lint and VM packages on
x86_64 Linux. It also sets `MULLVAD_SPLIT_TUNNEL_DRIVER_DIR` and
`SYSNET_WINDOWS_WINTUN_DIR` to the pinned runtime dependency trees. The winvm
tools use these paths directly. Set `WINVM_OVMF_CODE` and `WINVM_OVMF_VARS` if
firmware is not in one of the paths listed in `common.sh`. Use a matching
firmware pair.

For packaging or use outside the development shell, build the combined runtime
dependency tree with `nix build .#windows-runtime-dependencies`. The separate
`windows-split-tunnel-drivers` and `windows-wintun` outputs have the same
architecture-specific layout.

Copy `env.example` to `env` and set absolute paths to the Windows and VirtIO
ISOs. Supply legally obtained media with the exact hashes in `image-lock.json`.
Media are not downloaded. Go and OpenSSH archives are downloaded over HTTPS and
checked against their locks during image creation. Keep the private cache,
media, and secrets outside the repository. The default cache is
`$XDG_CACHE_HOME/almagest/winvm`, or `$HOME/.cache/almagest/winvm`.

From the repository root:

```sh
just test-winvm-harness
just winvm-doctor
just winvm-image
just winvm-smoke
just winvm-test
just daemon-winvm
just test-windows-vm
just test-windows-e2e
just test-windows-resource
```

`doctor.sh --validate` checks configuration without VM prerequisites.
`doctor.sh --print-input-hashes` helps identify local media. Wrong hashes and
missing prerequisites fail; they are not skips.

`just daemon-winvm` opens a visible QEMU console, builds and runs `almagestd`
as Windows `SYSTEM`, forwards the daemon's loopback web panel to
`http://127.0.0.1:8091/`. Use `just daemon-winvm 9090` to select a different
host port. Set `WINVM_OPEN_BROWSER=1` to open the panel with `xdg-open`, or set
`WINVM_DISPLAY` to select another QEMU display backend. The manual overlay
automatically signs in to the standard `winvm` account and opens the Server
Core command environment. Its
password is `AlmagestManual1!`; automatic login and this password do not modify
the locked base image. Windows Server Core has no desktop browser, so the panel
is available through the Linux host. Run `curl.exe` and other console tools in
the visible Windows session when you need to probe graph services from inside
the guest. The manual run installs its matching CLI at
`C:\winvm\bin\almagest-cli.exe`. The comments in
`local/winvm/config/alm.hujson` show how to export the built-in TLS CA and CRL,
add them to the machine Windows certificate stores from an elevated terminal,
test intercepted HTTPS with `curl.exe` without certificate flags, and remove
both objects after the test.

Manual daemon state is synchronized with the gitignored `local/winvm/`
directory. It contains `config/`, `store/`, and `logs/`. The state is restored
at startup and saved during a clean Ctrl+C shutdown. The default QEMU close
button is disabled to prevent an abrupt exit from losing recent state; use
Ctrl+C in the terminal that runs `just`. Set `WINVM_STATE_DIR` to use another
host directory. Manual runs do not produce Windows qualification evidence.

Image creation performs unattended installation, installs QEMU Guest Agent
and OpenSSH, and creates a standard account named `winvm`. Windows Firewall
stays enabled; one guest rule permits SSH. Authentication uses a private cache
key. The image builder obtains the SSH host key through its private QGA socket
and then verifies SSH against that key. Password authentication is disabled.
QGA has SYSTEM privileges and is available only through a private host socket.

## Isolation, source identity, and evidence

Each run has a unique `artifacts/winvm/run-*` directory, qcow2 overlay, firmware
copy, sockets, and locked loopback SSH port. A shared cache lock serializes VM
runs and image creation to limit memory use. Per-image and per-run locks also
protect recovery runs. Port contention fails before a run can claim success.
Readiness requires two matching provisioning-token and Windows-boot-time
observations. Provisioning, guest commands, transfers, and shutdown have limits.
Both image creation and test runs use `host,cet-ss=off,cet-ibt=off`.
This mitigates Windows CET/shadow-stack failures with QEMU 11 and Linux 7.
See the [upstream Windows CET mitigation](https://lore.proxmox.com/pve-devel/20260515155810.229819-1-f.ebner@proxmox.com/).
The investigated Server 2022 guest stalled in kernel TLB-flush code and later
crashed in a shadow-stack write. Re-enable CET only after validation on a fixed
host stack, not merely after increasing a timeout.
The CPU setting is part of the image cache key. An older cached image is not
silently reused after this change.

`source.tar` contains current tracked files and eligible untracked files, with
tracked deletions omitted. Clean and dirty trees use the same policy.
`source.json` records the revision, dirty state, archive SHA-256, and each
included file hash. Archive metadata is deterministic. Source symlinks fail
packaging because Windows extraction must not follow links outside staging.

Packaging excludes Git metadata, local environment files, conventional secret
directories and key files, VM media/disks/firmware, caches, artifacts, and ignored
build output. Arbitrarily named credentials cannot be detected: keep them outside
the repository. The full policy is in `package-worktree.py` and applies even to
tracked files. Five named public certificate fixtures are included; the packager
rejects private-key data in these fixtures. Review `source.json` when adding new
local inputs.

The host extracts that archive into the run directory and regenerates the Go
workspace, certificate catalog, and embedded panel there. It uses the archived
catalog data and npm lock. `generated.sha256` and `generator-versions.txt` record
the generated files and generator tools. `payload.sha256` identifies the complete
guest payload; the guest verifies that hash before extraction. The checkout's
ignored workspace and assets are not used or changed.

The smoke run verifies transport, standard-user identity, OS, architecture,
toolchain, and artifact retrieval. It records `testExecution: not-run`:
**this is not the Windows test-suite gate or a support qualification.**

`just winvm-test` runs the active native gate from `test-suites.json`, then the
isolated two-guest packet-flow gate. The
ordinary suite runs all tests in `certcat`, `lib`, and `bin` through the
standard `winvm` SSH account. The privileged suite runs as SYSTEM through the
private QEMU Guest Agent. It is serial and uses `go test -p=1 -parallel=1` for
each Go case, so later ACL, service, driver, and global-network cases can use
the same gate without overlap. Add such cases only when their implementation
milestone makes them mandatory.

Each Go command writes `go test -json` events. The guest validator requires a
passing process result, complete package events, and every test explicitly
listed by the case. A missing test, skip, failure, corrupt event, timeout, wrong
identity, missing suite artifact, or artifact retrieval failure fails the run.
The host independently requires passing ordinary and privileged artifacts.
Each Go command also writes an atomic coverage profile. After validation, the
host combines and publishes these profiles in `coverage/winvm`. The final
coverage report includes that profile when it is present.
The execution manifest sets a Go package timeout, a hard deadline for each Go
case, and a progress interval. The package timeout lets Go emit its timeout
stack dump. The hard deadline also covers package loading and compilation. A
case can override the default hard deadline with `timeoutSeconds`. The host
prints `RUN`, `WAIT`, `TIMEOUT`, `PASS`, and `FAIL` progress. Each heartbeat
also records the active Go process tree and guest memory and disk state under
the suite's `diagnostics` directory.
The active gate also builds and starts the production daemon and CLI. Focused
named-pipe cases send CLI pings through standard-user and Local System daemon
pipes, verify their protected access rules, and require the user pipe to reject
the CLI machine trust class. The other standard-user daemon cases cover live
config, subscriptions, snapshots, TLS, restart persistence, startup failures,
and a local SOCKS/HTTP data path. An elevated daemon case creates a native
adapter, records its adapter, route, and DNS state, stops the daemon gracefully,
and requires the owned state baseline after the first run and after restart.
These daemon cases do not replace the separate packet-capture and soak gates
from Steps 21 and 22.

## Isolated packet-flow evidence

Step 21 uses two disposable Server 2022 guests. Each guest has its management
adapter plus two test adapters. The test adapters use separate QEMU stream
links named `tunnel` and `underlay`. QEMU capture filters write one PCAP for
each link. Management traffic is not present in either capture. Run
`flow-network.ps1 -Role Client` and `flow-network.ps1 -Role Endpoint` in the
applicable guests before the Almagest graph starts. Limit a firewall relaxation
to the endpoint guest and restore it before another case uses that guest.

The graph runner writes one JSON object per probe to
`packet-flow-observations.jsonl`. Each token is unique ASCII text carried by
its TCP or UDP probe. Each assertion immediately follows a positive control
that proves capture visibility. The required cases are included, excluded,
descendant, pre-existing IPC target, nested WireGuard, application-owned DNS,
shared Windows resolver DNS, underlay failure, and reconnect. The matrix covers
IPv4-only, IPv6-only, and dual-stack profiles. Underlay-failure observations
use `none`; a token on either test link is an unrestricted fallback. Shared
resolver observations use `attribution: "shared-resolver"` because the Windows
resolver process cannot prove requesting-application ownership.

Validate evidence independently on the host:

```console
just winvm-validate-flow packet-flow-observations.jsonl tunnel.pcap underlay.pcap packet-flow-evidence.json
```

The validator fails for an absent profile, case, marker, control, capture,
incorrect-link marker, fallback marker, duplicate or overlapping token, or an
application attribution claim for shared resolver traffic. It removes a stale
derived report before it parses new input. Keep the two PCAP files, observation
log, generated report, guest network inventories, graph snapshot, and daemon
log together. Host-only negative fixtures run as part of
`just test-winvm-harness` runs the host-only harness tests. `just lint-winvm`
runs ShellCheck, Ruff correctness checks, and PSScriptAnalyzer. `just lint` runs
both gates.

## Resource and recovery qualification

`just test-windows-resource` runs the privileged Milestone 8 resource gate in
one disposable Server 2022 guest. The gate starts with 10 warm-up lifecycle
cycles. It then runs four measured batches of 25 lifecycle cycles, 100
cancellation cycles, and a 30-minute UDP transfer and TUN reconfiguration soak.
It also runs 25 production-daemon graph attach/detach cycles and a forced daemon
termination followed by production-path restart and cleanup.

The gate waits 10 seconds after cleanup and compares complete stable adapter,
address, route, and DNS snapshots. It records handle, goroutine, and private-byte
samples after warm-up and after each measured batch. The limits are fixed at no
more than 8 additional handles, 4 additional goroutines, and 64 MiB additional
private memory between the first and last measured samples. The independent
host validator checks the snapshots, samples, workload, limits, required cases,
and recovery declarations in the retrieved archive.

Set `ALMAGEST_WINDOWS_SOAK_DURATION=2m` only to investigate the gate. The
result is marked `qualifying: false`; the validator permits it as a diagnostic
artifact but does not accept it as release qualification. A full qualification
does not set this variable.

Busy close, failed reset, external DNS edits, adapter identity reuse, zombie
driver state, and changed driver identity use deterministic fault injection.
Their evidence names the retained owned or foreign state and the required
recovery. Almagest does not claim a persistent cross-process split/WFP recovery
journal. If split cleanup is uncertain, stop Almagest and the split-driver
service, preserve the logs and WFP ownership data, remove only independently
verified Almagest-owned objects, and then restart the provisioned service.

Keep `resources-before.json`, `resources-after.json`,
`process-resources.json`, the four JSONL event logs, and `suite.json` together.
The suite record alone is not resource qualification evidence. Windows Firewall
remains enabled during this gate.
The host-only harness tests run through `just lint` without starting a VM.
`just test-total` always runs the harness. Set `RUN_WINVM_TESTS=1` to also run
the combined standard-user, elevated, and packet-flow `just winvm-test` gate.
Aggregate E2E recipes keep the Docker E2E suite on Linux and join the enabled
VM gates before coverage reporting. When enabled, the native gate requires the
configured KVM host, locked media, and WinVM prerequisites.

## Project and CI gates

`just lint` runs the host linters, the host-only VM harness tests, and
`golangci-lint` for Windows amd64 and arm64 with the Windows integration build
tags. It does not start a VM. `just test-total` is the documented host-only
aggregate when `RUN_WINVM_TESTS` is unset. It cannot qualify Windows support.
Set `RUN_WINVM_TESTS=1` only on a configured Linux KVM host. This makes both
`just check` and `just test-cycles` run the combined standard-user, elevated,
and packet-flow gate. A missing image, locked input, driver, or other
prerequisite then fails the requested gate.

On a native Windows runner, `just test-windows-vm` invokes the PowerShell suite
runner for the standard account and `just test-windows-e2e` invokes it for the
elevated account. The platform E2E dispatcher does not try to run the Linux
Docker cases on Windows. CI uses separate runner identities for these jobs.

Validate retained flow evidence with:

```console
just test-windows-flow /absolute/path/to/flow-evidence
```

The directory must contain `packet-flow-observations.jsonl`, `tunnel.pcap`, and
`underlay.pcap`. The command writes `packet-flow-evidence.json`. It validates
existing captures; it does not create them or replace the isolated two-guest
capture run.

After the native, flow, and resource runs are complete, validate one support
entry with:

```console
just qualify-windows windows-server-2022-amd64 /absolute/path/to/qualification.json
```

The strict JSON record uses the suite and test IDs in
`contracts/windows/test-manifest.json`. Each required suite records the same
source revision, source archive, generated-input manifest, OS, architecture,
Go toolchain, Wintun DLL, split driver, daemon binary, and CLI binary identities.
Core, privileged, and daemon E2E suites require individual coverage hashes.
Packet-flow and resource suites must set `nonInstrumented` to `true`. A stale,
missing, skipped, wrong-platform, mixed-identity, or shortened diagnostic
result fails qualification.

## Failure and recovery

Runs retain disk, firmware, serial and QEMU logs, console output, source and
payload hashes, image identity, and `run.json`. The host supervisor owns QEMU
directly. It checks guest command execution independently of test output.
Silence from a test does not mean that Windows is frozen.

The limits in `config.json` have separate meanings:

- `testTimeoutSeconds` is one shared deadline for ordinary and privileged
  tests. A host test timeout does not start another phase with a new budget.
- `healthIntervalSeconds` sets the guest health check interval (15 seconds).
  `healthTimeoutSeconds` limits consecutive failed checks (120 seconds).
  SSH is checked first; QGA command execution is the fallback.
- `artifactIntervalSeconds` sets periodic host artifact copies (60 seconds).
  A failed or incomplete copy does not replace the last valid archive.
  If Windows locks an active log, the other readable files are retained in
  `guest-artifacts.snapshot.tar`, explicitly marked as a partial snapshot.
- `cleanupTimeoutSeconds` bounds diagnostics, shutdown and recovery together
  (300 seconds). Mandatory TERM/KILL/reap has a final six-second reserve.
- `recoveryBootTimeoutSeconds` bounds the one evidence-only restart (120 seconds).

A normal Go timeout remains a failed test. A host test deadline remains a
failed run even if the final guest artifact claims success. An unresponsive
guest is reported separately as `guest-unresponsive`. Recovery never reruns
tests or converts a failed attempt into a pass. `test-cycles` records that
failure, then starts its next independent iteration with a fresh VM.

Failure and Ctrl+C collect bounded QMP state, CPU registers, a screen image,
guest process and network state, Windows events, small crash dumps, active
test tails, and partial test artifacts. The supervisor stops only this run's
guest test launchers before final collection. It then stops and reaps QEMU.
If the guest froze or final artifact export failed, it boots the retained disk
once to export the logs. No test command runs during this recovery boot.
Original and recovery QEMU, serial and diagnostic logs have separate names.
The last pre-recovery archive is also retained.

`supervisor.log` records host decisions. `collection.json` records each archive
attempt and whether the final copy succeeded. `run.json` records the failure
kind and recovery result. `diagnostics.log` and `recovery-diagnostics.log`
contain host diagnostics; guest reports are inside `guest-artifacts.tar`.
The full kernel dump, if present, stays on the retained disk. Memory dumps
can contain sensitive guest data.

`test-cycles` sends its stop signal to the supervisor, not its collectors. It
allows the configured cleanup budget plus 15 seconds before its last-resort
kill. Further Ctrl+C signals do not cancel collection. No guest response is
required to enforce the host deadlines or terminate QEMU.

Collection is best effort: a guest that cannot boot or read its disk cannot
export all logs. In that case the host keeps existing copies, partial archive,
disk and firmware, and explicitly reports incomplete collection. It does not
claim that missing logs were recovered. A process that survives KILL is
recorded and blocks subsequent runs until that exact process has stopped.
There is no unbounded process wait. Host power loss or SIGKILL of the supervisor
cannot provide the same collection guarantee as a handled Ctrl+C/SIGTERM.

The failure message prints this recovery command:

```sh
just winvm-shell /absolute/path/to/artifacts/winvm/run-XXXXXXXX
```

Recovery reuses that run's overlay and firmware and checks its base image and
SSH identity. Do not run recovery while the original process is still active.
Failed installation work remains in the private cache with its provisioning
ISO and logs; `just winvm-image` retries installation in a new directory.
These files can contain generated credentials. Successful image creation removes
its temporary provisioning media. All run directories, including successful
runs, are retained for inspection; remove an exact run directory only after its
QEMU process has stopped and its artifacts are no longer needed.

Native provisioning and smoke execution require the locked media and host tools.
Host-only tests do not prove that native provisioning succeeded.
