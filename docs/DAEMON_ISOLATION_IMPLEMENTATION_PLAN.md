# Daemon isolation implementation plan

Status: proposed implementation plan. This document does not implement isolation.
It follows the [isolation architecture](DAEMON_ISOLATION_ARCHITECTURE.md).
Its sequence takes precedence over the outline in architecture section 10.3:
first establish the complete direct-mode composition, then run the isolation
experiments, and only then select the isolated data paths.

Role names follow the architecture: R is the runtime, N is the network broker,
C is the optional config writer, and H is an optional restricted helper.

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
- Record the change, commands run, environment, failures, performance evidence
  where applicable, and remaining limitations. An unavailable test host leaves
  the relevant acceptance condition open; it does not justify a silent skip.

These rules apply after every section below. Until section 32, the working
production checkpoint is the complete direct daemon; isolated work is additive.

## 1. Add the explicit direct execution mode

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

Work:

- Extract native system construction and host control from daemon orchestration.
  Make an inventory of every used sysnet, Gonnect, TUN, resolver, and matcher
  method, including optional methods and native fast-path consumers.
- Separate DNS packet processing from host DNS configuration. Put packet parsing
  and forwarding in runtime-owned code; keep host changes in the local network
  service. Make necessary dependency changes as separately tested changes.
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

Work:

- Add import-boundary checks for the extracted modules. Keep service logic
  independent of process launch and wire protocol packages.
- Run the full direct-mode test matrix against the baseline from section 1.
- Instrument child creation, private listeners, transport construction, native
  path use, and allocations. Compare direct-mode performance with the original
  implementation before accepting the refactor.

Acceptance:

- All original conceptual daemon features pass, including CLI/panel, integration,
  privileged E2E, and Windows VM tests. Test changes are limited to justified
  mechanical moves and launch flags, not weaker expected behavior.
- Startup, graph changes, matching, config writes, restore, and shutdown create
  no internal IPC or daemon role subprocess. The mode needs no sandbox account,
  AppContainer, namespace, or privileged storage migration.
- Native fast paths remain active. Added IPC/serialization allocations are zero.
  Any other measured regression is explained and meets an explicitly recorded
  direct-mode budget; do not hide it by reducing the baseline's functionality.

## 9. Define policy, contracts, and performance budgets

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
- Define numeric performance budgets and test environments before selecting a
  data path. Cover throughput, latency tails, CPU, memory, and direct-mode
  regression. Record who approves the budgets; this plan does not assume values.

Acceptance:

- A contract review covers every inventory entry and default legitimate sysnet
  operation. Missing capability is not replaced by a new mandatory allowlist.
- Fake services test forged IDs, foreign ownership, stale generations, conflicts,
  cancellation, and lost replies without elevated privileges.
- Control contracts are versioned. Data-channel candidates remain open until
  section 13. Budgets, workloads, and comparison rules are recorded and approved.

## 10. Experiment: Linux restrictions and native resource access

Work:

- Build a development-only launcher/harness with actual UID and capability
  drops, namespaces, `no_new_privs`, syscall restrictions, and the proposed
  filesystem view. Test all Go threads and runtime operations.
- Check Landlock ABI and thread-wide enforcement options; do not assume locking
  one Go thread restricts the others. Test any allowed equivalent profile.
- Probe TUN, socket, BPF, DNS-provider, and partial-privilege behavior after the
  restrictions. Identify helper requirements without broadening privileges.

Acceptance:

- Record supported kernel/profile combinations and explicit failure reasons.
  Ordinary Go execution works; forbidden host files, process creation, raw
  sockets, token/descriptor theft, and host administration from R fail.
- Retained rights match the selected backend features. No experiment silently
  adds `CAP_SYS_ADMIN`, elevation, or unrestricted execution to make it pass.
- Results are reproducible. The production direct daemon is unchanged.

## 11. Experiment: Windows restrictions and native resource access

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

Work:

- Compare the original direct baseline, refactored `--no-isolation`, batched and
  unbatched broker relays, shared-memory rings, and eligible direct handles.
  Use the real restrictions from sections 10 and 11, not unrestricted children.
- Test real Linux TUN and Windows Wintun, TCP connections/listeners, connected
  and unconnected UDP, IPv4/IPv6, both directions, and supported queue counts.
- Use packet sizes 64, 256, 512, 1200, 1500, and the supported maximum. Include
  single/many flows, interactive low-rate traffic, saturated small packets,
  WireGuard, and matchers enabled/disabled. Repeat warm and cold runs.
- Measure goodput, packets per second, loss/retransmits, latency p50/p95/p99,
  jitter, per-role CPU, context switches, wakeups, syscalls, IPC messages,
  copies, allocations, memory, queue depth, and control responsiveness.
- Measure one bounded matcher RPC, overload, and optional bounded batching or
  coalescing. Do not require observation leases or introduce a client cache by
  default. Keep backend routing enforcement off the per-packet RPC path.
- Include normal logging, disabled debug logs, and overloaded logging.

Acceptance:

- Publish commands, machines/VM settings, source versions, raw results, repeated
  run variation, and a comparison against the budgets from section 9.
- Candidate results cover semantics as well as speed: deadlines, cancellation,
  close, packet boundaries, UDP ancillary data, backpressure, and fairness.
- Lookup results retain native tuple/error/freshness behavior. A valid tuple
  does not fail because no broker observation or lease exists.
- Tests show no hidden native fallback, and neither direct baseline is made
  slower or less capable to improve the isolated comparison.

## 13. Select and freeze data paths after a security review

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

- Each decision cites functional, performance, and hostile-client evidence.
  A faster candidate that grants undeclared host authority is rejected.
- Every selected path has an enforceable revocation/lifetime contract. If only
  process termination revokes a handle, document and test the required action.
- All selected paths meet the agreed budgets. If no safe path meets them, leave
  the profile incomplete and revise the design or obtain explicit budget
  approval; do not weaken isolation or quietly reset the comparison baseline.

## 14. Implement bounded control transports

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
- Selected paths meet the experiment budgets in component tests. Control calls
  and shutdown still progress under saturated traffic and blocked readers.

## 17. Implement host routes, firewall, and DNS control

Work:

- Add typed route, firewall, strict-mode, and host DNS changes with ownership
  records, apply results, and safe cleanup. Keep DNS packet parsing in R.
- Implement Linux provider-specific paths for resolved, NetworkManager, fixed
  resolvconf execution, or a fixed DNS-file writer as needed. Do not provide
  arbitrary D-Bus calls, file writes, or executable selection.
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
- Record matcher latency and packet-path impact against section 9 budgets.

## 19. Implement the fixed-target config writer

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

Work:

- Implement known command IDs, bounded slots, fixed executable/environment/cwd,
  and protected execution profiles. Enforce `AllowNodes` inside H.
- Use a distinct N-to-H callback channel. Derive callback slots from N's actual
  committed event, not an event name or node identity claimed by R.
- Give DNS/BPF helpers only their selected fixed functions. Reject shell or
  interpreter profiles that cannot meet the initial isolated restrictions.
  Preserve legacy configured commands in direct mode.

Acceptance:

- R cannot forge a callback, bypass `AllowNodes`, select arbitrary argv/paths,
  change executable search rules, or gain H's launch privileges.
- Test argument injection, environment injection, executable replacement, time
  limits, output limits, cancellation, and helper death.
- Ambiguous command completion is not automatically replayed. External side
  effects are not reported as rolled back. Unsupported isolated profiles fail
  with a clear instruction to change the profile or use explicit direct mode.

## 21. Implement cross-process logging

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

Work:

- Apply the qualified profile from section 10 before untrusted initialization:
  namespaces, minimal mounts, runtime identity, capability/securebits drop,
  `no_new_privs`, thread-wide syscall/filesystem restrictions, and resource limits.
- Keep N in the host network context with only available rights needed by its
  selected features. Restrict C and H independently. Do not use unsafe Go work
  after fork in a multithreaded process.
- Qualify optional BPF loading, DNS helpers, and supported rootless profiles.
  Ensure setup-only authority is dropped when no longer needed.

Acceptance:

- Adversarial code in every R thread cannot access host proc/sys/run services,
  create host-native sockets outside approved handles, execute children, mount,
  trace other roles, or obtain broker descriptors.
- All implemented features work under final restrictions, not only before the
  sandbox is installed. Capabilities are measured after setup.
- Missing required restrictions stop isolated startup with no downgrade. An
  unsupported rootless profile offers explicit `--no-isolation`, without
  elevation; direct mode remains unaffected.

## 25. Implement the Windows production profiles

Work:

- Apply the qualified restricted-token/AppContainer profile, integrity/ACL
  layout, exact handle list, mitigations, and job before resuming each role.
- Limit N's token/service identity to required native operations. Keep protected
  executable/DLL locations and explicit driver-loading rules. Never give R the
  split-driver control handle.
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

## 31. Qualify complete-daemon performance

Work:

- Repeat section 12 with the complete daemon, final restrictions, real admin
  and node workloads, normal/overloaded logging, config operations, and restore.
- Compare with both retained direct baselines. Include long-running resource
  use, graph churn, contention, and control responsiveness under saturation.
- Investigate differences from the component harness. Keep any optimization a
  separately tested change with the same authority and freshness requirements.

Acceptance:

- Results meet the section 9 budgets for every qualified platform/resource path.
  Explain material differences from harness measurements and record raw data.
- Direct mode still has no daemon process split, internal IPC, serialization, or
  native-fast-path regression. Matcher overhead/freshness remain within contract.
- Failed budgets block the default switch. A new budget or path decision needs
  explicit review and new evidence, not an undocumented acceptance exception.

## 32. Package and enable isolation by default

Work:

- Package role binaries, trusted paths, service definitions, identities,
  profiles, storage migration, and operator recovery instructions. Update config
  schemas, CLI help, deployment examples, and architecture status together.
- Enable default isolation only for qualified profiles. On unsupported systems,
  fail with an actionable error and explicit `--no-isolation` guidance; never
  switch silently. Preserve caller authority and feature discovery.
- Expose mode, enforced restrictions, role health, protocol version, capability
  availability, and recovery state without protected secrets.

Acceptance:

- Fresh install, upgrade, migration rollback, service restart, and ordinary-user
  launches pass on the supported matrix. Untrusted runtime storage cannot replace
  role binaries or protected launch inputs.
- Both default isolated mode and explicit direct mode pass the full gates.
  `--experimental` requires explicit `--no-isolation`; experimental classes are
  absent from isolated config/snapshot validation.
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
- Approved budgets, reproducible experiment commands, raw measurements, and
  complete-daemon comparisons.
- Fault-injection, fuzz, integration/E2E, and recovery results, with actual suite
  execution and explicit environment requirements.
- Deployment, migration, limitations, and operator recovery documentation.

The work is complete only when these records support the final acceptance
conditions. A passing direct daemon is required throughout, but it is not proof
that an unfinished isolated composition is ready for release.
