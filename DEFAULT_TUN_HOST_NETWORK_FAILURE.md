# Flaky DefaultTun Host Network Failure

## Status

The root cause is confirmed at the detached-TUN read boundary. In the old
dependency set, a cached root started with MTU 1420 and kept a fixed 1420-byte
packet buffer after the same native TUN changed to MTU 1428. The dependency's
Linux GSO path can then return a 1428-byte reported packet size without an
error. Gonnect returned `io.ErrShortBuffer` and stopped the root read pump, but
the cache still considered that root ready and reused it.

Two failed host captures show this same sequence. A third clean capture had
the same MTU change but did not receive the triggering packet during that
window. A deterministic Docker test reproduced the `short buffer` result, and
the fixed test now passes. The exact packet from the host captures was not
recorded, but the GSO trigger is strongly supported by the logs and dependency
code.

The upstream fixes are present in
[Gonnect v0.51.0](https://github.com/asciimoth/gonnect/tree/v0.51.0),
[tuntap v0.3.34](https://github.com/asciimoth/tuntap/tree/v0.3.34), and
[sysnet-linux v0.2.28](https://github.com/asciimoth/sysnet-linux/tree/v0.2.28).
Almagest now uses these versions. The temporary 1500-byte MTU floor, duplicate
read-result validation, and local output-source failure flag are removed.
Thirty consecutive runs of the capture script on the affected host were clean,
and manual regular-TUN and default-TUN tests were also clean. The host gate in
`TUN_UPSTREAM_ADOPTION_PLAN.md` is complete, and this incident is resolved.

The released implementations are Gonnect commit
`2565a37555f46b5ec8b637e7226ce95b7c4bc5bf`, tuntap commit
`6ddc67c51d505d44b0614c7f94fa22cea108c8cb`, and sysnet-linux commit
`e6946d5842467bb750471c3b671361a208723dca`. No separate upstream issue or
pull-request references are available for these release changes.

## Symptom

Start the daemon with the repository's real local configuration:

```sh
just daemon-sudo
```

The command starts `almagestd` with these arguments:

```sh
sudo ./almagestd -c ./local -l debug -w 8090 \
  --debug-nodes --unsafe-config --no-dns-cache-expiration
```

In a failed session:

- The host cannot access the network through DefaultTun.
- The graph can still access the network. `FetchAddr`, `HttpClient`,
  `HttpClient A`, and `HttpClient B` can succeed.
- Deleting the `DefaultTun` node, creating a new node with the same class and
  reconnecting the same TUN and DNS links restores host network access.
- The failure is intermittent.
- The failure is more likely when Amnesia, Tailscale, or another upstream node
  has startup errors and restarts before it becomes operational.

The important distinction is that internal graph traffic can work while host
traffic does not. A successful internal HTTP request does not prove that the
native DefaultTun read path is working.

## Relevant Local Topology

The host data path in `local/alm.hujson` starts as follows:

```text
DefaultTun
  Tun -> TunSplitter lH -> TunJoiner vh -> TunSplitter -> TunJoiner -> Amnesia VPN
  DNS -> DnsCache vt
```

The splitters also route DNS and Tailscale prefixes to other branches. The two
joiners accept VTun secondary paths. This topology causes TUN configuration,
especially the effective MTU, to change while Amnesia and Tailscale start or
restart.

The four diagnostic nodes use different graph paths:

| Node | Network source | Purpose |
| --- | --- | --- |
| `FetchAddr` | `VTun Bu` | Public Internet through the nested graph |
| `HttpClient` | `VTun Bu` | HTTP to the local Tailscale server |
| `HttpClient A` | `OutNetwork` | Direct daemon outbound network |
| `HttpClient B` | `VTun ma` | Public Internet through the inner VTun path |

## Evidence From the Captured Failed Session

The root `log` file contains one failed session and the manual DefaultTun
recreation.

- At line 446, `TunSplitter lH` reports `short buffer` on the TUN input that is
  directly connected to DefaultTun.
- At lines 533-544, `FetchAddr` and `HttpClient` complete successfully while
  the reported host problem is present.
- At lines 569-585, deletion of the old DefaultTun removes its DNS setup, two
  links, two ports, and node 135.
- At lines 592-621, the new DefaultTun is created as node 287, gets a new
  native interface index, applies DNS, and receives its first link.
- At line 696, the same splitter reports another `short buffer` after the new
  node is present.
- At lines 738-749, internal `FetchAddr` and `HttpClient` requests succeed
  after recreation.

The old log does not contain system route snapshots, a verbose host curl, TUN
object identities, cache generations, or goroutine stacks. It therefore cannot
show whether the `short buffer` event caused the host outage. The second event
after recreation also shows that a `short buffer` warning alone is not enough
to classify the host as broken.

## Root Cause: Stale Detached-TUN Read Size

The failure is an MTU and read-buffer lifecycle problem:

1. The old `tun.DetachedTun` fixed its MTU and internal native read-buffer
   length when it was created.
2. The DefaultTun output cache creates this wrapper while the nested graph is
   still starting.
3. Downstream TUN configuration later changes the native DefaultTun MTU. The
   log shows repeated effective MTU changes while Amnesia and WireGuard start.
4. Linux TUN uses `vnet_hdr` and GSO in these captures. Tuntap can report a
   complete segment size after it copies into a slice backed by a larger pool
   allocation. Thus, the source can return `count=1`, `size=1428`, and no
   error even when the visible packet space is 1420 bytes.
5. The old Gonnect implementation checks the reported size after the source
   read. It creates `io.ErrShortBuffer` and stops the detached root read pump.
6. The source monitor used to see only the successful native read. It did not
   mark the cache source failed because Gonnect created the error above it.
7. The old Gonnect implementation still reported the stopped root as up. The splitter requested a
   new child wrapper, but the output cache attached that child to the same dead
   root generation.
8. The graph remained healthy because its internal clients did not depend on
   host packets entering the native DefaultTun file.
9. Recreating DefaultTun created a new root read pump and restored the host
   path.

## Three-Capture Investigation (2026-09-11)

The capture script recorded three useful tries:

| Try | Result | Root sequence |
| --- | --- | --- |
| `20260911T130201Z-48646/try-0001` | Host failed; recreation repaired it | Root generation 1 started at MTU 1420, rebound to the same file at MTU 1428, then reported `short buffer` |
| `20260911T131018Z-58944/try-0001` | Clean | Root changed from MTU 1420 to 1428, but no oversized read occurred before MTU decreased |
| `20260911T131018Z-58944/try-0002` | Host failed; recreation repaired it | Root generation 1 started at MTU 1420, rebound to the same file at MTU 1428, then reported `short buffer` |

Both failed goroutine snapshots contain the affected root write pump, but they
do not contain its read pump or a native DefaultTun `Read`. The clean snapshot
contains both pumps and a native `Read`. In the failed tries, successful TUN
counters stop while transmit drops increase to 681 and 738. Recreating the
node creates a new root read pump, counters move again, and the host curl
succeeds.

The two failed tries contain `chatgpt.com` DNS cache hits approximately 0.56
seconds before `short buffer`. The clean try performs the equivalent lookup
after MTU has decreased to 1280. This suggests that a background TLS flow
supplied the GSO packet. DNS is not the cause because name resolution succeeds
while the host data path is broken.

### Competing hypotheses

| Hypothesis | Result | Evidence |
| --- | --- | --- |
| Route, policy rule, mark, or nftables failure | Rejected for these captures | Normalized state and route lookups are equivalent before failure, in the clean try, and after repair. The selected route uses DefaultTun and the expected source address. |
| systemd-resolved stale state | Rejected for these captures | DNS succeeds during both failures, and resolved state is equivalent after repair. Direct IP probes also fail. |
| General upstream or graph outage | Rejected | `FetchAddr` and the internal HTTP probes succeed during both host failures. |
| Duplicate native readers | Not supported | Each failed daemon has one `/dev/net/tun` file descriptor. The missing goroutine is a reader, not an extra reader. |
| Splitter child attachment failure as the first cause | Rejected | The splitter receives the root error and replaces its child. That replacement stays on the already-dead root generation. |
| MTU increase alone | Rejected | The clean capture and a privileged Docker run both survive 1420 to 1428 when no triggering GSO segment arrives. |
| Fixed read buffer plus a larger successful source read | Confirmed | Both failed captures match it. A deterministic Docker test returns the same `io.ErrShortBuffer` before the fix and passes after the fix. |

### Docker experiments

All network experiments ran in Docker containers.

1. The privileged `default_tun_routing` case changed DefaultTun from MTU 1420
   to 1428 and sent a 4 KiB HTTP request. It passed. This confirms that MTU
   growth alone is not sufficient.
2. A dependency-level GSO test used tuntap v0.3.33 with the same visible slice
   geometry as the daemon. A TCPv4 GSO segment with total length 1428 was
   written through a visible 1420-byte packet slice backed by the larger
   pooled allocation. `handleVirtioRead` returned one packet, no error, and a
   reported size of 1428. This proves the dependency behavior required by the
   capture sequence.
3. A deterministic cache test creates a root at MTU 1420, changes the source
   to MTU 1428, and completes the read that was blocked with the old capacity.
   Gonnect discards that packet, grows the same root, and delivers the next
   1428-byte packet.
4. A second deterministic cache test returns a successful 1600-byte result
   without an MTU event. Gonnect detects the capacity mismatch, polls the
   current MTU, grows the same read pump, and delivers the next packet. The
   test confirms that no concurrent source read occurs.

The deterministic tests use a container with networking disabled. They test
the exact interface contract that failed and do not change host networking.

The released source trees for Gonnect v0.51.0, tuntap v0.3.34, and
sysnet-linux v0.2.28 pass `go test -race ./...` in fresh Docker containers.
The sysnet-linux privileged system test also passes in an isolated container.
Almagest's privileged TUN, DefaultTun, WireGuard, route, callback, firewall,
splitter, and nested-tunnel cases pass. The expanded `default_tun_routing`
case uses two splitter/joiner stages, the MTU matrix 1280, 1420, 1428, 1500,
and 9000, carrier MTUs 1280, 1500, and 9000, an 8 KiB request, and forced
native-device recreation. Its logs confirm batch size 128 and the Linux
virtual-network-header path. The upstream tuntap unit suite supplies the
non-GSO control because the current Linux integration API does not provide a
safe switch for `IFF_VNET_HDR`.

## Fix

The permanent fix is split across the upstream libraries and Almagest:

- Gonnect grows the owning detached root's read capacity after an MTU event,
  `io.ErrShortBuffer`, or a successful oversized result. It keeps one source
  reader. The packet that discovers the larger capacity is discarded, and the
  same pump reads the next packet with the new capacity.
- Gonnect puts a detached root into a terminal failed state when a read or
  write pump ends. `TunOutputCache` uses this state and does not reuse a failed
  root.
- tuntap checks the visible output slice before Linux GSO splitting. It returns
  a typed capacity error instead of reporting a successful packet that does
  not fit the slice.
- sysnet-linux returns one stable public DefaultTun wrapper. Its
  `SourceGeneration` changes only when the native source changes. Almagest
  captures this generation before a rebuild. It retains cached outputs for a
  same-source rebuild and replaces them after native-source replacement. It
  does not close the stable public wrapper returned by the rebuild.
- TunSplitter continues to request a replacement after an input failure. It
  observes both I/O errors and terminal failure on the TUN event path. Event
  observation is necessary because `EventDown` can make Gonnect Splitter wait
  before it reads the terminal error.
- TunJoiner applies the same terminal-failure policy to its default input and
  to each secondary input. Each input has an independent generation and retry
  guard.

The local stable-root and generation-guard logic remains. It prevents two
readers from owning one native source and rejects stale retry messages. These
are lifecycle protections and are not duplicate packet-size validation.

Regression coverage includes:

- `TestTunOutputCacheReadSurvivesSourceMTUGrowth`
- `TestTunOutputCacheRecoversAfterSourceShortBuffer`
- `TestTunOutputCacheRecoversAfterReadExceedsInitialMTU`
- `TestTunOutputCacheReplacesSourcePumpAfterReadFailure`
- `TestTunOutputCacheMTUGrowthThroughEightNestedDetachedWrappers`
- `TestTunInputFailureObserverReportsTerminalFailureFromEvent`
- `TestTunSplitterNodeRecoversInputAfterStartupReadFailure`
- `TestTunJoinerNodeRecoversFailedInputs`
- `TestTunJoinerNodeIgnoresStaleInputMessages`
- `TestTunNodeDynamicMTUGrowthKeepsTrafficOnSameSource`
- `TestDefaultTunRebuildKeepsOutputsForReusedNativeDevice`
- `TestDefaultTunRebuildKeepsStablePublicWrapperAfterSourceReplacement`
- generated DefaultTun graphs at depths 1, 4, and 8, with IPv4, IPv6, TCP,
  UDP, native-source replacement, and repeated MTUs 1280, 1420, 1428, 1500,
  and 9000

## Earlier Lifecycle Changes

The current worktree includes these TUN lifecycle changes:

- `TunOutputCache` reuses a ready output for repeated sends.
- Each output source has one stable detached root. Link wrappers are nested
  below that root to reduce concurrent direct reads from one native TUN.
- A DefaultTun rebuild compares the source generation captured before the
  rebuild. Systems without this extension use captured native-file identity as
  a fallback.
- Gonnect output-root health makes a failed cache source unusable.
- TunSplitter observes input I/O errors, invalidates the failed generation,
  closes its frontends, and requests a replacement input.
- Stale splitter failure and retry messages use link and generation checks.
- TunJoiner uses the same policy for its default input and for every secondary
  link. The independent generations prevent a failure on one input from
  replacing another input or a newer attachment.

Tests were added for direct cache behavior, DefaultTun rebuild/restore, a
minimal DefaultTun path, and a more complex splitter/joiner path. Before the
upstream update, these changes made the failure less frequent but did not
handle an error created inside the detached root.

## Final Diagnostic Logging

The high-volume incident logs are removed. This includes output-cache
create/reuse/close/wait records, repeated unchanged DefaultTun configuration,
and routine splitter and joiner attach/detach records.

The remaining concise records are:

- initial and replaced DefaultTun native source, source generation, native
  file identity, MRO, MWO, and batch size; and
- terminal TunSplitter and TunJoiner input failures, with link and attachment
  generation.

Daemon stdout keeps microsecond timestamps. The capture script needs this
precision to correlate startup changes and failure events. The logs do not
include packet payloads.

## Real-Environment Capture Script

Use [scripts/capture-default-tun-flake.sh](scripts/capture-default-tun-flake.sh)
on the host where the problem occurs:

```sh
just capture-default-tun-flake
```

The Just recipe loads the ignored `.env` file. The script fails before build or
sudo setup if one of its required Amnesia, WireGuard, or Tailscale values is
absent or invalid.

The script does not use `set -x`. It prints only major progress messages. Full
output is stored below:

```text
artifacts/default-tun-flake/<UTC timestamp>-<PID>/
```

For each try, the script:

1. Captures the host network state and verifies that the baseline host curl
   works.
2. Restores the required secrets, Amnesia installation UUID, and Tailscale
   client caches into a new temporary daemon store.
3. Writes the embedded reproduction graph and starts the daemon.
4. Waits for the required graph nodes to remain operational, then waits
   an additional settle delay.
5. Sends fresh admin API requests through `FetchAddr`, `HttpClient`,
   `HttpClient A`, and `HttpClient B`.
6. Stops the try as inconclusive if an internal request fails.
7. Runs a verbose host curl and supporting DNS, route, ping, and trace probes.
8. Starts another try if the host curl succeeds.
9. If the host curl fails, captures the broken state, deletes DefaultTun,
   captures the state without it, creates a new DefaultTun, reconnects its TUN
   and DNS links, and repeats all checks.
10. Stops after it records the target failure and the result of the recreation.

At each key point it records addresses, links, all IPv4 and IPv6 route tables,
rules, DNS state, sockets, nftables, iptables, selected sysctls, graph and
worker snapshots, node menus, cached daemon logs, daemon file descriptors, and
Go goroutine stacks.

The script does not read `local/`. It creates a random private runtime directory
under `/run` for the config, file storage, and Unix admin socket. It also selects
an unused random web-admin port. Cleanup removes the runtime directory. The
script refuses to start if another `almagestd` process exists.

The artifact contains host network details, firewall rules, process details,
and node configuration menus. Review it before sharing it outside the project.

### Readiness-check correction (2026-09-11)

The first real run stopped after the five-minute readiness timeout. The daemon
was operational. The timeout file contained only these mismatches:

- The script expected a node named `DefaultTun`, but this graph contains the
  generated name `DefaultTun c3`. Its actual label was `ready`.
- The script expected `NetworkFirewall kA` to have the label `filtering 0
  rules`. Tailscale had installed four runtime rules, so the actual operational
  label was `filtering 4 rules`.

Thus, this timeout was a script defect. It was not a daemon startup defect.
The exact-name and exact-count checks had become stale after the earlier graph
edit and after dynamic firewall configuration.

The script now checks operational label patterns by node class. Generated node
names and dynamic counts do not affect readiness. A temporary startup error
still blocks readiness, but a node can recover: all required labels must match
for the configured stable interval. The checks also accept successful result
labels from `FetchAddr` and `HttpClient` after the first probe run.

DefaultTun recreation now locates the one `almagest/DefaultTun` node by class,
keeps its current name, and records both its old and new IDs. This is necessary
because the embedded node is named `DefaultTun c3`.

The corrected script was run in a privileged Docker network namespace with
copies of the same `local/` config and daemon file store. A normal try passed
readiness, all four internal probes, and the host probe. A second run used a
test HTTP endpoint that failed only the pre-recreation host probe. That run
completed the full path: it deleted node 135 (`DefaultTun c3`), created node
279 with the same name, reconnected it, passed recreated-graph readiness,
passed all four internal probes, and passed the final host probe. The Docker
test controlled the probe result; it did not reproduce the real flaky network
failure.

`DAEMON_BINARY`, `CLI_BINARY`, and `SKIP_BUILD=1` are available for isolated
runs with prebuilt binaries.

Useful timing overrides are:

```sh
INITIAL_DELAY=30 SETTLE_DELAY=30 OPERATIONAL_TIMEOUT=600 \
  just capture-default-tun-flake
```

`MAX_TRIES=0` and `MAX_INCONCLUSIVE=0` repeat without limits. These are the
defaults. Set `MAX_INCONCLUSIVE=3` if the script must stop after three
consecutive upstream or graph failures that are not the target bug.

## If the Failure Returns

First, find the reproduced try from `summary.txt`. Compare these directories:

```text
04-host-probe/
05-broken/
06-recreate/after-delete/
07-recreated-operational/
09-host-probe-after-recreate/
10-final/
```

Search its `daemon.log` for the new lifecycle records:

```sh
rg 'DefaultTun native source|TUN splitter input failed|TUN joiner .* input failed' \
  artifacts/default-tun-flake/<session>/try-*/daemon.log
```

Then use these decision points:

- If the first failed host curl follows a capacity error, verify that the same
  Gonnect root continues with a larger read buffer. If a terminal error occurs,
  verify that the cache discards the failed root and creates a new generation.
- If a newer source is fully attached but the host still fails, inspect the
  goroutine stacks for a stopped or duplicate native read.
- If the TUN pipeline stays active but route, rule, nftables, link, or resolved
  state differs before and after recreation, focus the fix in sysnet-linux
  DefaultTun reconciliation.
- If only name resolution fails, compare the systemd-resolved interface index,
  per-link DNS server, route domains, and default-route flag.
- If direct addressing fails before DNS is used, compare the native interface
  state, MTU, policy rules, packet marks, and the daemon's TUN read goroutines.

Do not add another lifecycle fix based only on an internal HTTP success or a
single warning. Use the host probe timestamp and the before/after system state
to identify the first divergent component.
