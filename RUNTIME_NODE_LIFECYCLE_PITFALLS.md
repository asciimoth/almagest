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
A node must not reports that it is fully up until the runtime attachment that
makes it usable is active for the current runtime generation.
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

### Keep Runtime Work Off Workspace Callbacks

Long-running runtime operations should not run while holding workspace callback
or node event stacks. Use the node's normal async boundary for completion
messages, then apply state changes on the node-owned path.

The completion handler should still perform generation checks before changing
status, retry state, labels, resources, or menus. Async boundaries prevent lock
contention; they do not by themselves solve stale completion bugs.

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
- If an operation marks an attachment failed, did retry eligibility get captured
  before that mutation?
- Can an old success clear a newer error or cancel a newer retry?
- Can a stale resource event reopen sockets after link detach but before
  physical detach?
- Are buffered lifecycle events drained before attaching resources to runtimes
  with their own event readers?
- Can a same-source request or re-emit close wrappers still held downstream?
- Does config propagation rebuild live resources for equivalent effective
  options?
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

## Fixed Bugs And Coverage

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

### WireGuard Opened Sockets After TUN Detach

Bug:

- Detached TUN wrappers can emit buffered lifecycle events, including an
  initial `EventUp`.
- `wgo` has its own TUN event reader that can call `device.Up()` independently
  of Almagest's runtime generation checks.
- After a TUN link was removed, Almagest invalidated the TUN generation and
  scheduled async detach, but the old TUN could still be physically installed
  briefly.
- A stale TUN event could bring `wgo` up during that window. A following config
  apply that changed `listenPort` then reopened UDP sockets even though the
  node was already logically waiting for TUN.

Fix:

- WireGuard drains buffered TUN lifecycle events before attaching a TUN to
  `wgo`.
- Config apply first physically detaches any installed TUN whose generation is
  no longer current.

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

## Full Validation

After changes that affect runtime lifecycle, run the project-level checks:

```sh
just lint
just test-total
```

For changes touching startup readiness, also consider a no-retry first-request
test or a focused daemon run with a representative setup. Eventual success
alone is not enough evidence for startup correctness.
