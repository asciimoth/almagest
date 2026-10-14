# Daemon isolation implementation plan

Status: checkpoints 1 through 32 are implemented for the packaged Linux amd64
profile. Windows Server 2022 amd64 has isolation components under development,
but not a released isolation profile. Windows uses direct mode by default.
The project maintainer accepted
the current direct and isolated performance state on 2026-10-11. Performance
measurements are diagnostic and do not block later checkpoints.
It follows the [isolation architecture](DAEMON_ISOLATION_ARCHITECTURE.md).
Checkpoint 13 design is frozen as broker-owned relays for all Linux/Windows
resources. By explicit maintainer direction, later performance work is optional
optimization after isolation is complete and working.
Its sequence takes precedence over the outline in architecture section 10.3:
first establish the complete direct-mode composition, then run the isolation
experiments, and only then select the isolated data paths.

Role names follow the architecture: R is the runtime, N is the network broker,
C is the optional config writer, and H is an optional restricted helper.

Design revision, 2026-10-10: isolate node/application code in R; treat the
native sysnet backend in N as trusted host code. Preserve R's full Linux or
Windows sandbox, C's fixed-target boundary, and node-command H restrictions.
N may retain launch authority and native DNS/process integration. Separate
DNS/BPF helpers and upstream API changes are no longer prerequisites. Earlier
restricted-N experiments remain historical evidence, not release requirements.
Checkpoints 24 and 25 implement and qualify the revised Linux and Windows
profile components. Checkpoint 32 connects and packages the Linux profile.

## Delivery rules

Each numbered section is a separate checkpoint. Split its work into smaller
changes if necessary. Do not merge a change that leaves the direct daemon
partly functional. A checkpoint is complete only when its acceptance conditions
and the common gate below pass.

Until the final default-mode switch, keep the existing direct startup as the
default and support explicit `--no-isolation`. Report it as direct mode, not as
isolation. Develop incomplete isolated compositions in test binaries or an
explicit development-only harness. Do not expose an incomplete profile as
production isolation. Do not make an incomplete isolated feature call a native
backend in R as a fallback.

The numbered order is the integration order. Platform prototypes can be
developed independently after the direct-mode checkpoints. Their results must
be available before the data-path decision. A failed experiment does not break
the shipping daemon; it blocks the dependent isolated feature or profile.

### Common acceptance gate

- The daemon starts, serves its admin API, runs configured nodes, saves state,
  and shuts down in direct mode. All existing conceptual features remain
  available under the same launch authority and protected settings.
- Run `just check` (include lints and tests) for implementation changes. Do not
  remove tests, weaken assertions, lower coverage requirements, or replace real
  integration tests with mocks to make a refactor pass. Mechanical package
  changes and explicit `--no-isolation` in launch commands are permitted.
- Run affected native integration and E2E tests at each checkpoint. At the
  direct-mode qualification and final qualification checkpoints, run the full
  platform matrix, including privileged Linux cases and Windows VM cases.
  `RUN_WINVM_TESTS=1 E2E_INCLUDE_PRIVILEGED=1 just check` enables these
  optional paths in the current test entry points. Record which suites actually
  ran: a skip is not evidence that a platform passed.
- Test elevated, ordinary-user, and partial-privilege launches where the
  platform supports them. A missing TUN capability must not remove unrelated
  features. Do not request elevation to make a test or feature work.
- New functionality has focused tests, including errors and shutdown. Each
  cross-process operation also has malformed-input, cancellation, stale-state,
  and peer-failure tests before it enters a production composition.
- Preserve the `lib`/`bin` dependency boundary. Read
  [runtime lifecycle pitfalls](RUNTIME_NODE_LIFECYCLE_PITFALLS.md) before any
  runtime node refactor. Keep both config schemas in sync when fields change.
- Record the change, commands run, environment, failures, and remaining
  limitations. An unavailable test host leaves
  the relevant acceptance condition open; it does not justify a silent skip.

These rules apply after every section below. Until section 32, the working
production checkpoint is the complete direct daemon; isolated work is additive.

## 1. Add the explicit direct execution mode

Status: completed.

Work:

- Add `--no-isolation` and select the execution mode once, before host changes
  or listener creation. Initially both the default path and this flag use the
  existing direct implementation.
- Require explicit `--no-isolation` with `--experimental`. Update experimental
  test launch commands and usage text. Do not refactor `XTorDriver`.
- Add startup/status reporting that does not claim a sandbox exists.
- Capture the current functional matrix and reproducible performance baseline
  before the subsequent refactors. Preserve the source revision and build flags.
- Reject starting daemon without `--no-isolation` flag reporting that isolation
  is not implemented yet.

Acceptance:

- Existing unit, integration, and E2E tests pass with no semantic changes;
  experimental launches need only the additional flag.
- Ordinary-user startup works. Invalid experimental flag combinations fail
  before any listener, network resource, or file mutation is created.
- There is one daemon process. No broker, IPC channel, or sandbox setup is
  introduced. Configured external programs remain permitted in direct mode.

## 2. Extract runtime construction and local service contracts

Status: completed.

Work:

- Separate argument handling and application construction from the body of
  `main`. Move engine, admin application, and store ownership toward
  `bin/internal/daemonruntime`.
- Introduce local contracts in `daemonservices` for host network access,
  config persistence, allowed commands, and lifecycle ownership. Use existing
  interfaces where they suffice. Do not create wire-message-shaped interfaces.
- Keep all roles in the original process, with explicit construction and close
  order. Keep the existing implementation behind each interface while moving it.

Acceptance:

- The complete direct daemon passes the common gate, including startup errors
  and shutdown during initialization. Each owned service closes exactly once.
- Application tests call the extracted implementation, not a retained second
  copy of the old daemon. `lib` has no new dependency on daemon packages.
- Local calls do not pass through serialization, RPC dispatch, resource-ID
  translation, transport queues, or a simulated session.

## 3. Separate local host-network control from runtime code

Status: completed.

Work:

- Extract native system construction and host control from daemon orchestration.
  Make an inventory of every used sysnet, Gonnect, TUN, resolver, and matcher
  method, including optional methods and native fast-path consumers.
- Keep application DNS nodes in runtime-owned code. The local network service
  may own the existing backend DNS provider and proxy, including packet
  processing. Do not require dependency refactors solely to split backend DNS.
- Preserve the direct backend's socket/TUN access, batching, errors, deadlines,
  capability reports, and truthful `IsNative()` values. Avoid a per-packet
  execution-mode branch.

Acceptance:

- The method inventory maps each feature to a local implementation and a future
  isolated owner. No feature disappears because it is difficult to proxy.
- Native outbound networks can still report `IsNative() == true`; transforming
  wrappers remain non-native where bypass would be incorrect. Native consumers
  exercise the same fast paths as before.
- Default TUN, routing, DNS, low/high/ephemeral listeners, UDP metadata, matchers,
  and platform-specific LocalNet behavior pass existing native tests.

## 4. Separate local config persistence from orchestration

Status: completed.

Work:

- Split `platform.Agent` responsibilities into bootstrap inputs and fixed-target
  regular-config persistence. Keep HuJSON parsing, graph reconciliation,
  revision tracking, and autosave in the runtime.
- Implement the local fixed-target persistence contract, including compare and
  replace and operation results. Do not add IPC or privileged file services.
- Keep trusted settings separate from mutable regular config. Preserve existing
  permission checks and the read-only-config case.

Acceptance:

- Text-config changes, autosave, revision conflicts, read-only files, disk-full
  failures, and graph rollback retain their externally visible behavior.
- Config operations use direct calls. Root-owned targets still work when the
  caller has access; ordinary-user targets need no new privilege or migration.
- A runtime request cannot select a different persistence path through the new
  contract. No protected settings become editable through the admin API.

## 5. Extract the snapshot and restore coordinator

Status: completed.

Work:

- Keep archive parsing, bbolt operations, storage copies, graph replacement,
  and notifications in the runtime. Pass startup snapshot input as a reader
  instead of making the future broker parse it.
- Make mutation coordination and service ownership explicit. Preserve immutable
  state reapplication, rollback, and `ConfigWriteError` behavior.
- Keep this change a local refactor; do not introduce broker epochs or change
  archive formats merely to prepare for IPC.

Acceptance:

- Existing startup/live restore, import/export, corrupt archive, rollback, and
  concurrent API tests pass with the same assertions.
- The worker and workspace views agree after success and rollback. Failed config
  persistence after a successful restore still has its documented distinct result.
- No archive, database, or general storage operation moves into host-control code.

## 6. Separate local command dispatch

Status: completed.

Work:

- Extract command dispatch and backend callback dispatch behind local contracts.
  Preserve protected allowlists, slot validation, and legacy direct-mode command
  support. Keep node commands distinct from backend-generated callbacks.

Acceptance:

- Existing allowed commands and callbacks pass, including errors, cancellation,
  and shutdown. Protected command rules remain in effect.
- Direct mode starts no role process for commands. Only configured external
  programs may create their required child processes.

## 7. Separate local logging ownership

Status: complete.

Work:

- Give each logical role a local logger. Keep runtime/node logs local and keep
  existing admin log behavior. Separate log delivery from lifecycle control.
- Prepare bounded subscriber delivery where needed; do not add a collector
  process, serialization, or a local imitation of remote logging.

Acceptance:

- Existing log subscriptions and shutdown behavior pass. A slow subscriber does
  not block network or control progress.
- Direct mode starts no role process for logs.
- Ordinary logging does not require IPC. Logging a remote-shaped fatal record
  in tests does not invoke the receiver's fatal-exit behavior.

## 8. Qualify the complete direct-mode refactor

Status: complete. Implementation, platform acceptance, and controlled same-host
library hot-path comparison are complete. Current native I/O performance is
accepted and is not a release gate.

Work:

- Add import-boundary checks for the extracted modules. Keep service logic
  independent of process launch and wire protocol packages.
- Run the full direct-mode test matrix against the baseline from section 1.
- Instrument child creation, private listeners, transport construction, native
  path use, and allocations. Retain measurements as an optimization baseline.

Acceptance:

- All original conceptual daemon features pass, including CLI/panel, integration,
  privileged E2E, and Windows VM tests. Test changes are limited to justified
  mechanical moves and launch flags, not weaker expected behavior.
- Startup, graph changes, matching, config writes, restore, and shutdown create
  no internal IPC or daemon role subprocess. The mode needs no sandbox account,
  AppContainer, namespace, or privileged storage migration.
- Native fast paths remain active. Direct mode adds no internal role process or
  IPC path. Performance measurements do not affect acceptance.

## 9. Define policy and contracts

Status: complete. The project maintainer accepted the current performance state
on 2026-10-11 and removed numeric performance gates. Data-channel selection is
recorded separately in checkpoint 13.

Contract review: [daemon isolation control contract](DAEMON_ISOLATION_CONTROL_CONTRACT.md).
Performance policy:
[daemon isolation performance policy](DAEMON_ISOLATION_PERFORMANCE_BUDGETS.md).

Work:

- Map the inventory to typed control operations, owners, arguments, results,
  limits, cancellation, and uncertain-result rules. Define session, resource
  type, generation, graph epoch, and operation ID semantics.
- Encode default authority from launch credentials and existing protected
  settings. Keep further restrictions optional. Distinguish supported,
  unsupported, and unknown capabilities; preserve valid attempt-on-unknown use.
- Specify fixed config targets, node versus callback command authority, and
  diagnostic records. Prohibit generic syscall, path, argv, ioctl, registry,
  driver, and arbitrary native-control requests.
- Record available measurements as diagnostic baselines. Do not make throughput,
  latency, CPU, memory, or resource-counter values qualification gates.

Acceptance:

- A contract review covers every inventory entry and default legitimate sysnet
  operation. Missing capability is not replaced by a new mandatory allowlist.
- Fake services test forged IDs, foreign ownership, stale generations, conflicts,
  cancellation, and lost replies without elevated privileges.
- Control contracts are versioned. Data-channel candidates remain open until
  section 13. Current performance is accepted without numeric thresholds.

## 10. Experiment: Linux restrictions and native resource access

Status: implementation and Linux amd64 qualification complete. The experiment
found native route-monitor and DNS-provider integration gaps that must be
resolved before a production Linux broker profile is qualified. See the
[Linux experiment report](DAEMON_ISOLATION_LINUX_EXPERIMENT.md).

Work:

- Build a development-only launcher/harness with actual UID and capability
  drops, namespaces, `no_new_privs`, syscall restrictions, and the proposed
  filesystem view. Test all Go threads and runtime operations.
- Check Landlock ABI and thread-wide enforcement options; do not assume locking
  one Go thread restricts the others. Test any allowed equivalent profile.
- Probe TUN, socket, BPF, DNS-provider, and partial-privilege behavior in N's
  selected launch environment. Keep backend access separate from R's sandbox;
  identify actual native requirements without adding missing launch authority.

Acceptance:

- Record supported kernel/profile combinations and explicit failure reasons.
  Ordinary Go execution works; forbidden host files, process creation, raw
  sockets, token/descriptor theft, and host administration from R fail.
- Record retained backend rights, including existing `CAP_SYS_ADMIN` when a
  native operation needs it. No experiment elevates the caller, grants those
  rights to R, or silently bypasses R's required restrictions.
- Results are reproducible. The production direct daemon is unchanged.

## 11. Experiment: Windows restrictions and native resource access

Status: implementation and Windows Server 2022/amd64 qualification complete.
Direct Winsock and Wintun data-handle candidates remain unapproved for
checkpoints 12 and 13. See the
[Windows experiment report](DAEMON_ISOLATION_WINDOWS_EXPERIMENT.md).

Work:

- Test restricted tokens, AppContainer, integrity levels, exact inherited
  handles, suspended launch, and a non-breakaway job with the actual Go binaries.
- Test private IPC access and storage ACLs under the proposed identities. Check
  caller-derived ordinary-user profiles separately from service profiles.
- Probe Wintun session/ring access, duplicated Winsock sockets, driver control,
  and supported process mitigations under those restrictions.

Acceptance:

- Record supported Windows/profile combinations. R cannot open host files,
  escape the job, obtain N's token, install drivers, or make unrestricted native
  network connections. Any approved data handle has a separately tested scope.
- Verify restrictions before child code runs, including package initialization.
  Do not claim a mitigation that the binary or OS does not enforce.
- No automatic UAC prompt or privileged service connection is needed for a
  caller-derived profile. Unsupported combinations fail clearly in the harness.

## 12. Experiment: data paths and five-tuple lookup cost

Status: complete. The harness and Docker/WinVM measurements are retained as
diagnostic baselines; no performance acceptance evidence is required. See the
[data-path experiment record](DAEMON_ISOLATION_DATA_PATH_EXPERIMENT.md).

Historical experiment scope:

- The harness compared direct, relay, shared-memory, direct-handle, TUN,
  Wintun, socket, matcher, address-family, packet-size, and logging variants.
- It recorded rate, loss, latency, CPU, scheduling, syscall, IPC, allocation,
  memory, queue, and control-response observations.
- These measurements are optional inputs to later optimization. They do not
  need to be repeated for isolation implementation or qualification.

Acceptance:

- Candidate results cover functional semantics: deadlines, cancellation,
  close, packet boundaries, UDP ancillary data, backpressure, and fairness.
- Lookup results retain native tuple/error/freshness behavior. A valid tuple
  does not fail because no broker observation or lease exists.
- Tests show no hidden native fallback.

## 13. Select and freeze data paths after a security review

Status: design selection and contract freeze complete. The maintainer requested
conservative choices for every resource on 2026-10-09 so implementation can
continue. Select broker-owned relays on Linux and Windows; exclude direct native
handles and shared memory from version 1. See the
[per-resource decision and evidence](DAEMON_ISOLATION_DATA_PATH_DECISION.md) and
[frozen data-channel contract](DAEMON_ISOLATION_DATA_CHANNEL_CONTRACT.md).
The explicit sequencing change moves the remaining functional and
hostile-runtime qualification to the implementation and release gates below.
Current performance is accepted and does not affect production readiness.

Work:

- Audit each direct handle for all authority it grants: socket options,
  reconnect/rebind, device ioctls, shared state, optional destination-policy
  bypass, and access after graph removal. Closing N's copy is not revocation.
- Audit shared rings for untrusted indices, sizes, metadata races, memory
  ownership, and bounded work. Test Windows transfer mechanisms rather than
  assuming arbitrary handles or Wintun sessions can be transferred safely.
- Record a decision per OS and resource type: TUN, TCP connection, listener,
  connected UDP, and unconnected UDP. Select relays when stronger policy or
  revocation needs them. Freeze only the approved channel contracts.

Acceptance:

- Each decision cites functional and hostile-client evidence.
  A faster candidate that grants undeclared host authority is rejected.
- Every selected path has an enforceable revocation/lifetime contract. If only
  process termination revokes a handle, document and test the required action.
- Freeze the conservative relay design now. Functional and security
  qualification remains required in checkpoints 30 through 32. Performance
  results do not block either OS profile.

## 14. Implement bounded control transports

Status: implemented in `bin/internal/isolation/transport`. See the
[wire limits, endpoint binding, and verification](DAEMON_ISOLATION_CONTROL_TRANSPORT.md).
The packaged Linux profile uses this transport. Windows production integration
remains incomplete.

Work:

- Implement version negotiation, typed errors, bounded decoding, request IDs,
  cancellation, and independent control/data/diagnostic channels.
- Start with architecture limits: 64 KiB control frames, maximum JSON depth 32,
  rejection of duplicate fields, and 128 in-flight requests. Use bounded config
  chunks with an 8 MiB total limit. Bound replies and event queues too.
- Implement private Linux socketpairs and qualified Windows connected pipes
  with exact endpoint/handle access. Validate ancillary handles and peer origin.
  Do not treat creation-time Linux peer credentials or a random pipe name alone
  as proof of the final child's identity.

Acceptance:

- Fuzz framing, decoding, versions, IDs, chunk assembly, and ancillary metadata.
  Oversized, duplicate, truncated, and unexpected messages are rejected safely.
- Unrelated local processes cannot connect. Unexpected descriptors/handles are
  closed, and malformed clients cannot cause unbounded work or memory use.
- Peer death and cancellation terminate pending calls. Saturated data or logs
  cannot prevent bounded control progress. Direct mode does not link its call
  path through these transports.

## 15. Implement network resource ownership and TUN control

Status: implemented in `bin/internal/isolation/network`. See the
[ownership, native TUN control, and verification notes](DAEMON_ISOLATION_NETWORK_CONTROL.md).
The packaged Linux profile uses this broker component and its packet relays.

Work:

- Add the N registry with session/type/generation checks and owned-resource
  cleanup. Handle create failures with partial native effects explicitly.
- Implement capability discovery, TUN validation/create/update/close/events,
  default-TUN preflight and serialized replacement, and operation receipts.
- Track native identity, not only names. Add graph epochs for stale work while
  preserving unchanged resources across equivalent graph updates.

Acceptance:

- A hostile client cannot alter foreign devices or reuse a stale/wrong-type ID.
  Name collisions do not cause adoption or deletion of unrelated interfaces.
- TUN methods match the direct backend, including warnings and unavailable
  capabilities. Failed or ambiguous operations return a recovery state when
  necessary instead of a false success.
- Concurrent replacement, cancellation, broker restart, and stale completions
  leave a known result or a reported recovery requirement, not an orphan hidden
  from accounting.

## 16. Implement socket control and the selected data channels

Status: implemented and qualified for packaged Linux amd64 in
`bin/internal/isolation/network`, `relay`, and `runtime`. See the
[socket and data relay implementation and evidence](DAEMON_ISOLATION_SOCKET_DATA.md).
Windows production integration remains incomplete.

The component gate includes a deterministic saturated-resource FIFO test. It
proves that later work from a hot resource cannot overtake a queued peer.

Work:

- Add outbound networks, LocalNet, TCP/UDP dial/listen/accept, resolver access,
  interface information, socket purposes, and the required optional methods.
  Derive marks and native options in N; do not accept arbitrary mark values.
- Implement the approved TUN and socket data adapters from section 13. Keep
  proxy `IsNative()` false and proxied TUN `File()` nil. Expose only separately
  approved handles, without a blanket native-bypass promise.
- Preserve platform LocalNet differences and userspace-stack listener semantics.

Acceptance:

- The section 3 inventory has a passing test for every supported method, with
  full IPv4/IPv6, wildcard/ephemeral/low-port, UDP metadata, multicast where
  supported, half-close, deadlines, close, and cancellation coverage.
- Packet and stream behavior matches direct mode. Unsupported operations return
  explicit errors and never create native resources in R as fallback.
- Control calls and shutdown still progress without deadlock under saturated
  traffic and blocked readers. No latency or throughput threshold applies.

## 17. Implement host routes, firewall, and DNS control

Status: component implementation is complete in
`bin/internal/isolation/network`. Linux direct-DNS and Windows managed-DNS
native component checks pass.
See the [host-control implementation and verification notes](DAEMON_ISOLATION_HOST_CONTROL.md).
Production isolated startup remains disabled. Checkpoint 24 now qualifies the
Linux provider and restricted-role profile; complete daemon qualification
remains a later gate.

Work:

- Add typed route, firewall, strict-mode, and host DNS changes with ownership
  records, apply results, and safe cleanup. Keep application DNS nodes in R;
  backend DNS parsing and forwarding may stay in N.
- Reuse the native Linux providers for resolved, NetworkManager, resolvconf,
  and direct-file DNS in N. Extra helpers are optional. Do not expose arbitrary
  D-Bus calls, file writes, or executable selection to R.
- Preserve Windows WFP, split-driver, managed-DNS, underlay, and global-feature
  constraints. Do not expose driver installation or raw driver requests to R.

Acceptance:

- Default routes, DNS bypass, underlay changes, kill-switch behavior, and all
  supported rule operations match the backend under equivalent credentials.
- Missing rights/providers produce precise feature errors. Optional socket
  marking failures retain their valid non-default-TUN behavior.
- Cleanup does not overwrite foreign route/DNS/firewall changes. Crash tests
  distinguish strict-mode guarantees from isolation; no unsupported kernel
  persistence guarantee is claimed.

## 18. Implement five-tuple matchers

Component implementation and evidence:
[Five-tuple matchers](DAEMON_ISOLATION_MATCHERS.md). Rule updates use immutable
matcher replacement through the frozen create/close API. Hostile-runtime
qualification remains a gate in section 30.
The outgoing typed control path now uses one JSON encoding instead of an
encode/decode/encode cycle. Short Linux component runs improved, but the
sequential lookup rate remains below the direct path. That result is accepted
and retained only as an optimization baseline.

Work:

- Add matcher creation, rule updates, bounded read-only tuple matching, and close
  over the control API. Return match results and sanitized backend errors, not
  raw process records or a general PID inspection API.
- Preserve `MatchConn` endpoint direction, proxy original endpoints, and backend
  caches/process-identity checks. Do not add observation tokens or tuple leases.
- Add batching/coalescing only if section 12 shows a need and freshness remains
  equivalent. Treat any new cache as a separate reviewed optimization.

Acceptance:

- Compare with native results for queued TUN traffic, IPv4/IPv6, UDP wildcard
  and shared binds, missing/ambiguous owners, short connections, PID/port reuse,
  exit/exec, rule replacement, cancellation, and overload.
- Valid lookups succeed without a previously observed packet. Resource limits
  bound hostile lookup traffic without changing the normal authorization model.
- Record matcher latency and packet-path impact when useful for later
  optimization. No numeric result blocks acceptance.

## 19. Implement the fixed-target config writer

Status: complete for the fixed-target writer component on Linux and Windows.
The Windows ordinary-user profile passed
`winvm:m19::fixed-target-config-writer` without skips on 2026-10-11, including
hard-link, junction, post-open parent-redirection, receipt, failure, and fuzz
checks. Production isolated startup remains disabled.

See the [config-writer implementation and verification notes](DAEMON_ISOLATION_CONFIG_WRITER.md).

Work:

- Add `ReadCurrent`, `Replace(expectedRevision, operationID, bytes)`, and
  `GetResult`. Keep bytes opaque; C does not parse HuJSON or construct graphs.
- Bind the target and metadata at startup. Implement atomic replacement,
  required durability, revision conflicts, and retained operation receipts.
- Implement Linux trusted-parent/descriptor-relative path handling and Windows
  handle/ACL-based target handling. Reject unsafe links, aliases, reparse paths,
  or layouts that the selected profile cannot contain.

Acceptance:

- Malicious path, symlink, hard-link, junction/reparse, alternate-stream, and
  concurrent replacement tests cannot redirect a write outside the fixed target.
- Inject disk-full, crash, and lost-reply failures around each commit boundary.
  `GetResult` resolves retries without a duplicate or unconditional stale write.
- C has no network, execution, protected-config, archive, or database access.
  Read-only operation starts no C; an ordinary runtime-owned target can retain
  local persistence with the same revision contract.

## 20. Implement restricted command and callback helpers

Status: component implementation is complete in
`bin/internal/isolation/commandhelper`. Linux component tests and fuzzing pass.
The Windows tests pass in the ordinary-user profile through
`winvm:m20::restricted-command-helper`. Production isolated startup remains
disabled.

See the [restricted command-helper implementation and verification notes](DAEMON_ISOLATION_COMMAND_HELPER.md).

Work:

- Implement known command IDs, bounded slots, fixed executable/environment/cwd,
  and protected execution profiles. Enforce `AllowNodes` inside H.
- Use a distinct N-to-H callback channel. Derive callback slots from N's actual
  committed event, not an event name or node identity claimed by R.
- Keep node-command H restricted. Reject node-command shell or interpreter
  profiles that cannot meet those restrictions. Backend-owned DNS utilities
  and BPF operations may run in N; they are not node-command execution paths.
  R cannot select their programs or supply scripts. Preserve legacy configured
  commands in direct mode.

Acceptance:

- R cannot forge a callback, bypass `AllowNodes`, select arbitrary argv/paths,
  change executable search rules, or gain H's launch privileges.
- Test argument injection, environment injection, executable replacement, time
  limits, output limits, cancellation, and helper death.
- Ambiguous command completion is not automatically replayed. External side
  effects are not reported as rolled back. Unsupported isolated profiles fail
  with a clear instruction to change the profile or use explicit direct mode.

## 21. Implement cross-process logging

Status: component implementation is complete in
`bin/internal/isolation/diagnostics`. Portable component tests and fuzzing pass.
The Windows ordinary-user package case is
`winvm:m21::cross-process-logging`. Production isolated startup remains
disabled.

See the [cross-process logging implementation and verification notes](DAEMON_ISOLATION_LOGGING.md).

Work:

- Keep node/runtime logs and their cache in R. Add separate bounded diagnostic
  streams from N/C/H for admin display and an independent operator output path.
- Assign process origin from the trusted channel. Include instance/session,
  sequence, and applicable operation/resource/generation fields. Do not imply
  global order from child timestamps.
- Sanitize control characters, multiline records, and panic output. Redact
  secrets, config bodies, command slots, packet data, and raw process metadata.
- Add priority limits, visible drop counts, no-forwarding-loop rules, and bounded
  flush. Disable unused levels before formatting. Keep recovery journals
  separate from best-effort logs.

Acceptance:

- A stopped R cannot suppress N's operator diagnostics or change protected audit
  settings. A blocked sink/subscriber or debug flood does not block control or
  traffic and cannot grow memory without bound.
- Forged origins, oversized records, terminal escapes, child panic, and remote
  fatal records are safe. Fatal records do not terminate their collector.
- Startup failures remain visible before sandbox setup; shutdown flushing is
  bounded. Direct mode still creates no logging IPC or collector process.

## 22. Implement the privileged admin listener gate

Status: component implementation is complete in
`bin/internal/isolation/admingate`. Portable component tests and fuzzing pass.
The Windows ordinary-user package case is
`winvm:m22::privileged-admin-listener-gate`. Production isolated startup
remains disabled.

See the [privileged admin listener gate implementation and verification notes](DAEMON_ISOLATION_ADMIN_GATE.md).

Work:

- Keep HTTP, WebSocket, stream parsing, authorization above the transport,
  origin checks, and panel handling in R. Give N only fixed listener creation,
  required peer checks, and authenticated stream delivery.
- Preserve Linux Unix-peer/native TCP-owner checks and Windows public-pipe
  access behavior. Keep Windows impersonation thread-scoped with guaranteed
  revert and no unrelated work under the client identity.

Acceptance:

- Existing CLI/panel/admin authentication tests pass. Untrusted clients cannot
  reach a private broker endpoint or obtain arbitrary peer/process inspection.
- A slow or malicious admin connection cannot block privileged control progress.
  N does not import the admin application or parse its protocol bodies.
- Direct mode uses its local listener and handlers without an internal relay.

## 23. Implement trusted bootstrap and storage preparation

Status: component implementation is complete in
`bin/internal/isolation/bootstrap` and `bin/internal/isolation/roleentry`.
Portable component tests and fuzzing pass. The Windows ordinary-user package
case is `winvm:m23::trusted-bootstrap-storage`. Production isolated startup and
platform restriction profiles remain disabled.

See the [trusted bootstrap and storage preparation implementation and verification notes](DAEMON_ISOLATION_BOOTSTRAP_STORAGE.md).

Work:

- Build minimal role entry points so privileged Go package initialization cannot
  run runtime/node code. Remove implicit environment-file loading from trusted
  startup. Fix executable, DLL/library, environment, and working-directory inputs.
- Read protected config once. Resolve exact protected secrets/TLS inputs and
  send R only required immutable data. Handle the bounded protected stdin case
  explicitly. `--unsafe-config` must not waive protected-input trust checks.
- Open startup snapshot inputs as opaque data for R. Keep regular config,
  HuJSON/ZIP parsing, bbolt, and mutable application stores in R.
- Define private runtime storage, separate broker journals, and fixed public
  certificate refresh inputs. Add stopped-daemon migration with backup and
  rollback; reject unsafe layouts instead of recursively following/chowning links.

Acceptance:

- A malicious regular config or snapshot, including one persisted by a compromised
  R, cannot change protected authority on the next launch.
- Import checks keep node/panel/archive/database/general config parsing out of
  privileged role binaries. Runtime-writable parents cannot replace protected
  files, binaries, or journals.
- Migration is tested with legacy layouts, interrupted work, rollback, and path
  attacks. Direct mode needs no migration. Unsupported layouts have actionable
  errors and no automatic privilege expansion.

## 24. Implement the Linux production profiles

Status: complete for the Linux amd64 production profile components and their
component-level platform tests. Full combined feature acceptance remains in
checkpoint 30. R/C/H restrictions are implemented in
`bin/native/linux-role` and `bin/internal/isolation/linuxprofile`.
`linuxprofile.PrepareBackend` prepares trusted bootstrap as N in place without
changing its launch credentials or host namespaces. `ProvisionCgroups` creates
separate bounded role cgroups below a delegated instance and gives N a higher
CPU weight. Production isolation stays disabled until the later composition,
supervision, deployment, and default-mode checkpoints. The obsolete restricted-N
launcher path is removed; `network` now identifies only its cgroup.

The dedicated trusted-backend Docker case now compares direct and prepared
native backend behavior for direct-provider DNS, default TUN, routing, cleanup,
and userspace process-owner matching. A separate Docker matrix qualifies all
sysnet-linux v0.10.1 DNS modes, including no-upstream fallbacks, split DNS, and
dynamic resolved interface selection. The released p-mark v0.5.0 networkless
guest suite qualifies tracepoint and cgroup programs, authoritative kernel
rules, userspace reconciliation of existing sockets, and process-map churn.
sysnet-linux v0.10.1 contains the corresponding consumer E2E checks for
automatic construction, routing, user marks, repeated fork/exec/exit socket
probes, and teardown, so no downstream source patch is required.
The provider matrix and guest run upstream fixtures, not PrepareBackend or a
complete isolated daemon. A combined case runs the real broker against restricted R
for TCP/UDP, owner matching, regular/default TUN, and peer-loss cleanup. It
found and removed a redundant channel socket-option write without weakening
R's syscall rules. Bootstrap identity selection remains a deployment input
for later daemon assembly; it does not restrict N or
change this profile boundary.
Runtime OutDNS now forwards bounded DNS packets to the existing native provider,
without requiring an upstream snapshot. The restricted-runtime Docker case
checks this path before and during a default TUN. Application-resolver attachment
now has a bounded correlated relay. The native composition test attaches it to
the default TUN, sends a real DNS query through N's managed listener, executes
the application resolver in R, and validates the reply and cleanup.
Native TUN creation now writes a broker-private pending recovery record before
the host mutation, promotes it only after a stable platform identity is known,
and removes it only after native cleanup. The restricted C role performs a
bounded bootstrap/readiness exchange and runs the fixed-target writer over its
control-only channel. The Linux Docker composition executes that real role.

See the [Linux role profile implementation and qualification notes](DAEMON_ISOLATION_LINUX_PROFILES.md).

Work:

- Apply the qualified R profile from section 10 before untrusted initialization:
  namespaces, minimal mounts, runtime identity, capability/securebits drop,
  `no_new_privs`, thread-wide syscall/filesystem restrictions, and resource limits.
- Keep N in the host context with its available launch identity and rights
  needed by the native backend. Do not force N through R's private PID/mount
  view or network-only capability mask. Permit native DNS providers, installed
  backend utilities, BPF loading, process inspection, and existing-socket marks
  in N. No mandatory DNS/p-mark refactor or helper split is required.
- Restrict C and node-command H independently. Keep node code, application
  callbacks, config/archive parsing, and runtime-selected programs out of N.
  Do not use unsafe Go work after fork in a multithreaded process.
- Qualify full backend behavior and supported rootless profiles. Drop only
  authority not needed after setup; never add authority missing at launch.
  Optional N hardening must not remove features available in direct mode.

Acceptance:

- Adversarial code in every R thread cannot access host proc/sys/run services,
  create host-native sockets outside approved handles, execute children, mount,
  trace other roles, or obtain broker descriptors.
- All implemented features work with R fully restricted and N in its final
  launch environment. Measure retained credentials/capabilities after setup.
  Compare DNS provider selection, eBPF kernel/userspace rules, process lifecycle,
  existing-socket marks, owner matching, routing, TUN and ordinary sockets with
  direct mode under equal launch authority. Disabled features are not parity.
- Hostile R cannot use N's retained authority through generic path, executable,
  BPF, D-Bus, process-handle, or raw native-operation requests. Session ownership,
  validation, bounds, callback provenance, cleanup, and private IPC remain
  mandatory. Backend-internal DNS parsing is allowed; node code in N is not.
- Missing required restrictions stop isolated startup with no downgrade. An
  unsupported rootless profile offers explicit `--no-isolation`, without
  elevation; direct mode remains unaffected.

## 25. Implement the Windows production profiles

Status: complete for the Windows Server 2022/amd64 production profile
components. `bin/internal/isolation/windowsprofile` implements restricted
R/C/H roles, trusted caller/service N, and persistent AppContainer deployment
provisioning. Both launch identities pass final-token native backend tests in
the locked Windows VM. Production isolated startup remains disabled until the
later composition, security qualification, and rollout checkpoints pass.
The common C entry point and durable native-resource journal are used by this
profile. Native network cleanup must leave no journal record. Restart
reconciliation inspects the recorded LUID/GUID and never removes an adapter
from a generic or name-only claim.

See the [Windows role profile implementation and qualification notes](DAEMON_ISOLATION_WINDOWS_PROFILES.md).

Work:

- Apply the qualified restricted-token/AppContainer profile, integrity/ACL
  layout, exact handle list, mitigations, and job to R before node initialization.
  Keep C and node-command H in their separate restricted profiles.
- Let trusted N retain the caller or service token needed by sysnet-windows.
  Do not require R's AppContainer, maximum-privilege removal, or ordinary-user
  identity for service N. Backend DNS, process inspection, WFP and driver control
  may remain in N. No automatic elevation or service attachment is permitted.
- Keep protected executable/DLL locations, private IPC ACLs, and explicit
  driver-provisioning rules. Never give R token, process, or driver-control
  handles. Do not require extra DNS/driver helpers solely to restrict N.
- Apply separate C/H identities and target ACLs. Preserve the qualified public
  admin pipe separately from private broker channels.

Acceptance:

- Adversarial R cannot escape its job, acquire broker tokens/handles, access
  protected files, load an arbitrary DLL/driver, or gain general native network
  access. Test inherited handles and all worker threads.
- TUN, Winsock, DNS, owner matching, and backend-specific supported features pass
  under the final service and ordinary-user profiles where qualified.
- Unsupported OS/profile/storage combinations fail clearly. No UAC escalation,
  system broker attachment, or unrestricted fallback occurs automatically.

## 26. Implement supervision and host recovery

Status: the bounded supervisor, durable identity-driven recovery journal,
Linux/Windows native identity inspectors, and broker integration are
implemented in `bin/internal/isolation/supervision` and
`bin/internal/isolation/network`. Production isolated startup remains disabled.
Complete-composition qualification remains in checkpoints 30 through 32.

See the [supervision and host recovery contract](DAEMON_ISOLATION_SUPERVISION.md).

Work:

- Make bootstrap become N after setup, with no general later process launcher.
  Define readiness, startup failure, bounded shutdown, and full service restart.
- On R loss, revoke resources and clean up owned host effects. On N loss, fail
  runtime adapters and stop safely; never fall back to native host access.
- Persist the minimum trusted ownership/recovery state outside R's storage.
  Recover uncertain native operations using identity checks, not name prefixes.
  Treat C/H failures according to their narrower affected functions.

Acceptance:

- Kill each role before/after readiness and during network operations. Test
  hangs, broken channels, service restart, external host changes, and partial
  cleanup. All waits and retained queues are bounded.
- R's death cannot leave usable direct handles beyond their approved lifetime.
  Foreign resources are not removed. Uncertain effects remain visible as a
  recovery requirement, not a successful cleanup claim.
- Persistence failure does not falsely report success or needlessly destroy
  unrelated active traffic. Strict-mode outcomes match the protected contract
  and actual platform capability.

## 27. Integrate cross-process config transactions

Status: implemented as the runtime-side isolated config persistence adapter in
`bin/internal/isolation/configpersistence` and the restricted C role in
`bin/cmd/almagest-config-writer-role`. C accepts one trusted target name below
its held directory and uses a control-only authenticated channel. The production
isolated composition remains disabled until checkpoint 32.

See the [fixed-target isolation config writer integration notes](DAEMON_ISOLATION_CONFIG_WRITER.md#checkpoint-27-runtime-integration).

Work:

- Connect the section 4 coordinator to C. Keep one runtime mutation gate,
  selective reconciliation, revision checks, and canonical text generation.
- Confirm persistence before publishing the committed revision. Use operation
  result queries after lost replies; do not overwrite a newer file with an old
  rollback image. Bound rollback and report failure explicitly.

Acceptance:

- Run existing text-config/autosave tests against direct and isolated adapters.
  Add concurrent admin edits, native partial failures, C death, conflicts, and
  lost replies at every transaction boundary.
- Success identifies the active graph/config revision; failure identifies
  rollback or recovery state. Do not claim an atomic transaction spanning the
  filesystem and host network.
- Equivalent config updates do not rebuild unchanged nodes or revoke their
  resources. All direct-mode behavior remains available.

## 28. Integrate snapshot creation and startup restore

Status: implemented in the runtime-owned daemon snapshot coordinator. Snapshot
creation now quiesces live bbolt writers, startup restore remains before live
database and graph construction, and archive validation has explicit structural
and work bounds. Production isolated startup remains disabled until checkpoint
32.

See the [isolated snapshot creation and startup restore notes](DAEMON_ISOLATION_SNAPSHOTS.md).

Work:

- Coordinate actual storage writers for consistent creation; an admin mutex
  alone is insufficient. Preserve bbolt's consistent-copy mechanism.
- Apply archive limits, including current 32 MiB archive, 256 MiB expanded,
  64 KiB manifest, 8 MiB config, and 256-byte name limits; add bounded entry
  count, path validation, and work limits.
- Parse startup snapshots in R before live database/graph use. Reapply immutable
  bootstrap data. Exclude protected policy, broker state/IDs, recovery journals,
  and the snapshots subtree from restored application authority.

Acceptance:

- Create/export/import/startup restore pass in both modes with concurrent
  writers, malformed archives, duplicate/path-escape entries, expansion attacks,
  disk-full, and cancellation.
- N/C never parse archive contents. A snapshot cannot change launch authority
  or restore a usable old broker resource ID.
- Valid existing snapshots remain compatible; any new rejection is tied to a
  documented safety bound rather than removal of a conceptual feature.

## 29. Integrate live restore, rollback, and crash recovery

Status: implemented in the runtime-owned live-restore coordinator. Restore now
uses graph-epoch drains, a durable three-stage application journal, startup
crash recovery, and an explicit recovery-required mutation gate. Production
isolated startup remains disabled until checkpoint 32.

See the [snapshot and live-restore isolation notes](DAEMON_ISOLATION_SNAPSHOTS.md#live-restore-transaction).

Work:

- Extend the local coordinator with graph-epoch invalidation and N drain before
  storage replacement. Rebuild with fresh authority references after commit or
  rollback; reject late work from the old graph.
- Add prepared/data-applied/committed restore journal states and a recovery copy.
  Keep this application journal unable to authorize host effects.
- Preserve one authoritative workspace/worker view and nonblocking notification
  delivery. Preserve successful restore plus `ConfigWriteError` when only the
  final regular-config write fails.

Acceptance:

- Inject process death, lost replies, failed native teardown/rebuild, disk-full,
  and concurrent requests at every restore/rollback stage in both modes.
- Recovery produces a consistent old/new application state or stops in an
  explicit recovery-required state. Failed rollback is never reported as success.
- No stale callback/resource survives the epoch change; blocked subscribers do
  not block restore. Existing restore and notification tests retain their intent.

## 30. Qualify feature parity and the hostile-runtime boundary

Status: the fail-closed qualification manifest, development-only role entry
points, and restricted daemon composition are implemented. The Linux profile
now starts the real daemon engine in R with an authenticated `sysnet.System`
adapter and an N-owned admin listener. The retained daemon run executes a
command node through H, updates config, and performs a live snapshot restore.
Native C and H tests also use their final restrictions and fixed authority.
Loss of N stops R, C, and H. The Linux real-daemon case now uses separate
restricted C and H executables and distinct R/C/H identities. The Linux profile
components from checkpoint 24 and Windows profile components from checkpoint
25 are qualified.
The integration audit added the missing typed rule validation/completion
operations and native parity tests, and fixed stale read completion after
cancellation or revocation. Typed IP/subnet allocation now reaches the native
allocator with graph-owned reserve counts, bounded cleanup, and native Linux
and Windows parity tests. The runtime network client now implements the complete
`sysnet.System` surface, including stable TUN identities, application DNS, and
native capability fidelity. The release evidence gate remains fail closed:
component or development-composition results cannot be relabeled as a complete
platform record.
Production isolated startup remains disabled.

The qualification package now provides a closed-manifest concurrent recorder
for actual producer observations. It rejects unknown and duplicate IDs, retains
failures, and emits manifest-ordered snapshots for validation. Platform runners
do not yet supply a complete record, so this closes only the evidence-assembly
code gap.

The 2026-10-11 native Windows audit passed all 22 ordinary-user cases, all 23
privileged cases, and the isolated two-guest packet-flow gate with 116 validated
observations. The corrected resource runner also completed its 30-minute soak,
crash recovery, daemon graph/restart, and fault-injection cases. Handle growth
was 13 after warm-up, and an earlier run measured 19. The maintainer accepts
the current backend result. The runner now records these deltas without using
them as qualification limits. See the
[qualification audit](DAEMON_ISOLATION_QUALIFICATION.md#windows-matrix-and-resource-audit-2026-10-11).

See the [complete-profile qualification gate](DAEMON_ISOLATION_QUALIFICATION.md).

Work:

- Assemble complete isolated profiles behind the explicit development entry
  point. Run the full feature matrix against direct mode under equal launch
  credentials and protected settings, with no added restrictive policy.
- Then test optional restrictions separately. Exercise default TUN, all supported
  listeners, outbound traffic, DNS, rules, matching, commands, API, snapshots,
  logging, and recovery. Preserve experimental exclusion in isolation.
- Run deliberate arbitrary-code-in-R tests, fuzz campaigns, sustained overload,
  and native Windows resource/flow qualification as applicable.

Acceptance:

- Every inventory item has a passing result or a documented native-backend
  limitation shared by both modes. An isolated-only loss of a required feature
  blocks that profile's release; do not relabel it as native unavailability.
- Attempts to forge identity, escape paths, alter policy, obtain host handles,
  access foreign resources, execute commands, or exhaust brokers remain inside
  the declared boundary. All-thread and inherited-handle tests pass.
- No required Linux/Windows test is skipped in the release evidence. Unqualified
  profiles remain unavailable rather than receiving a weaker isolation claim.

## 31. Record the accepted complete-daemon performance baseline

Status: complete by maintainer direction on 2026-10-11. Current performance is
accepted. This checkpoint cannot block isolation completion, release, or the
default switch.

Work:

- Keep existing measurements as diagnostic baselines.
- Add new measurements only when they help a later optimization.
- Keep every optimization subject to the same functional, authority, freshness,
  and cleanup requirements.

Acceptance:

- No numeric performance, resource-growth, duration, hardware, or comparison
  requirement applies.
- Direct mode still has no daemon process split or internal isolation IPC. This
  is an execution-mode invariant, not a performance target.
- Functional or security failures still block release; slow execution does not.

## 32. Package and enable isolation by default

Status: complete for the packaged Linux amd64 profile. `almagestd` selects
isolation by default and replaces itself with the minimal trusted N binary
before regular config is parsed. `--no-isolation` keeps the supported direct
single-process composition. Unsupported targets and incomplete installations
fail with explicit `--no-isolation` guidance; there is no fallback.

The production entry loads protected authority, materializes fixed R inputs,
provisions separate role cgroups, starts restricted R and optional C/H, binds
trusted callbacks, recovers the native journal, and publishes N-owned admin
listeners only after restriction and application readiness. It reports mode,
protocol, role health, restrictions, capabilities, and recovery state through
the existing hello response. Storage tooling covers fresh installation,
idempotent preparation, migration, recovery, and rollback. The systemd,
sysusers, tmpfiles, build, deployment, and operator-recovery files are in
`packaging/linux`.

Windows Server 2022 amd64 keeps its qualified component profiles and native
assembly boundary, including explicit lane maps and fixed runtime input
materialization. It is not a released isolation profile because complete Windows
daemon composition, H semantics, packaging, and release qualification are not
complete. Windows therefore uses the supported direct composition by default
while isolation support is in progress. Explicit `--no-isolation` selects the
same mode and has no additional effect.

Scope update, 2026-10-11: quota implementation is excluded from this plan and
remaining release work at the maintainer's request. In particular, persistent
storage quotas are not an acceptance or qualification gate. Existing bounded
protocol, process, and resource safety limits remain in place.

Work:

- Package role binaries, trusted paths, service definitions, identities,
  profiles, storage migration, and operator recovery instructions. Update config
  schemas, CLI help, deployment examples, and architecture status together.
- Enable default isolation only for qualified profiles. Keep direct mode as the
  documented Windows default until its profile is complete. On other unsupported
  systems, fail with actionable `--no-isolation` guidance. Preserve caller
  authority and feature discovery.
- Expose mode, enforced restrictions, role health, protocol version, capability
  availability, and recovery state without protected secrets.

Acceptance:

- Fresh install, upgrade, migration rollback, service restart, and ordinary-user
  launches pass on the supported matrix. Untrusted runtime storage cannot replace
  role binaries or protected launch inputs.
- Both default isolated mode and explicit direct mode pass the full gates.
  `--experimental` requires direct mode. It requires explicit `--no-isolation`
  where isolation is the default; experimental classes are absent from isolated
  config/snapshot validation.
- Required-profile failures stop before untrusted execution or leave only
  explicitly reported, safely handled partial setup. No automatic downgrade or
  privilege acquisition occurs.
- Operators can identify the active guarantees and recover from failure. Future
  CI continues to test both compositions, including direct-mode overhead and
  isolated hostile-client tests; direct mode is not a frozen legacy fork.

## Completion evidence

Keep the following with the implementation review, not only in a developer's
local environment:

- The feature/method inventory and direct-mode compatibility results.
- Versioned control/data contracts and per-OS/resource data-path decisions.
- Supported platform profiles, enforced privileges, and denial-test results.
- Fault-injection, fuzz, integration/E2E, and recovery results, with actual suite
  execution and explicit environment requirements.
- Deployment, migration, limitations, and operator recovery documentation.

The work is complete only when these records support the final acceptance
conditions. A passing direct daemon is required throughout, but it is not proof
that an unfinished isolated composition is ready for release.
