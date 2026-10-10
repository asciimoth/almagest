# Isolated role supervision and host recovery

Checkpoint 26 adds `bin/internal/isolation/supervision`. The package coordinates
one already-started isolated process set and stores the broker's minimum trusted
host-effect records. It does not enable production isolated startup. The direct
daemon does not import it, create children, or open a recovery journal.

## Process ownership and readiness

Trusted bootstrap starts C, the required H roles, and R. It installs their final
platform restrictions before it constructs `Supervisor`; bootstrap then becomes
N. The supervisor accepts only already-started process handles. It has no start,
restart, executable, argument, or environment API. It rejects N as a child,
duplicate roles, and a process set without R.

Startup first reconciles the broker-private journal. It then waits for an
explicit ready result from every configured child. A child exit, closed ready
channel, reconciliation failure, cancellation, or timeout fails startup without
publishing readiness. Application and node readiness remain separate: R must
send its role-ready result only after its application initialization succeeds.

Each supervisor is single-use. A service restart constructs a complete new
process set and supervisor with a new protocol session. The supervisor has no
general launch API and does not replace one role in place. N may execute
backend-selected native utilities under the revised trusted-backend design;
this does not expose process launch or role replacement to R.

## Failure and bounded shutdown

R loss is fatal. N revokes the session's resources and stops the whole process
set. Normal shutdown and startup failure use the same revocation path. Revocation
is idempotent and bounded. No path gives R native host access as a fallback.

C and H loss marks that role failed and calls the composition's narrow degraded
function. C loss must make persistence unavailable. H loss must disable only its
fixed command, callback, DNS, or matcher function. A callback cannot block N's
later runtime-loss handling.

Shutdown stops R first so it cannot submit new operations, revokes native
session resources, and then stops H and C. Each stop has a fixed deadline. A
hung role is killed after the deadline. An ignored context can retain at most
one stop goroutine per configured role; role count and observation queues are
fixed. Any failed kill, timeout, or unproved revocation sets recovery-required
status. The service manager, not N, restarts the complete process set.

Status reports only ready/alive/failed facts for each role. It does not expose
child error text, protected policy, paths, identities, or journal contents.

## Durable ownership records

The journal lives at one bootstrap-selected absolute path in N's private
storage. Bootstrap must apply the checkpoint 23 path and ownership checks before
opening it. The file is bounded, versioned, checksummed, written through a
private mode-0600 temporary file, flushed, and atomically replaced. Journal
updates change memory only after durable replacement succeeds. A full or
unwritable journal therefore rejects a new operation before it can be treated
as recorded.

Before a native mutation, N stores a `pending` record with the protocol session,
resource type, and operation ID. Trusted native code changes it to `owned` with
an opaque stable identity as soon as identity capture succeeds. An unproved
operation or cleanup changes it to `uncertain`. N removes a record only after a
trusted identity inspection proves the effect absent.

A pending record has no native identity. Recovery leaves it visible and does
not guess ownership from its operation ID or a native name. For owned and
uncertain records, a platform reconciler compares the stored identity with the
live native identity:

- An absent effect is resolved.
- An owned effect is cleaned and inspected again before resolution.
- A foreign, replaced, or unknown effect is not changed and remains visible.
- An inspection, cleanup, persistence, or deadline failure remains uncertain.
- An effect marked `retain_on_failure`, such as a protected strict traffic
  block, remains installed until a policy-specific operation explicitly proves
  that removal is safe.

The identity string is interpreted only by trusted platform code. It must bind
the native object, such as a Linux interface index plus the backend's durable
ownership evidence or a Windows LUID and adapter GUID. A name or prefix alone
is invalid evidence. The journal does not authorize adoption or deletion.

## Verification

Package tests cover the startup readiness barrier, role exit before and after
readiness, broken readiness channels, R loss, C/H degradation, normal shutdown,
hung stop and recovery calls, failed kill, complete service restart, invalid
role sets, journal restart, durable transitions, capacity, tampering, write
failure, foreign replacement, strict-effect retention, partial cleanup, and
recovery timeouts. The journal decoder has a fuzz target for malformed,
truncated, duplicate, oversized, and checksum-invalid records.

These are portable component tests and do not change host networking. Final
Linux and Windows composition tests must connect qualified native identity
reconcilers and role processes, and run in Docker or the disposable Windows VM.
That platform qualification remains part of checkpoints 30 through 32.

### Checkpoint 26 results, 2026-10-10

- `go test -race ./internal/isolation/supervision` passed, as did a 20-run race
  repetition of the complete package (38.6 seconds).
- `FuzzJournalDecoder` ran for 10 seconds with two workers and completed 671,040
  executions without a failure.
- The package passed `go vet`, focused `golangci-lint`, and a Windows amd64 test
  cross-build.
- Selected `just check` passed for the runtime-loss and identity-recovery cases.
  This ran the repository static checks, Linux and Windows amd64/arm64 lint,
  vet, secret scan, panel build, and both selected tests with the race detector.
- The full `just check` ran all 77 Docker E2E cases successfully with 75.5%
  aggregate E2E statement coverage. All 19 ordinary and 23 privileged Windows
  VM cases passed in `artifacts/winvm/run-GRVQ8pLV`.
- The full gate then failed in its separate two-VM packet-flow gate before a
  project test program ran. The endpoint guest did not finish
  `flow-network.ps1` within the 60-second network-setup limit. A standalone
  retry failed at the same setup point. Evidence is in
  `artifacts/winvm/run-flow-dXmQQpPG` and
  `artifacts/winvm/run-flow-CGeBxa3v`. This package is not imported by the flow
  programs, but the failed gate remains recorded rather than counted as a pass.
- Native TUN creation is now journaled before the host mutation. It is promoted
  to an owned platform identity after creation and resolved only after native
  cleanup. Linux records the network namespace and interface index. Windows
  records the adapter LUID and GUID.
- `BrokerHost` runs identity reconciliation before readiness and after runtime
  revocation. An absent object is resolved. A still-present object remains
  visible for operator inspection because a generic reconciler cannot safely
  delete it from a reusable name or index.
- Complete-daemon process and recovery qualification remains open for the later
  Docker and Windows VM complete-composition gates.
