# Direct mode refactor qualification

Status: implementation and platform matrix complete. The privileged Linux and
Windows VM matrices passed. The controlled performance comparison remains open.

This record qualifies the direct composition after daemon isolation checkpoints
2 through 7. The comparison baseline is
[`DIRECT_MODE_BASELINE.md`](DIRECT_MODE_BASELINE.md).

## Enforced checks

- `TestDirectModeExtractedPackageImportBoundaries` checks every production Go
  file in `daemonservices` and `daemonruntime`, including files for other
  operating systems. It rejects process launch, RPC and IPC-oriented
  serialization, daemon API, isolation client, and isolation transport imports.
- `TestBuildDebugReturnsOriginalNativeNetworks` checks that direct mode returns
  the backend outbound and local networks without a wrapper, retains
  `IsNative()`, and adds zero allocations to repeated native network lookup.
- The `direct_mode_qualification` E2E case checks the real daemon process tree
  and socket file descriptors. It repeats the checks after startup, config and
  graph replacement, and snapshot creation and restore. Only the public admin
  socket is permitted. Shutdown must remove the daemon process.
- Existing TUN and consumer tests retain native `File()`, batching, and native
  WireGuard paths. `BenchmarkAutoTunSplitterRouter` remains the allocation and
  packet-routing regression benchmark.

These checks distinguish public admin transport and configured external
commands from internal daemon role transport. The focused E2E case configures
no external command, so every child process is an error.

## Commands

Run the complete gate with:

```sh
just qualify-direct
```

This command enables the privileged Docker cases and Windows VM suites, runs
the full lint and test gate, and then runs the retained benchmark. If either
optional platform is unavailable or skipped, qualification stays open.

Focused development commands are:

```sh
just check go:bin/almagestd:TestDirectModeExtractedPackageImportBoundaries
just check go:bin/almagestd/daemonservices/localnetwork:TestBuildDebugReturnsOriginalNativeNetworks
just check e2e:direct_mode_qualification
```

## Performance observation

The retained benchmark was run on 2026-10-09 at source revision
`c86fc81cdebfbd2ddd09c9a321434a6e78b53042` before this qualification change.
The host used Go 1.26.7 on Linux 7.2.8, amd64, with an AMD Ryzen 5 5600H and 12
logical benchmark workers.

| Case | Time range | Bytes/op | Allocations/op |
| --- | ---: | ---: | ---: |
| IPv4 small match | 5.858-5.912 ns/op | 0 | 0 |
| IPv4 large late match | 15.81-15.96 ns/op | 0 | 0 |
| IPv4 large miss | 12.39-12.76 ns/op | 0 | 0 |
| IPv6 large late match | 28.59-29.90 ns/op | 0 | 0 |
| IPv4 equal-prefix source match | 12.99-13.26 ns/op | 0 | 0 |

The benchmark was repeated after the qualification changes on the same host.
The ranges were 5.873-6.380, 15.99-16.35, 11.28-11.73, 28.64-28.94, and
13.07-13.37 ns/op in table order. Every case again reported zero bytes and zero
allocations per operation. The qualification change contains tests and
documentation only, so the small timing differences are measurement variance,
not a changed production packet path.

The section 1 baseline used different hardware, so its absolute latency is not
a controlled before-and-after comparison. No serialization or IPC package is
present in the direct service composition. A same-host comparison with the
original source revision, including native I/O CPU, throughput, and tail
latency, is still required before final acceptance.

## Remaining acceptance work

- Repeat the performance baseline and this revision on the same host and record
  CPU, throughput, latency, memory, and allocation results.

## Qualification runs

On 2026-10-09, the focused import, native-path allocation, and direct-mode E2E
checks passed. The standalone privileged Linux matrix then passed all 77 Docker
E2E cases in 10 minutes 29 seconds. Its combined statement coverage was 75.7%.
The repository-wide `just check` passed lint, formatting, vet, vulnerability,
Go race/coverage, dependency regression, panel, and E2E harness checks before
the Windows failure stopped the concurrent matrix. Regular Go statement
coverage in that run was 81.5%, and all 99 panel tests passed.

The first Windows run failed `m2::portable-lib` because
`TestTunFirewallE2EVTunCopyHTTPAndUDP` sent one UDP request immediately after it
detached a firewall config link. Link processing is asynchronous, so the old
rule could still reject that request. The test then waited for a response to
the rejected request until its timeout. The test now waits for the firewall
label to confirm each rule transition before it tests traffic. Ten consecutive
focused Linux runs passed, and the change does not increase a timeout or change
production behavior. This was a test synchronization defect, not a third-party
library defect.

After the fix, `just winvm-test` passed on 2026-10-09. The ordinary suite passed
all 9 cases, the privileged suite passed all 20 cases, and the two-guest flow
gate validated 116 packet observations. This covers ordinary-user, restricted
token, and elevated paths. Evidence is in `artifacts/winvm/run-p2SnSnEE` and
`artifacts/winvm/run-flow-ZtB04U8C`. The initial failure and its isolated passing
rerun remain in `artifacts/winvm/run-vaVZFPke` and
`artifacts/winvm/run-ZPReZVSE`.
