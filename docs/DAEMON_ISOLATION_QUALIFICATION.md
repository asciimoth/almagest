# Complete isolation profile qualification

Checkpoint 30 adds the strict complete-profile evidence gate in
`bin/internal/isolation/qualification` and the development-only
`isolation-qualify` command. It does not enable isolated daemon startup. A
profile stays unavailable until one evidence record passes all checks. Direct
mode is unchanged.

## Evidence boundary

The gate has closed manifests for Linux amd64 and Windows Server 2022 amd64.
Each record must identify the exact profile, platform, source revision, launch
credential class, and protected-settings digest. Direct and isolated parity
runs must use the same credential class and protected settings. The baseline
runs cannot use optional restrictions.

Every manifest feature needs both a direct and isolated result. Both results
must pass. A native limitation is accepted only when both modes report the
same nonempty limitation identifier. Thus, a missing isolated feature cannot
be reported as native unavailability. Missing, duplicate, unknown, failed, or
skipped results reject the complete record.

The feature inventory includes regular and default TUN, native and userspace
listeners, UDP metadata, outbound TCP and UDP, application and host DNS, rule
completion and matching, commands and callbacks, both admin protocols, config
transactions, startup and live snapshot operations, logging, crash recovery,
partial native capability, and direct-mode process/transport absence.

Linux also requires explicit results for eBPF process filtering, kernel and
userspace process rules, marks on existing sockets, fork/exec/exit changes,
process-owner lookup, and DNS provider selection. A socket-only matcher test
cannot cover these results. Native checks must record the process backend's
retained authority, denial of arbitrary process operations requested by R, and
cleanup. A separate process helper or reduced N token is not required. These
requirements do not apply Linux-specific mechanisms to the Windows manifest.

Compare every feature with direct mode under equal launch authority. Do not
disable process rules, eBPF, or a selected DNS provider to make the isolated
profile pass. An actual native limitation must be observed in both modes;
an isolation restriction or missing adapter is not a native limitation.

Optional policy tests are a separate group. They cover default-route denial,
host-DNS denial, command denial, and resource limits. They cannot replace an
unrestricted parity result.

## Hostile runtime requirements

Every hostile-runtime observation must pass. The manifest covers arbitrary
runtime code, forged identity and callback origin, path escape, policy changes,
host handles, foreign resources, arbitrary commands, broker exhaustion, host
files and sockets, broker process or token access, malicious persisted config,
all-thread restrictions, inherited handles, and control progress during
overload.

Both manifests also require proof that backend roles execute no node code and
offer no generic host-operation API to R. The native backend is trusted code:
backend DNS parsing, process inspection, and backend-selected utility execution
are allowed in N under existing launch authority. These checks do not weaken
R's all-thread, filesystem, network, process, or inherited-handle restrictions.
Compromise of N itself is outside this node-containment claim.

The revised manifest replaces mandatory process-helper checks with
`process_backend_authority`, `process_backend_runtime_request_denial`, and
`process_backend_cleanup`. Both platforms add `backend_node_code_absent` and
`backend_generic_host_api_denied`. Earlier component evidence cannot satisfy
these by renaming results; producers must measure the actual revised launch
path. No production profile was qualified under the earlier manifest.

Qualification also requires at least eight tested runtime threads, no
unexpected inherited handle, and experimental classes absent. The overload
boundary check must show control progress, but it has no performance target or
minimum duration. Platform manifests add native TUN flow, IPv4/IPv6 listeners,
route/firewall/DNS cleanup, matcher lifecycle, and final restriction checks.
The Windows manifest also requires the service network role and native flow
qualification. Cross-compilation is not native evidence.

The JSON decoder has a 256 KiB limit, rejects duplicate object members, rejects
unknown fields and trailing values, and validates exact manifest membership.
This prevents an evidence producer from using ambiguous input or silently
adding an unreviewed result type.

Use the explicit development entry point after the Docker or Windows VM runner
has produced one complete record:

```sh
go -C bin run ./cmd/isolation-qualify -list-profiles
go -C bin run ./cmd/isolation-qualify -evidence ../artifacts/isolation-qualification/evidence.json
```

Portable validation and fuzz checks are:

```sh
go -C bin test -race -cover ./internal/isolation/qualification
go -C bin test ./internal/isolation/qualification -run '^$' -fuzz '^FuzzEvidenceDecoder$' -fuzztime=30s -parallel=2
go -C bin test ./internal/isolation/qualification -run '^$' -fuzz '^FuzzEvidenceValidation$' -fuzztime=30s -parallel=2
```

Run native producers only in the disposable Docker environment or locked
Windows VM. Do not run native TUN, route, firewall, DNS, or hostile socket
probes on the host.

## Current qualification state

No complete daemon profile has passed the frozen manifest. Linux implements
trusted N preparation, restricted R/C/H, bounded role cgroup provisioning,
native backend comparison, a restricted-runtime composition test, and a real
daemon-engine composition with an N-owned web listener. Separate
upstream DNS and p-mark fixtures provide additional component coverage; they
are not complete direct/isolated daemon parity evidence. See the
[Linux component reference](DAEMON_ISOLATION_LINUX_PROFILES.md).

The native broker path now records durable ownership before a TUN mutation and
requires cleanup to resolve the record. Platform identity inspectors run before
readiness and after revocation. The restricted C executable also has a bounded
readiness exchange and a real Linux fixed-target transaction test. These close
component and recovery gaps, but they do not synthesize complete-profile
evidence.

Storage quotas and integrated recovery/hostile-runtime evidence remain
required. Performance evidence is diagnostic only. Windows has
trusted caller/service N, persistent deployment identities, and final-token
native component evidence for Windows Server 2022/amd64, but does not yet have
the complete-daemon evidence required by this manifest. Missing results fail
qualification; they are not successful skips.

Production isolated startup remains disabled. Checkpoint 32 must not enable
a profile before complete evidence passes this gate. Direct mode remains
available through `--no-isolation`, which experimental classes also require.

The validator has race-enabled tests and structured/raw JSON fuzz targets.
Passing those tests proves validator behavior, not acceptance of a platform.
Native producers run only in disposable Docker environments or a locked VM.

The qualification package now also has a concurrent `Recorder` for an actual
producer. It accepts only IDs from the selected closed manifest, rejects
duplicates, keeps failed observations, orders snapshots by the manifest, and
does not change caller-owned launch data. A snapshot must still pass `Validate`;
the recorder cannot turn an incomplete or failed run into evidence. Race tests
cover concurrent observation delivery and independent snapshots. A two-worker,
30-second ID fuzz run completed 1,971,549 inputs. This supplies the evidence
assembly boundary, but the platform runners still must measure and submit all
observations from complete daemon runs.

## Checkpoint 30 integration audit, 2026-10-11

The rule-control gap is fixed and has focused hostile-input, cancellation,
peer-loss, fuzz, and native parity tests. See the
[rule validation and completion contract](DAEMON_ISOLATION_MATCHERS.md#rule-validation-and-completion-integration).
The address allocator now has typed broker dispatch, native parity checks, and
graph-owned runtime adapters. See the
[address allocator contract](DAEMON_ISOLATION_NETWORK_CONTROL.md#address-allocator-integration).
The runtime client now supplies a complete `sysnet.System` adapter to
`almagest.BuildEngine`. See the
[runtime system integration](DAEMON_ISOLATION_NETWORK_CONTROL.md#runtime-system-integration).
The adapter and development role assembly are complete. Platform release
qualification remains separate and fail closed.

The following qualification gaps still prevent a release evidence record:

- `isolation-qualify` validates supplied evidence. The package can assemble
  measured observations with `Recorder`, but no platform runner yet launches
  the full matrix and supplies every required observation.
- The development composition runs the real daemon engine and relayed web
  admin in R. One retained run executes a command node through H, performs a
  config transaction and live snapshot restore, then verifies that loss of N
  stops R and H. Separate C and H tests also exercise their final native
  restrictions.
- No complete-composition evidence covers the arbitrary-code boundary,
  restart/recovery, and all launch credential classes. The isolated Windows
  packet-flow and resource gates provide component evidence, not a complete
  profile record.

These are isolation integration gaps, not native-backend limitations. The
closed qualification manifests and production startup gate remain unchanged.
A complete record must come from actual complete-daemon runs; component results
must not be renamed or copied into passing manifest entries.

### Windows matrix and resource audit, 2026-10-11

The full native Windows matrix passed 22 ordinary-user and 23 privileged cases
in `artifacts/winvm/run-cGhplHPT`. Its source archive SHA-256 is
`3092184910006d70fbf959534a5742683805ce14e82bad49c90133e5f0db9336`.
The first packet-flow attempt then found that a 30-second guest-command limit
was too short for route setup after the full matrix. The harness now gives the
bounded service and route commands 120 seconds. All 42 harness tests pass. The
isolated rerun in `artifacts/winvm/run-flow-XIuWFowU` passed and validated 116
packet observations.

The corrected resource runner now handles native Go stderr by its exit code and
accepts valid package-level JSON events that have no `Test` member. In
`artifacts/winvm/run-VrYT8eIb`, source archive SHA-256
`4c28b42587f56cbebbf8e1908c34f2e15e836906726b635d5f1fe40e2e4e66e5`,
the 30-minute native transfer soak passed in 1800.40 seconds, crash recovery
passed, daemon graph/restart passed, and both fault-injection groups passed.
Process handles grew from 483 to 496 after warm-up. This measured growth of 13
is accepted as the current Windows backend baseline.

The earlier full run in
`artifacts/winvm/run-PWwIrI63`, source archive SHA-256
`f855f2575a49bb067a88f70a7958624fb7c1a96c89d72aff87b727aec69f848d`,
grew from 489 to 508 handles. In both runs, goroutines stayed at 2 and handle
growth stopped after batch 2. The upstream dependency's trend check passes.
By maintainer direction, resource growth is now recorded without a pass/fail
threshold. Cleanup of native adapters, addresses, routes, and DNS state remains
mandatory.

The updated evidence schema passed a shortened qualifying run in
`artifacts/winvm/run-izzlrWMD`, source archive SHA-256
`d5d3dcf819550ef6166e7807ec27bad9d65be376d2356b1147221db9e638b5c0`.
All four resource cases and the independent host validator passed. The run
recorded growth of 9 handles, 0 goroutines, and 2,265,088 private bytes. Soak
duration is recorded but does not affect qualification. The earlier
full-duration run remains useful stability evidence.

### Audit verification results

The worktree started at `948c8c62cefe73be110c13f569227466e8c02503`.
Checks ran on Linux 7.2.8 amd64 with Docker 29.8.1. Portable/race/fuzz and
native Linux rule checks used `golang:1.26.6-bookworm`; the project check
container mounted the development tools, including Go 1.26.8. All network
probes ran inside disposable containers or the locked Windows VM.

- Focused `just check` passed for the new rule-control, runtime-client, and
  Linux native rule tests. Its normal lint, vet, formatting, and Windows
  amd64/arm64 static checks ran. This was not a full-project test run.
- Race tests passed for `network`, `runtime`, `transport`, and `protocol`.
  The control rejection, capability, cancellation, and stale-result tests also
  passed five repeated race runs. Both read operations were tested against
  cancellation, timeout, graph changes, and revocation.
- `FuzzRuleContext` passed 445,951 executions in 30 seconds with two workers.
  `FuzzRuleValidationReply` passed 13,621 executions in a separate 30-second
  campaign with two workers. These are bounded smoke campaigns.
- Linux native rule parity passed for all eight advertised rule types, both as
  container root and as UID/GID 65534 with all container capabilities removed.
  Neither run used host networking.
- `just test-total winvm:m30::rule-control-parity` passed as the ordinary user
  on Windows Server 2022 amd64. The VM harness validated the required tests;
  this was native execution, not a cross-build. The retained run is
  `artifacts/winvm/run-dbTDJlh4`, with source archive SHA-256
  `1bb1fb93febe402c489ca67163d5de753b98205be5e8a260064f039d1d5026f1`.
  Later portable cancellation/capability regression checks ran in Docker.

Command output is in `artifacts/isolation-qualification/rule-control/`:
`check.log`, `docker-tests.log`, `final-race.log`, `repeated-control.log`,
`linux-ordinary.log`, and `winvm.log`. No complete qualification JSON was
produced. The full daemon matrix and service-token composition remain open.


### Allocator verification results

- Focused `just check` passed for the new allocator tests, including the normal
  lint, vet, formatting, and Windows amd64/arm64 static checks. Optional-policy
  denial tests also passed with the race detector in Docker.
- Race tests passed for the complete `network`, `runtime`, `protocol`, and
  `transport` packages in Docker.
- Two-worker, 30-second fuzz campaigns passed: `FuzzAllocationContract` ran
  1,846,720 inputs and `FuzzAllocatorNativePrefixes` ran 1,796,718 inputs.
  These are bounded smoke campaigns.
- `TestLinuxAllocationParity` passed all IPv4/IPv6 IP and subnet cases in
  Docker with the race detector and no host network access. It also passed as
  UID/GID 65534 with all container capabilities removed.
- `just test-total winvm:m30::allocator-parity` passed and the VM harness
  validated its required tests as the ordinary Windows user. The retained
  run is `artifacts/winvm/run-fBAwA5Ub`, with source archive SHA-256
  `ec7fdbc707ade958344f37b4a690646a40c07d306dd4c22fd0c18309e72a36a0`.
  The suite selector was then rewritten with full words to pass the spelling
  check; its selected test set is unchanged.

Logs are in `artifacts/isolation-qualification/allocator/`. These results
cover allocator components. They are not complete-profile evidence.

### Runtime system verification results

The complete runtime adapter preserves the native capability inventory and
per-TUN revisions while it removes native files and handles. It supplies
outbound and local networks, host DNS, typed allocation and rule operations,
matchers, stable regular and default TUN objects, all TUN updates and reads,
warnings, application DNS, and close ownership. Default-TUN replacement and
every in-place update can change the broker generation without changing the
object held by a runtime node.

Application DNS endpoints are created when a runtime node calls `SetDNS`.
The request has no address, provider, executable, or host selector. The
endpoint belongs to the graph epoch and is closed on detach, replacement,
peer loss, graph release, or system shutdown. Thus each node can supply its
own resolver after startup without receiving native DNS authority.

- Race tests passed for the complete `network`, `runtime`, `protocol`, and
  `transport` packages in a networkless Docker container.
- Two 31-second, two-worker `FuzzSystemTunOptions` campaigns passed 1,282,971
  and 698,971 inputs.
- Exact Linux native capability parity passed in Docker. The trusted native
  composition also passed with a restricted runtime, real TCP/UDP and DNS
  traffic, owner matching, regular/default TUN creation, application DNS, and
  final interface, DNS, and recovery-journal cleanup. It had no host network.
- Native Windows capability and runtime adapter parity passed through the
  locked Windows VM selector `winvm:m30::runtime-system-parity`. The final
  retained run is `artifacts/winvm/run-KCA9s86f`, with source archive SHA-256
  `4ae853dd2ab1c163480ffeea088e5d004a782efc2dcd8be9a06ce880df7a5057`.

Logs are in `artifacts/isolation-qualification/runtime-system/`. This closes
the system-adapter assembly gap. `trusted-linux-final.log` covers native backend
parity, the restricted adapter composition, C, H, and the actual daemon engine
with its relayed web admin. `runtime-race-final.log` and
`role-assembly-race-final.log` cover the affected packages with the race
detector. `runtime-bootstrap-fuzz.log` records 2,425,136 inputs in a 30-second,
two-worker `FuzzRuntimeInput` campaign. These development results do not replace
the required recovery and Windows release evidence.
