# Runtime Node Lifecycle Pitfalls

This document summarizes recurring runtime lifecycle hazards in Almagest nodes.
It is meant for adding new nodes, reviewing node refactors, and designing
regression tests. The examples come from runtime bugs fixed, but
the rules are node-agnostic.

## General Problem Areas

Runtime nodes often publish graph-level state while also managing asynchronous
resources:

- network wrappers, TUN wrappers, listeners, sockets, devices, or stacks
- background receive/accept/read/write routines
- config propagation from adjacent nodes
- retry loops and delayed recovery work
- save/restore, copy/paste, and link attach/detach events

The graph does not provide a global barrier that proves every runtime worker has
opened listeners, attached binds, consumed final config, and replaced stale
wrappers. A node's own state machine must therefore distinguish between:

- a resource object being present
- the resource being the current one
- the resource being open and usable
- the resource having completed all async setup for the current graph generation
- the resource having failed and requiring replacement

Treat every node that keeps long-lived runtime resources across config apply,
link rewiring, retries, or background I/O as error-prone.

### Do Not Derive Readiness From Resource Presence

`currentNetwork != nil`, `currentTun != nil`, `listener != nil`, or
`runtime != nil` is not enough to publish `up`.

A node should report a healthy/usable state only after the exact async runtime
operation required by the current generation has completed successfully. If an
attachment is present but still being opened, configured, or rebound, report a
transitional state such as `starting` or `ready`.

Useful state usually includes:

- a current runtime generation
- a generation that has completed readiness
- an active attachment predicate that checks both presence and health
- an error or failed-resource flag that cannot be cleared by unrelated success

Invariant:

```text
A node must not report that it is fully up until the runtime attachment that
makes it usable is active for the current runtime generation.
```

### Separate Message Delivery From Runtime Convergence

Workspace inbox sends are queued. A sender can return while another goroutine
is already draining workspace work and before the receiver callback runs. Even
after the callback returns, the node can still have asynchronous runtime work
in progress.

Control-plane APIs that notify nodes must use an explicit inbox-delivery
barrier before they acknowledge a mutation. Callers that require the data plane
must then wait for the affected node's readiness state. Neither step replaces
the other:

- the delivery barrier proves that the node accepted the new desired state;
- the node readiness state proves that its current runtime generation applied
  that state.

Invariant:

```text
A control-plane response is not a runtime-convergence barrier unless its API
contract explicitly waits for both message delivery and node readiness.
```

### Track Resource Identity And Open Generations

Background routines can report after the resource they used has already been
replaced. This is normal during restore, relink, retry, config apply, and
intentional socket reopen.

Async completions and runtime errors should carry enough identity to decide
whether they still belong to the current runtime:

- node runtime generation
- attachment generation
- connection/listener/socket open generation
- resource pointer or wrapper identity, when safe and meaningful
- retry generation, when the completion came from a scheduled retry

Stale completions must not clear newer errors, cancel newer retries, mark newer
resources failed, or publish `up`.

Invariant:

```text
A late result from an old generation is data about the old generation only.
It must not mutate the current runtime state.
```

### Guard Runtime Event Streams Too

Some runtime resources do not only report completion or errors back to the
node. They can also drive the lower-level runtime directly through event
streams. For example, a TUN wrapper can emit lifecycle events that tell an
embedded device to go up or down.

Those events can arrive after the graph attachment has been logically detached
and its generation has been invalidated, but before the physical resource has
been removed from the lower-level runtime. The node may correctly report
`waiting for TUN` while the embedded runtime briefly reopens listeners or
sockets in response to a stale event.

Good patterns:

- drain buffered lifecycle events before handing a wrapper to a runtime that
  has its own event reader
- explicitly detach stale installed resources before applying config that can
  reopen sockets or listeners
- treat "desired generation moved on" as inactive even if the old resource is
  still physically installed
- test monotonic side effects such as listen/open counts immediately after
  link removal and config apply

Invariant:

```text
A stale resource event must not be able to make the lower-level runtime active
after the node has invalidated that resource's graph generation.
```

### Keep Failed Attachments Distinct From Missing Attachments

A failed runtime resource can still be non-nil. For example, a receive loop can
stop while the bind, listener, device, or wrapper object remains stored on the
node.

When a failure requires reconstruction, record that explicitly. Do not rely on
nil/non-nil checks to decide whether retry can reuse the resource.

Good patterns:

- `currentAttachmentFailed` or equivalent failure bit
- failed generation marker
- active predicate like `attachment != nil && !attachmentFailed`
- retry path that treats failed attachments like missing attachments
- label/state calculation that keeps the node in `error` while the failed bit is
  set

Same-source re-emits can be useful recovery signals. If a current failed
attachment belongs to the same source as a fresh payload, do not treat the
source match as proof the failed attachment can be reused; rebuild or reattach
before clearing errors.

Invariant:

```text
A node must not publish "up" after a fatal runtime attachment error until the
failed attachment has been replaced or detached.
```

### Do Not Let Sentinel Errors Hide Active Failures

Low-level I/O libraries often use sentinel errors such as `net.ErrClosed` for
normal shutdown. Those same sentinels can also appear when the current active
resource fails underneath a node. If an upstream loop treats the sentinel as
expected shutdown before notifying subscribers, a wrapper that exposes the
sentinel through `Unwrap` or `Is` can accidentally hide the failure from the
node state machine.

Do not encode lifecycle meaning in the sentinel alone. Carry explicit metadata
that says whether the close was intentional and which runtime/open generation it
belongs to. Preserve that metadata until the node-owned error handler can decide
whether to ignore a stale/intentional close or fail and rebuild the current
attachment.

Good patterns:

- wrapper error types that carry runtime, attachment, and open generation
- an explicit `intentionalClose` or equivalent flag set before closing resources
- opaque wrappers when the upstream library suppresses specific sentinels before
  reporting errors
- manual unwrapping inside the node after generation checks recover the concrete
  wrapper type
- tests proving unexpected active-resource closes are reported and recovered

Invariant:

```text
A normal-shutdown sentinel from a lower layer is not proof that the current
resource was intentionally closed by this node.
```

### Rebuild Before Clearing Error State

Retry code can accidentally make a stale resource look healthy:

1. background I/O reports a recoverable error
2. node records only the error text
3. retry sees a non-nil resource
4. retry reapplies config to the stale resource
5. retry clears the error and publishes `up`

For recoverable runtime errors, decide first whether the resource that failed
must be rebuilt. If yes, rebuild or detach it before applying config, clearing
the runtime error, cancelling retry, or publishing `up`.

Be careful with operations that close and reopen runtime internals as a side
effect. A config apply, bind swap, or listener replacement can intentionally
close an old receive routine. That close can report asynchronously after the new
runtime is already active, so the error handler must verify the reported
generation before marking the current runtime failed.

### Keep Desired Config Separate From Runtime Identity

A config operation and an attachment operation can invalidate different work.
Do not use one generation for both meanings.

For example, a config update can emit a new TUN configuration. The provider can
then replace its TUN and advance the runtime generation before the original
device-config job starts. Dropping that old runtime job is correct, but the
latest desired device config is still unapplied.

Track both values:

- the latest desired config generation
- the latest config generation applied successfully

Every current attachment transition must check whether these values differ. If
they differ, it must schedule config work for the current runtime. Link changes
must also preserve pending config work when no attachment payload or detach
completion will arrive.

Every replacement config job must do the same full resource reconciliation as
the job that it replaces. For example, a WireGuard attachment retry must still
delete removed node-owned peers. A partial apply can otherwise record the new
ownership set while the removed peer stays in the device permanently.

Update resource ownership after each operation that can have a partial effect.
Do not wait for a multi-resource batch to succeed. A failed batch must retain
enough ownership data for the next desired snapshot to remove all partial
results.

Invariant:

```text
Invalidating one runtime operation must not lose an unapplied desired config.
```

### Separate Durable Control State From Replaceable Data Plane State

One runtime object can contain state with different lifetimes. For example, an
external service can register a generated key before the node creates and
attaches its local packet device. The registration and key are durable control
state. The local device, TUN child, bind, and DNS wrapper are replaceable data
plane state.

A data-plane activation can fail after it has created part of the data plane.
Do not use request-generation status to select its cleanup. The request can be
current when cleanup runs and become stale before its result is delivered.
Close only the partial data plane and let an equivalent generation reuse the
registration. A current failure can report an error without deleting the
durable control state.

Delete durable state only when the desired material changes or its owning
lifecycle object is explicitly detached. A temporary down payload from a still
linked TUN or network provider is not a detach. Preserve the durable state and
wait for that provider's replacement payload.

Tests must inject both forms:

- invalidate the generation during setup, return an error, and verify that the
  next generation reuses the registration and key;
- return the same error while the request is current, advance the generation
  before handling its result, and verify that the next generation still reuses
  the registration and key;
- make a linked TUN or network payload temporarily unavailable, replace it,
  and verify that no second registration occurs;
- explicitly detach the TUN or network link and verify that the complete
  control and data-plane state closes; and
- permit a new data-plane object when an in-place attachment replacement is
  not supported, but require the same durable identity and no second external
  registration.

Invariant:

```text
A local data-plane failure must not delete matching durable control state.
```

### Cancel Retry Only After Proven Convergence

The arrival of a replacement payload is progress, but it is not proof of
recovery. The payload can arrive after a failure result schedules retry and
before its own attachment result is handled. If payload receipt cancels retry,
a stale or incomplete attachment result can leave no recovery work.

Keep retry active until a current-generation operation proves the required
runtime is healthy. A new desired config can replace old retry work only when it
also schedules its own convergence operation.

### Complete Async Requests On Every Terminal Path

Some controller operations wait for runtime readiness through a result channel.
Generation rejection alone is not completion. Node stop, link removal, resource
replacement, and terminal runtime failure must either complete or explicitly
fail every affected waiter.

Readiness for a waiter must use current desired resources. An older installed
TUN or bind is not sufficient while its replacement is pending.

### Avoid Equivalent Rebuild Churn

Config events can arrive after a node has already emitted wrappers or after
downstream nodes have attached them. Rebuilding the runtime for an equivalent
effective config closes live wrappers and can make the first user request race
against replacement.

Before rebuilding a live runtime, normalize the effective runtime options and
compare them with the options used to create the active instance. Rebuild only
when the material runtime behavior changes.

Examples of values that should be normalized before comparison:

- generated or propagated TUN addresses
- MTU
- DNS servers
- runtime stack/channel sizing options
- feature toggles that affect packet behavior

### Preserve Wrapper Identity For Same-Source Re-Emits

Providers should not close and recreate detached network/TUN wrappers just
because a downstream node requested the current value again or the provider
re-emitted the same source.

Consumers may already hold the previous wrapper. Closing it during `OnReady`,
`OnLinkAdd`, `std.Request`, or config propagation can leave downstream nodes
with a closed runtime path even though the underlying source did not change.

Same-source suppression must still check that the current wrapper is ready. A
stale or closed current wrapper should be replaced even when the replacement
unwraps to the same underlying source; otherwise close/re-emit paths can leave
consumers permanently bound to a dead wrapper.

Prefer per-link output caches for detached wrappers. Close wrappers only when:

- the link or node lifecycle ends
- the underlying source is intentionally replaced
- the wrapper itself is known to be closed or invalid

Cache identity and cache health are separate properties. A shared root can
still refer to the same source after its read or write worker stops. New child
wrappers on that root do not repair the data path. A cache must include root
health in its reuse decision and must replace a terminally failed root.

### Notify Linked Consumers When A Value Becomes Unavailable

Closing an emitted wrapper does not update the state of a consumer while its
graph link stays attached. The consumer can keep a closed DNS, Network, or TUN
value, keep a healthy label, or wait until a request deadline because no graph
callback tells it to detach the value.

When a provider invalidates outputs but keeps their graph links, it must emit
the link type's explicit unavailable payload before it emits a later
replacement. For DNS, Network, and TUN links, this is the matching payload with
a nil runtime value. Consumers must close and clear their current value when
they receive that payload. An unavailable payload received while the consumer
already has no value is idempotent.

Do not emit an unavailable payload for only a link removal: both endpoints
already receive `OnLinkRemoved`. Do not emit it from node stop cleanup unless
the node deliberately keeps its links active during that transition.

Invariant:

```text
A provider must not close a published runtime value while its graph link stays
attached without also telling the linked consumer that the value is unavailable.
```

### Treat Runtime Geometry As Mutable State

Values read when a worker starts can become stale while the resource identity
stays the same. Examples include MTU, packet capacity, read and write offsets,
batch size, queue depth, and address-family support.

The worker that owns the source operation must also own changes to the buffers
used by that operation. Keep one source reader while a read capacity changes.
Do not start a second reader as a quick recovery action. Serialize the change
with the read pump, discard the packet that found an insufficient capacity when
necessary, and verify that a later packet is delivered.

Validate the complete I/O result. A nil error does not make a reported packet
size valid when that size is larger than the visible output region given to the
callee. A larger backing allocation does not extend the visible slice. Prefer a
typed capacity error so the owner can distinguish a recoverable resize from a
terminal source failure.

Invariant:

```text
Create-time resource geometry must not remain authoritative after the live
source changes that geometry.
```

### Observe Terminal TUN Failure On I/O And Event Paths

A detached TUN can publish `EventDown` before its consumer receives the
terminal I/O error. A lower-level splitter can process that event first, mark
its backend down, and wait for `EventUp`. It then does not call `Read` again and
cannot observe the terminal error that requires source replacement.

If a node owns replacement policy, observe terminal state on both paths:

- report terminal errors returned by `Read` and `Write`;
- when `EventDown` arrives, distinguish a terminal detached-pump failure from
  a recoverable down state;
- treat unexpected closure of the event stream as terminal; and
- carry the link and attachment generation in the recovery message.

This policy applies to each consumer input, not only to the first splitter in
a graph. `TunSplitter` tracks its input generation. `TunJoiner` tracks the
default input and each secondary link independently. On a terminal failure,
the consumer detaches that input and requests a new payload. The generation
guard rejects an old failure or retry after a newer payload is attached. A
lower-level detach is not sufficient because it cannot request a replacement
from the graph owner.

MTU capacity errors are different. Gonnect can grow a detached root's read
capacity and continue with the same source reader. Do not replace that root or
add a second native reader. The packet that discovers the larger capacity can
be lost, so tests must send and verify a later packet.

For a mutable public DefaultTun wrapper, capture source generation before a
rebuild call. After the call, the same wrapper can already refer to a different
native source. Do not use wrapper equality or a post-build file lookup as the
only source-identity check, and do not close the active stable wrapper.

### First Requests Expose Readiness Bugs

HTTP clients, UDP ping clients, and similar request nodes generally use the
currently received runtime object. They do not prove path-wide readiness before
issuing the request.

Retrying helpers and e2e tests that wait for eventual success can hide startup
readiness bugs. A path can pass eventual-success tests while the first real
request after daemon startup times out.

For startup-sensitive paths, add tests that:

1. build or restore the graph
2. wait only for the state the graph currently advertises as ready/up
3. send exactly one request or ping
4. fail if that first attempt times out or needs a retry

Keep eventual-success tests, but do not treat them as proof of first-request
readiness.

### Probe Each Data-Plane Boundary Separately

A successful probe can enter the graph after the failed component. It then
proves that the later path works, but it does not prove that the complete path
works. During the DefaultTun incident, in-daemon HTTP clients succeeded while
the host path through the native DefaultTun reader was stopped.

Map the start and end point of each probe. For a host TUN path, test at least:

- a host request that must enter the native TUN
- an internal graph request that does not use that ingress
- direct addressing when DNS is a separate possible failure
- the same probes after the intended repair action

A target reproduction is conclusive only when its prerequisite probes succeed
and the target boundary fails. Classify a failed prerequisite as inconclusive.
Do not use it as evidence for the target bug.

Invariant:

```text
Success on an adjacent data path is not proof that the affected ingress or
egress boundary is healthy.
```

### Keep Runtime Work Off Workspace Callbacks

Long-running runtime operations should not run while holding workspace callback
or node event stacks. Use the node's normal async boundary for completion
messages, then apply state changes on the node-owned path.

The completion handler should still perform generation checks before changing
status, retry state, labels, resources, or menus. Async boundaries prevent lock
contention; they do not by themselves solve stale completion bugs.

### Recheck Identity After Async And Lock Boundaries

A current-generation decision can become stale before its visible side effect.
For example, a worker can record a TUN failure under the node mutex, release the
mutex to close an adapter, wait for the workspace lock, and then overwrite the
label of a replacement TUN.

Carry the generation and resource identity across every boundary. Recheck them
under the lock that protects the visible side effect immediately before you
publish a label, emit a wrapper, cancel retry, update a menu, or close a shared
resource. Follow the established lock order; do not retain a node mutex while
you wait for a workspace callback that can call the node again.

Invariant:

```text
A generation check authorizes work only until the next asynchronous or lock
boundary.
```

### A Stop Request Is Not Worker Completion

Closing a worker's stop channel only requests shutdown. The worker can still
own a listener, socket, TUN reader, or other exclusive resource until its done
signal closes. Starting the replacement immediately can make both generations
active and can cause bind conflicts or concurrent reads.

Keep the stopping worker identity until it finishes. Coalesce later config
changes into the latest desired state. After the old worker's done signal
closes, verify its identity and start one replacement with the current desired
config.

Tests should hold the old worker after it receives the stop request. Verify
that no replacement starts before release, then verify that exactly one worker
starts with the newest config.

## Implementation Checklist

When adding or refactoring a runtime node, answer these before merging:

- What exact resource or operation makes this node usable?
- Can the node currently publish `up` before that operation completes?
- Does every async completion carry enough identity to reject stale results?
- Can an intentional close/reopen report an error after a replacement is active?
- Does a routine error invalidate the specific attachment it depended on?
- Can a sentinel error such as `net.ErrClosed` be suppressed before the node
  records an active-resource failure?
- Does retry rebuild failed attachments before applying config or clearing
  errors?
- Does same-source suppression reject only ready current wrappers?
- Does a cache verify the health of its shared root, not only source identity?
- Can runtime geometry change while the wrapper or native source stays the same?
- Does resize or replacement preserve the one-reader or one-listener invariant?
- If an operation marks an attachment failed, did retry eligibility get captured
  before that mutation?
- Can an old success clear a newer error or cancel a newer retry?
- Can a replacement payload cancel the only retry before recovery succeeds?
- Can a config generation remain unapplied after runtime generation changes?
- Does every pending result channel complete on stop, detach, and failure?
- Can a stale resource event reopen sockets after link detach but before
  physical detach?
- Does replacement wait for the old worker to release exclusive resources?
- Are buffered lifecycle events drained before attaching resources to runtimes
  with their own event readers?
- Can a same-source request or re-emit close wrappers still held downstream?
- Can a provider close a published value while its graph link stays attached
  without sending an unavailable payload?
- Does config propagation rebuild live resources for equivalent effective
  options?
- Does temporary provider unavailability differ from an explicit graph unlink?
- Is identity rechecked after each async or lock boundary and before visible
  workspace state changes?
- Do probes cross the exact ingress or egress boundary under investigation?
- Do tests prove resource ordering, or only that retry eventually happened?

## Regression Test Patterns

Prefer tests that prove ordering and identity:

1. inject a deterministic one-shot runtime error into the real background I/O
   path
2. wait until the node publishes or records `error`
3. record a resource generation, open count, connection ID, wrapper identity, or
   listen count
4. wait for recovery
5. assert the node does not publish `up` until the failed resource identity has
   changed or the failed resource has been detached

For readiness tests:

1. arrange a node state where required inputs are present
2. delay or omit the async setup completion
3. assert the node reports `starting`, `error`, or another non-`up` state
4. complete the async setup
5. assert `up` is published only for the current generation

Avoid tests that only assert "retry was attempted". They can pass while the node
still loops between `error` and `up` on the same stale resource.

For an intermittent runtime failure, use a layered investigation:

1. Capture failed, clean, and repaired runs with the same script and topology.
2. Record precise timestamps, source and attachment generations, effective
   runtime geometry, counters, file descriptors, and worker stacks.
3. Compare normalized route, rule, firewall, DNS, socket, and graph state to
   reject competing causes.
4. Reproduce the lowest failing interface contract in a deterministic test.
5. Test the dependency that owns that contract, then the Almagest consumer,
   then a representative deep graph, and finally the affected host boundary.

Do not select a root cause from one warning or one failed run. A clean run with
the same warning or state transition can show that the event is not sufficient.
Use a repair that changes one suspected lifecycle owner when possible, and
verify that both worker state and data transfer recover.

Capture-script readiness checks must use stable semantics. Prefer node class and
operational label patterns over generated node names or exact dynamic counts.
Require the labels to stay operational for a short stable interval. If a wait
times out, record the actual labels so a script defect is distinct from a daemon
defect.

Keep unrelated randomness out of lifecycle tests. Bound protocol fields below
transport limits, and remove timing dependencies that need a retransmission to
succeed. Test randomized protocol compatibility in a separate case. This keeps
a protocol stimulus failure from looking like a resource-lifecycle failure.

## Fixed Bugs And Coverage

### DefaultTun Reused A Stopped Detached Root

Bug:

- A detached output root started with an MTU of 1420 and kept that read
  capacity after the same native TUN changed to MTU 1428.
- The Linux virtual-network-header and GSO path could report one successful
  1428-byte packet through the old visible packet region.
- Gonnect converted the invalid result to `io.ErrShortBuffer` and stopped the
  root read pump. The old root still reported `up`.
- `TunOutputCache` matched the same source and attached replacement children to
  the stopped root. New children could not repair the host data path.
- Internal graph probes stayed healthy because they did not enter through the
  native DefaultTun reader.

Investigation:

- Two failed captures had the same MTU-growth and short-buffer sequence. Their
  goroutine stacks had the root write pump, but not its read pump or native TUN
  read. A clean capture had both pumps.
- Route, policy-rule, firewall, and resolved state were equivalent in the clean,
  failed, and repaired states. DNS and internal HTTP probes also succeeded
  during the host failure.
- A clean run survived the same MTU increase. This showed that the MTU change
  alone was not sufficient. A larger GSO result during that window was the
  required trigger.
- A deterministic dependency test reproduced the successful oversized result.
  A cache test then reproduced the stopped read root without using host
  networking.

Fix:

- Gonnect grows the owning root after an MTU event, a short-buffer result, or a
  successful result that exceeds the current visible packet capacity. It keeps
  one source reader and reports terminal pump failure through root health.
- tuntap validates visible output capacity before Linux GSO splitting and
  returns a typed capacity error.
- `TunOutputCache` reuses only a ready root. It replaces a terminally failed
  root and nests per-link children below one stable root.
- sysnet-linux exposes one stable public DefaultTun wrapper and a source
  generation. DefaultTun captures the generation before a build that can change
  the wrapper's native source.
- TunSplitter and TunJoiner observe terminal state on I/O and event paths. They
  request replacements with independent link and attachment generations.
- Tun2Dns retries a failed input request until it accepts a replacement. Its
  failure publication rechecks the input generation after it acquires the
  workspace lock.

Coverage includes deterministic capacity growth, terminal read and write
failure, same-source reuse, native-source replacement, stale consumer messages,
and generated graphs at depths 1, 4, and 8. The privileged
`default_tun_routing` case also uses two splitter/joiner stages, IPv4 and IPv6,
TCP and UDP, an MTU matrix from 1280 through 9000, and forced native-device
replacement.

The final acceptance gate on 2026-09-12 was six consecutive clean runs of
`scripts/capture-default-tun-flake.sh` on the affected host and 40 consecutive
clean runs of `just test-total`. The incident is resolved. See
[DEFAULT_TUN_HOST_NETWORK_FAILURE.md](DEFAULT_TUN_HOST_NETWORK_FAILURE.md) for
the full evidence, dependency versions, test list, and return-of-failure guide.

### WireGuard Published `up` Before Bind Attach

Bug:

- WireGuard stored `currentTun` and `currentNetwork`, then scheduled actual
  bind attach/config apply asynchronously.
- `stateText` treated TUN and network presence as `up`.
- Graph observers could send the first ping or HTTP request while the UDP bind
  was not attached, opened, or configured.

Fix:

- WireGuard now tracks `runtimeReadyGeneration`.
- It reports `starting` while inputs are present but async setup for the current
  generation has not completed.
- It reports `up` only when the ready generation matches the current runtime
  generation and active attachments are healthy.

Coverage:

```sh
go -C lib test ./wireguard -run TestWireGuardStateDoesNotReportUpBeforeNetworkBindAttachCompletes -count=1 -timeout 20s -v
```

Test location:

- `lib/wireguard/node_wireguard_internal_test.go`:
  `TestWireGuardStateDoesNotReportUpBeforeNetworkBindAttachCompletes`

Related implementation:

- `lib/wireguard/node_wireguard.go`
- `lib/wireguard/node_wireguard_runtime.go`
- `lib/wireguard/node_wireguard_device.go`

### WireGuard Published A Controller Before Applying Its Requested Key

Bug:

- The menu showed the desired public key before the async device config job
  applied it. A new WGCtrl link could receive the device with its previous key.
- Amnesia validated its profile against that old key and reported a mismatch.
  Later broadcasts reused the same API, so they did not restart reconciliation.

Fix:

- Controller publication checks the applied config generation against the
  desired generation without taking the runtime lock in workspace callbacks.
- Both config completion and attachment completion retry publication. An
  attachment can invalidate a config completion after the config was installed.
- A refresh request received during config apply discards the cached API, so
  the later broadcast still creates the fresh API requested by middleware.

Coverage: `TestWireGuardControllerWaitsForCurrentDeviceConfig` blocks device
updates and checks initial publication, cached refresh, and publication after
an attachment makes the config completion stale.

### WireGuard TUN Events Changed Device State Outside The Node

Bug:

- Detached TUN wrappers can emit buffered lifecycle events, including an
  initial `EventUp`.
- `wgo` has its own TUN event reader that can call `device.Up()` independently
  of Almagest's runtime generation checks.
- During a TUN replacement, an old `EventDown` could stop peers after the new
  WireGuard configuration was applied.
- After a TUN link was removed, a stale `EventUp` could also bring `wgo` up
  outside Almagest's runtime generation checks.

Fix:

- WireGuard filters TUN `EventUp` and `EventDown` values. The node owns device
  state changes, while MTU update events still reach `wgo`.
- Config apply first physically detaches any installed TUN whose generation is
  no longer current.
- An already-active device keeps its peers and UDP transport running while it
  waits for a replacement TUN. Initial startup still requires both TUN and
  network attachments before the device starts.
- A hot TUN replacement does not reapply unchanged device configuration or
  refresh the UDP bind.

Coverage:

```sh
go -C lib test ./wireguard -run TestWireGuardNetworkAttachmentDoesNotOpenSocketsUntilTunAttached -race -count=300
```

Test location:

- `lib/wireguard/node_wireguard_internal_test.go`:
  `TestWireGuardNetworkAttachmentDoesNotOpenSocketsUntilTunAttached`

Related implementation:

- `lib/wireguard/node_wireguard_device.go`

### VTun Rebuilt For Equivalent `TunConfig`

Bug:

- VTun could emit an initial dynamic runtime and wrappers.
- A later `TunConfig` with the same effective address and MTU rebuilt the VTun.
- Rebuild closed old wrappers and emitted replacements even though runtime
  behavior had not materially changed.
- Downstream nodes could race against the replacement during startup.

Fix:

- VTun records the normalized effective runtime config used to build the active
  VTun.
- Equivalent config events reuse the active VTun and wrappers.
- Rebuild happens only when effective runtime options materially change.

Coverage:

```sh
go -C lib test ./tun -run TestVTunEquivalentTunConfigDoesNotRebuildActiveVTun -count=1 -timeout 20s -v
```

Test location:

- `lib/tun/node_vtun_external_test.go`:
  `TestVTunEquivalentTunConfigDoesNotRebuildActiveVTun`

Related implementation:

- `lib/tun/node_vtun.go`
- `lib/tun/node_vtun_runtime.go`

### WireGuard Receive EOF Reused Failed Bind

Bug:

- A recoverable UDP receive error could stop the receive loop.
- The old bind attachment remained non-nil.
- Retry reused that failed bind, reapplied config, cleared the error, and
  published `up` without opening a new UDP bind.
- The node could loop through `error -> up -> error -> up`.

Fix:

- WireGuard tracks `currentBindFailed`.
- Active attachment checks require the bind to be present and not failed.
- Retry treats a failed bind like a missing bind and schedules bind replacement
  before config apply.
- Label/state calculation keeps the node in `error` while the failed-bind bit is
  set.

Coverage:

```sh
go -C lib test ./wireguard -run TestWireGuardReceiveEOFRebuildsBindBeforePublishingUp -count=1 -timeout 20s -v
```

Test location:

- `lib/wireguard/node_wireguard_internal_test.go`:
  `TestWireGuardReceiveEOFRebuildsBindBeforePublishingUp`

Related implementation:

- `lib/wireguard/node_wireguard.go`
- `lib/wireguard/node_wireguard_bind.go`
- `lib/wireguard/node_wireguard_runtime.go`

### WireGuard Accepted Stale Receive Errors

Bug:

- Bind replacement or socket reopen intentionally closed an old receive routine.
- The underlying device reported that close asynchronously.
- If accepted as current, the stale error marked the fresh bind failed and could
  start a false recovery loop.

Fix:

- WireGuard receive errors carry bind generation and open generation.
- Errors from older bind objects are ignored.
- Errors from intentional closes of older open generations are ignored after a
  later reopen.
- Bind attach/replacement brings the device down before swapping the bind and
  applying config, then brings it up once.

Coverage:

- Covered by generation checks in the WireGuard runtime code.
- Practical validation should use a representative daemon setup and inspect
  logs after `engine built`; startup socket churn may produce debug receive EOF
  lines, but steady-state `WireGuard node error: receive from bind: EOF` should
  not remain. Shutdown-time EOF after `Stopping` is normal shutdown noise.

### WireGuard Replacement Lost Recovery Work

Bug:

- A named controller transport could be temporarily closed while WireGuard
  reopened all transports.
- WireGuard correctly scheduled a retry after the reopen failed.
- A replacement TUN or network payload then cancelled that retry before its
  attachment result proved recovery.
- In the opposite ordering, the attachment generation made the failed config
  job stale, but no later job knew that the desired config was still unapplied.
- The node stayed in `starting` until the e2e timeout.

Fix:

- Attachment payload receipt does not cancel recovery retry.
- WireGuard tracks desired and successfully applied config generations
  separately.
- Current attachment and link transitions reschedule skipped config work.
- Config convergence repairs a failed current bind before it can succeed or
  cancel retry. Recovery does not depend on which event scheduled the job.
- A successful current-generation operation rechecks whether config work is
  still needed before it refreshes live sockets.
- Every replacement config job fully reconciles node-owned peers.

Coverage:

```sh
go -C lib test ./wireguard -run 'TestWireGuard(AttachmentPayloadsDoNotCancelFailureRetry|TunReplacementAppliesSkippedCurrentConfig|LinkChangesDoNotDropPendingConfigApply|LocalPeerRebuildPreservesControllerPeers)' -race -count=10
```

### WireGuard Waiters And Controller Reconcile Could Stop Permanently

Bug:

- A WireGuard interface-config apply could wait forever after TUN removal or a
  later TUN generation failure.
- An older TUN could satisfy the apply while a replacement was still pending.
- WGConfig kept an old `up` state while reconcile was pending and did not retry
  transient reconcile failures.
- Replaced network wrappers remained open.

Fix:

- Pending interface applies fail on stop, link removal, and applicable runtime
  failures. They require the current desired TUN and, when expected, a healthy
  current network bind.
- WGConfig reports `starting` during reconcile and retries failures with bounded
  exponential backoff.
- Peer ownership survives partial reconciliation failures, so later retries or
  removals can clean all applied peers.
- Both WireGuard nodes close stale and replaced network wrappers.

### Same-Source Network Re-Emits Closed Wrappers

Bug:

- A provider could respond to a repeated request or same-source re-emit by
  closing and recreating a detached wrapper.
- Consumers holding the old wrapper could observe EOF even though the
  underlying network source had not changed.

Fix:

- Network providers should use `NetworkOutputCache` or the equivalent local
  pattern so a link gets stable detached wrappers for the same live source.
- Detached wrappers are closed when their link/node lifecycle ends or when the
  underlying source is intentionally rebuilt.

Coverage:

```sh
go -C lib test ./links -run 'TestNetworkOutputCache(ReusesReadyWrapperAndReplacesClosedWrapper|ReplacesChangedSource)' -count=1
```

Test location:

- `lib/links/network_test.go`:
  `TestNetworkOutputCacheReusesReadyWrapperAndReplacesClosedWrapper`
- `lib/links/network_test.go`:
  `TestNetworkOutputCacheReplacesChangedSource`

### Tailscale Consumed An Event Before Runtime Activation

Bug:

- Tailscale subscribed to coalesced client events before it started the client.
- Its event watcher could consume the newest event before the workspace handled
  the initial runtime result.
- The workspace rejected that early event because the runtime ID was not active
  yet. If no later event arrived, the node kept an older initial snapshot.

Fix:

- The event watcher waits on a runtime-specific activation signal.
- The runtime result applies its initial snapshot and marks the runtime active
  before it releases the watcher.
- Generation cancellation releases an unactivated watcher without consuming
  its buffered event.

Coverage:

```sh
go -C lib test ./tailscale -run TestTailscaleRuntimeEventWatcherWaitsForActivation -race -count=1
```

Test location:

- `lib/tailscale/runtime_test.go`:
  `TestTailscaleRuntimeEventWatcherWaitsForActivation`

Related implementation:

- `lib/tailscale/runtime.go`

### Managed Amnesia Activation Removed Durable Registration

Bug:

- A successful service negotiation registered an ephemeral public key before
  the local WireGuard device became active.
- A later TUN, bind, DNS, pre-connect, or device-start failure could close the
  complete runtime. Recovery then registered another service key even when the
  requested connection material had not changed.
- A generation check during cleanup was not sufficient. A request could be
  current when cleanup ran and become stale before its result was delivered.
- A temporary missing payload from a linked TUN or network provider was treated
  like an explicit unlink and could also discard the registration.

Fix:

- The service client, negotiated profile, and registered key are durable
  control state. The device, detached TUN, bind, and tunnel DNS resolver are
  replaceable data-plane state.
- Activation failure always closes only the partial data plane. An equivalent
  generation can reuse the registered profile.
- A temporary linked-provider gap keeps the control state. An explicit unlink
  or changed connection material closes the complete runtime.
- Network replacement creates a new local data plane because the bind cannot
  move to another network wrapper. TUN replacement uses in-place replacement
  when supported and otherwise rebuilds the device with the same registered
  identity.

Coverage:

```sh
go -C lib test ./amnesia -run 'TestManagedAmnesiaRuntime(KeepsSuccessfulNegotiationAcrossStaleGeneration|KeepsPreparedProfileWhenTunGoesDownDuringReconcile|KeepsProfileAcrossCarrierReplacement|ReattachesTunAfterStaleActivation|KeepsProfileAfterStaleTunSetupFailure)' -race -count=1
```

### WireGuard Kept A Closed TUN Logically Active

Bug:

- WireGuard treated a non-nil, generation-matched TUN as active.
- An indirect upstream close could stop the device TUN while its graph link
  remained attached.
- The node could keep its previous `up` label and did not request a replacement
  payload.

Fix:

- The installed TUN has a terminal I/O and event-stream observer.
- Failure messages carry the exact observer and TUN generation.
- A current failure invalidates readiness, fails affected controller waiters,
  and requests a replacement from the still-linked provider.
- Active-state and same-instance checks reject a closed or failed TUN.

Coverage:

```sh
go -C lib test ./wireguard -run 'TestWireGuard(StateDoesNotReportUpWithClosedTun|TunFailureRejectsStaleAttachmentAndInvalidatesCurrentTun|E2EVTunHTTPClientServer)' -race -count=1
```

### Managed Amnesia Kept `up` After Its TUN Closed

Bug:

- Managed Amnesia marked its negotiated data plane ready but did not observe a
  later terminal failure from the detached device TUN.
- Closing a linked upstream TUN could leave the workspace label at `up` and no
  replacement request was sent.

Fix:

- Both the plain detached-TUN and TunnelDNS device paths use the shared terminal
  TUN observer.
- Failure handling checks reconcile generation, durable runtime identity, and
  exact observer identity.
- A current failure closes only the replaceable data plane, requests a new TUN,
  and keeps the registered profile and service key.

Coverage:

```sh
go -C lib test ./amnesia -run TestManagedAmnesiaTerminalTunFailureRequestsReplacementAndKeepsRegistration -race -count=1
```

### Tun2Dns Could Stop Without A Usable Replacement

Bug:

- A terminal TUN read stopped the packet pump. The adapter stayed in an error
  state without persistent replacement work.
- A replacement could arrive after the old pump changed its local state but
  before the old failure acquired the workspace lock. The old failure could
  then publish a stale error label over the replacement state.

Fix:

- A terminal current-generation failure closes the adapter and starts a retry
  for the failed link and generation.
- Retry continues while that exact generation is failed. A replacement, unlink,
  or stop invalidates the timer.
- The failure path rechecks generation, TUN identity, and failed state after it
  acquires the workspace lock and before it changes the label.

Coverage:

```sh
go -C lib test ./dns -run 'TestTun2Dns(RequestsReplacementAfterTerminalTunRead|StalePumpFailureCannotOverwriteReplacementLabel)' -race -count=1
```

### HTTP Server Restart Overlapped The Previous Worker

Bug:

- Restart requested stop but cleared the old worker identity immediately.
- A second config event could start a replacement while the old worker still
  owned its listener.
- Multiple events could also schedule obsolete replacement configs while the
  old worker stopped.

Fix:

- The node retains the old worker and a stopping flag until `Done` closes.
- Later events update desired node state but do not start another worker.
- The done message verifies the old worker identity and starts one replacement
  from the latest desired state.

Coverage:

```sh
go -C lib test ./http -run TestHTTPServerRestartWaitsForPreviousWorker -race -count=1
```

## Full Validation

After changes that affect runtime lifecycle, run the project-level checks:

```sh
just lint
just test-total
```

For changes touching startup readiness, also consider a no-retry first-request
test or a focused daemon run with a representative setup. Eventual success
alone is not enough evidence for startup correctness.
