# Restricted isolation command helper

Checkpoint 20 is implemented in `bin/internal/isolation/commandhelper`. It is a
component only. It is not connected to daemon startup and does not enable an
isolated production mode. Direct mode continues to use the existing command
runner and its legacy configured commands.

## Authority and channels

The helper validates and freezes protected command profiles at construction.
A runtime request contains only a known command ID, bounded slot values, and an
operation ID. It cannot select an executable, argument template, environment,
working directory, sandbox, timeout, output limit, or concurrency limit.
`AllowNodes` is checked again in H for each runtime request.

Runtime commands and network-broker callbacks use distinct authenticated
control channels and closed handlers. The runtime handler rejects
`RunCallback`; the callback handler rejects `RunNodeCommand`. Their receipt
namespaces are also separate. Thus a runtime cannot guess a callback operation
ID and read, conflict with, or replay its result.

Callback profiles bind one callback ID to one event and resource type. H
checks the session, graph epoch, resource ID, generation, type, and event. It
then derives command slots from the typed callback fields. There is no callback
slot map, callback label, or node identity supplied by R. The N-side caller is
still responsible for creating the callback only from its committed native
resource state.

## Fixed process profile

Each protected profile has one absolute canonical executable, argument
templates, exact environment, absolute working directory, sandbox ID, timeout,
output limit, concurrency limit, and slot schema. The helper does not inherit
the H environment. In particular, it does not use `PATH` for executable search.
Slot names, counts, individual byte lengths, aggregate bytes, required values,
full regular-expression matches, and option-like values are checked before
launch. Slots cannot occur in the executable, environment, working directory,
or sandbox ID.

Shell and interpreter executables are rejected. Unsupported sandbox IDs fail
helper construction with an instruction to change the protected profile or
use explicit direct mode. There is no generic DNS, BPF, script, file, D-Bus,
driver, or native-operation request. These restrictions on node-command H are
unchanged by the trusted-backend design. Backend DNS utilities and BPF operations
may run inside N without this helper; R cannot select their executables or send
scripts, raw BPF, or generic host requests. An optional separate backend helper
must have its own reviewed contract, not an escape through node-command H.

The executable and working-directory identity are captured at construction and
checked before each launch. Executable content is also checked by SHA-256. The
platform launcher and protected filesystem layout must prevent a replacement
between this check and process creation. Those process-token, namespace,
filesystem, and launcher controls remain checkpoints 23 through 25. The
component `ProcessLauncher` exists for tests inside an already restricted
process and is not a production sandbox.

## Results and limits

The helper retains at most 8,192 receipts. A repeated operation ID with the
same canonical input waits for or returns the first result. Reuse with different
input is a conflict. Completed receipts expire in insertion order; in-flight
receipts are never evicted. `GetResult` never starts a process.

A failure before process start is `rejected`. A normal zero or nonzero exit is
a completed command result and includes the exit code. Timeout, cancellation,
or output overflow after start is `uncertain`, because external effects can
already exist. The helper does not claim rollback or automatically replay an
ambiguous command. Peer loss cancels the handler context and wakes the other
side with a channel error.

Output is consumed but never returned across the control channel. The default
limit is 64 KiB and the compiled maximum is 1 MiB. The default timeout is 10
seconds and the maximum is one hour. Per-profile concurrency and the global
helper concurrency are both bounded. On Unix component-test launches use a
new process group so cancellation also stops descendants that retain output
descriptors.

## Test coverage

Portable tests cover fixed argv/environment/cwd selection, `AllowNodes`,
unknown IDs and slots, argument and environment injection, callback channel
separation, forged and stale callback state, receipt namespace separation,
concurrent duplicate requests, operation-ID conflict and expiry, profile and
global limits, cancellation before and after start, timeout, bounded output,
nonzero exit, executable replacement, unsupported sandboxes and interpreters,
control exchange, runtime peer loss, and helper death. The fuzz target checks
slot parsing, full-pattern enforcement, option rejection, substitution, and
fixed process inputs with arbitrary strings.

On 2026-10-09, the Linux race suite passed with 77.3% statement coverage. Two
15-second fuzz runs completed approximately 1.7 million total executions. The
ordinary-user Windows VM case passed all required tests in 74 seconds. Its
artifacts are in `artifacts/winvm/run-2ktbfqfP`.

Use these focused checks:

```sh
go -C bin test -race -cover ./internal/isolation/commandhelper
GOCACHE=/tmp/almagest-go-cache-fuzz go -C bin test ./internal/isolation/commandhelper -run '^$' -fuzz '^FuzzProfileSlotResolution$' -fuzztime=30s -parallel=2
just test-total winvm:m20::restricted-command-helper
```

The Windows selector supplies the required platform evidence. A cross-compiled
test binary alone does not replace the VM run.
