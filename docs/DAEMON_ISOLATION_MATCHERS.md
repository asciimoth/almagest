# Five-tuple matchers

Checkpoint 18 implements `CreateMatcher`, `Match`, and matcher
`CloseResource` in `bin/internal/isolation/network`. The runtime adapter in
`bin/internal/isolation/runtime` implements `sysnet.Matcher` and provides
`BuildMatcher` and a cancellable `MatchContext`. Direct mode is unchanged.
This component does not enable isolated daemon startup.

## Ownership and rule updates

N builds the native matcher from one bounded rule. The selected sysnet backend
validates its rule semantics and available authority. Optional protected policy
can deny rule types or operations. Policy checks account for the Windows
backend's rule-name whitespace normalization. Capability reports show those restrictions.
Creation uses the existing mutation lane, resource limits, operation receipts,
and session/graph ownership checks. Failed or canceled creation closes any
returned native matcher. Unproved cleanup reports recovery required.

Native matcher rules are immutable. A rule update creates a new matcher, then
closes the previous matcher through the existing control API. A failed new rule
does not remove the previous matcher. The new rule has a fresh opaque resource
ID; an old ID cannot acquire its authority. No generic process inspection or
new wire operation is needed. Close is idempotent. Graph release and peer loss
use the same cleanup path.

## Lookup behavior and limits

Each lookup accepts one TCP or UDP tuple with valid local and remote addresses
from the same IP family. IPv4-mapped addresses keep the backend's family
semantics. Unspecified addresses and zero ports remain valid lookup inputs.
Scoped addresses are rejected because the native `FlowTuple` has no zone field.
No observed packet, registration token, or socket lease is required.

The adapter keeps the tuple direction and original socket addresses. Thus
`sysnet.MatchConn` can reverse the endpoints of an accepted relay connection
and query its connecting peer. Queued outgoing TUN packets use their original
outgoing tuple, even if the socket has since changed or closed.

N calls the native matcher for every accepted lookup. It adds no result cache,
batching, or coalescing. Native owner caches and process identity checks stay in
the backend. Results contain only the match boolean and quality. The native
`Matcher` interface does not report per-call quality, so this field is
`unknown`; advertised per-family backend quality remains in capabilities.

A separate lane admits at most 64 native lookups per session, or the lower
protected concurrent-request limit. It rejects excess work immediately. The
control transport reserves 32 of its 128 request slots from data and matcher
traffic for other control work. A lookup has a five-second upper bound for its
caller. A native call that cannot be canceled retains its worker slot until it
returns. Repeated timeouts cannot create unlimited workers. Owner lookup does
not hold the network mutation lane.

Close rejects new lookups and cancels current callers before native teardown.
It waits for native calls to end before confirming cleanup. A stuck backend
leaves a bounded cleanup worker and an uncertain receipt, rather than a false
cleanup success. Each matcher has at most one native close worker, including
retries. A close timeout releases the mutation lane so another device can still
be removed. Completion rechecks session, graph epoch, resource reference,
and closed state before returning a result.

Errors use the transport's closed code set. Public missing-owner and ambiguous
owner sentinels, permission failures, unavailable/unsupported features, close,
cancellation, and timeout remain errors. Unknown native errors use `internal`.
In particular, the current Windows backend has private owner-error sentinels;
those use `internal` unless they wrap a public category. No process records,
PID diagnostics, executable paths, or native error strings cross the channel.
Errors and overload never become a successful nonmatch.

## Verification

Portable tests cover identity forgery, wrong types, stale generations and graph
epochs, receipt replay, native failures, partial creation, cancellation, close,
late completion, graph release, peer loss, rule replacement, policy limits,
and control capacity during overload. Scripted backend changes check that the
adapter does not reuse an old decision after owner or process state changes.
These are adapter freshness tests, not a claim that tests force OS PID reuse.

Native Docker/Windows VM comparisons cover IPv4/IPv6, TCP, UDP wildcard binds,
short connections, socket close, missing owners, queued outgoing packet tuples,
and `MatchConn`. Linux also compares shared UDP binds. Fuzz tests compare tuple
validation with native family semantics and check rule bounds and encoding.

Run portable and Linux native checks inside a disposable Docker container:

```sh
go test -race -cover -timeout 90s ./bin/internal/isolation/network ./bin/internal/isolation/runtime ./bin/internal/isolation/transport ./bin/internal/isolation/protocol ./bin/internal/isolation/policy
go test ./bin/internal/isolation/network -run '^$' -fuzz '^FuzzMatcherTuple$' -fuzztime=20s -parallel=2
go test ./bin/internal/isolation/network -run '^$' -fuzz '^FuzzMatcherRule$' -fuzztime=20s -parallel=2
go test -v ./bin/internal/isolation/network -run '^TestLinuxMatcherParity$' -count=7
```

The native Linux tests require Docker and need no host network namespace or
network administration capability. Run the Windows case through the VM harness:

```sh
just test-total winvm:m18::five-tuple-matchers
```

## Performance scope

The native parity test interleaves 256 direct and RPC UDP lookups. It records
median and p99 latency, plus the sequential lookup rate. That rate is the
maximum packet rate for a serial caller that requires one lookup per packet;
it is not measured TUN throughput. The test uses the production wire codec and
broker/runtime adapters over in-process pipe lanes. It does not measure
separate-process scheduling, full packet forwarding, or combined process CPU.

An initial Linux run on 2026-10-09 measured native median/p99 of 224/375 us and
RPC median/p99 of 517/877 us. Added median was 293 us and added p99 was 502 us.
The median exceeds the section 9 Linux interactive budget of 250 us; the p99
is below its 1 ms limit. This short component sample is not qualification.
Do not infer a complete-daemon budget pass from it or change the frozen budgets.
Full packet-path, process reuse/exec, sustained overload, and final restricted
profile measurements remain required in checkpoints 30 and 31.

Seven further Linux runs, without the race detector, gave these component
results on Linux 7.2.8 amd64 and an AMD Ryzen 5 5600H, using the local
`golang:1.26.6-bookworm` Docker image. Project checks used the mounted Go 1.26.8
toolchain. Times are microseconds. Each run contains 256 interleaved samples.

| Run | Native median / p99 | RPC median / p99 | Added median / p99 | Native / RPC lookups per second |
| --- | ---: | ---: | ---: | ---: |
| 1 | 228 / 418 | 550 / 1023 | 322 / 604 | 4180 / 1698 |
| 2 | 215 / 383 | 517 / 1024 | 302 / 642 | 4400 / 1782 |
| 3 | 216 / 369 | 520 / 1059 | 304 / 690 | 4262 / 1710 |
| 4 | 226 / 359 | 535 / 1063 | 309 / 704 | 4138 / 1662 |
| 5 | 213 / 363 | 520 / 1067 | 307 / 704 | 4383 / 1751 |
| 6 | 216 / 459 | 526 / 1038 | 310 / 579 | 4302 / 1776 |
| 7 | 215 / 375 | 509 / 944 | 293 / 569 | 4541 / 1867 |

All seven added medians exceed 250 us. Sequential lookup rate drops by
58.7–60.1%, which also prevents a packet-rate qualification claim. No cache or
batch optimization is included to hide that cost. The initial Windows VM run
reported zero medians because its clock did not resolve these short intervals;
its added p99 was about 10 ms. Those timing samples cannot qualify Windows.

On 2026-10-09 the affected packages passed Docker race tests. Tuple fuzzing
completed 57,067 executions and rule fuzzing completed 14,554 executions, each
with two workers and a 20-second budget. The focused `just check` passed Linux
lint/vet, Windows amd64/arm64 lint, and selected native and portable tests.
PowerShell lint was rerun successfully with `PSSCRIPTANALYZER_PATH` supplied to
the container. Native tests used disposable Docker or Windows VM networking.
The complete Windows matcher case passed in the ordinary-user VM profile;
retained evidence is in `artifacts/winvm/run-Yv6sGGnc`. A final portable
regression and fuzz seed also cover padded Windows rule names in denied policy.
The final rule-fuzz run completed another 45,911 executions with two workers
and a 10-second budget after that policy check was added.

## Rule validation and completion integration

The checkpoint 30 audit found that `check_rule` and `complete_rule` were in
protocol version 1 but absent from the broker dispatch table. They now call the
same native `sysnet.System` methods as direct mode. The runtime client provides
`CheckRule` and `CompleteRule`; neither method runs native process inspection
in R.

A typed context replaces the unused string field. It selects exactly one
matcher family/transport or routing family/mode/strictness. IPv4, IPv6, and
dual-family routing retain their distinct native capability contexts. This is
an internal contract correction before production isolation is enabled. An
older string-shaped request fails decoding; it is not treated as a default
context.

Validation returns the native capability revision and structured issue paths,
reason identifiers, states, and closed error categories. Native diagnostic
text stays in N. Unknown capability remains different from unsupported,
unavailable, and invalid input. Completion also has a distinct
`capability_unknown` wire error; `unknown` still means an unknown operation
receipt. Completion returns at most 128 suggestions and observes the protected
string and encoded-reply limits.

Both operations check session identity, graph epoch, exact context, denied rule
types, and protected operation restrictions before native work. Routing
contexts also observe default-route and strict-mode restrictions. Effective
rule capabilities report denied validation and completion operations. Native
work uses the bounded read lane. Cancellation before dispatch does not call the
backend. Cancellation, session revocation, or an epoch change prevents delivery
of a late result. A stuck backend retains the one native worker slot.

New tests cover those conditions, malformed frames and replies, failed native
operations, peer loss, and native validation-category preservation. The Linux
and Windows comparisons use each native backend's advertised rule inventory,
all four IPv4/IPv6 matcher contexts, and all eighteen routing contexts. These
are component comparisons through the production wire adapters; they do not
claim that a complete restricted daemon has passed checkpoint 30.

Run the native Windows comparison through the disposable VM:

```sh
just test-total winvm:m30::rule-control-parity
```

Run the new fuzz targets inside Docker:

```sh
go test ./bin/internal/isolation/network -run '^$' -fuzz '^FuzzRuleContext$' -fuzztime=30s -parallel=2
go test ./bin/internal/isolation/runtime -run '^$' -fuzz '^FuzzRuleValidationReply$' -fuzztime=30s -parallel=2
```
