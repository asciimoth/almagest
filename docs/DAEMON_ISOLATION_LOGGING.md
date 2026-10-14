# Bounded cross-process logging

Checkpoint 21 is implemented in `bin/internal/isolation/diagnostics`. The
released Linux composition keeps application/admin logging in R and forwards
role stdout/stderr through N-owned pipes into a bounded nonblocking output
queue. Thus, a blocked operator sink cannot stop R/C/H. The structured
diagnostic component remains available for integrations that need typed
cross-process records; production does not add a separate collector process.
Direct mode does not create logging IPC.

## Identity and records

The child wire record does not contain a role, launch ID, process instance, or
session ID. Trusted bootstrap binds these fields to one `Collector`. Thus, a
child cannot select or forge its process origin. Each producer assigns a
strictly increasing process-local sequence number. Child timestamps are for
display only and do not define an order between processes.

Records keep severity, source, message, optional resource generation, operation
ID, typed operation, policy decision, and outcome. The admin `LogEntry` shape
also carries the trusted origin and correlation fields. A collected record goes
directly to the runtime display sink. It does not enter a producer again, so it
cannot create a forwarding loop. A fatal record is data and does not call a
collector fatal or exit function.

The wire format is strict UTF-8 JSON in a four-byte length frame. A complete
frame is at most 8 KiB. The decoder rejects unknown, duplicate, incorrectly
cased, malformed, truncated, oversized, and trailing input before it delivers
a record. A diagnostic is not a control reply, resource event, instruction, or
lifecycle signal.

## Bounded delivery

Each producer has independent runtime-forwarding and operator-output queues.
Child operator output uses a second framed sink and a trusted `Collector`, so
the launcher assigns its origin. The network broker can instead use the local
operator sink because its launcher owns that identity and destination.
The default queue has 192 ordinary positions and 64 positions reserved for
warnings, errors, fatal records, and audit records. Audit records are records
with an operation, decision, or outcome. When a queue is full, priority records
replace ordinary records first. If all records have priority, the oldest
priority record is replaced. Logging never waits for queue space.

Each destination has independent written, write-failure, and per-severity drop
counters. The next successful record contains the bounded `DroppedBefore`
count. A collector also detects sequence gaps and exposes both observed gaps
and producer-reported drops. Thus, loss remains visible after output resumes.
The recovery journal is not part of this component and must stay separate.

A destination has one worker. A blocked runtime-facing destination cannot
block operator output, control work, traffic, or a logging caller. Shutdown
closes both queues and drains them for at most two seconds by default and five
seconds at the compiled ceiling. It returns on the earlier caller deadline. It
does not wait indefinitely for a sink that ignores cancellation.

## Output safety

Disabled levels are checked before message formatting. The producer and trusted
collector both sanitize fields. They replace control bytes, terminal escapes,
line separators, bidirectional format characters, and invalid UTF-8, and they
truncate on a UTF-8 boundary. Operator output contains exactly one bounded line
per record.

Bootstrap can provide literal secrets for exact redaction. A second boundary
redacts labeled passwords, tokens, authorization data, cookies, config and
packet bodies, command slots, argument and environment data, executable and
working-directory values, and raw process identifiers. Callers must still use
the typed correlation fields and must not put these bodies in messages. The
bounded `CaptureWriter` safely attributes panic and standard-error writes
without retaining their input.

Protected bootstrap owns operator destinations, severity thresholds, and audit
selection. The runtime display can apply its own filter, but it has no handle
to the operator queue or destination.

## Verification

The portable package tests cover trusted attribution, stopped runtime output,
blocked sinks, bounded shutdown, debug flood, priority replacement, visible
drops, receiver gaps, redaction, terminal escapes, multiline panic output,
remote fatal records, strict framing, forged origins, oversized records,
sequence replay, peer failure, display conversion, invalid configuration, and
invalid correlation.

Run the Linux checks in a network-disabled disposable Docker container:

```sh
go test -race -cover -timeout 90s ./bin/internal/isolation/diagnostics ./bin/internal/isolation/protocol
go test ./bin/internal/isolation/diagnostics -run '^$' -fuzz '^FuzzDiagnosticFrame$' -fuzztime=30s -parallel=2
go test ./bin/internal/isolation/diagnostics -run '^$' -fuzz '^FuzzSanitizeAndRedact$' -fuzztime=30s -parallel=2
```

The Windows Server 2022 amd64 ordinary-user selector is:

```sh
just test-total winvm:m21::cross-process-logging
```

The Windows case runs the portable diagnostics and protocol suites, including
all fuzz seed cases. It does not change host networking or require elevation.
Complete-daemon integration, sandbox bootstrap, and protected operator-sink
setup are complete for packaged Linux amd64. Logging performance is accepted.

### Checkpoint 21 results, 2026-10-09

- Race-enabled diagnostics and protocol tests passed in a network-disabled
  Docker container on Go 1.26.6. Diagnostics statement coverage was 85.5%, and
  protocol statement coverage was 98.9%.
- Both fuzz targets passed 30-second campaigns with two workers. The frame
  campaign completed more than 1.6 million executions. The sanitization and
  redaction campaign completed more than 960,000 executions.
- Focused `just check` passed in a network-disabled Docker container. It
  included formatting, spelling, secret scanning, lint, vet, Windows amd64 and
  arm64 static checks, and the selected race-enabled test. Optional Windows VM
  checks were disabled for this Docker run and run separately.
- The ordinary-user Windows package case passed on Windows Server 2022 amd64.
  Evidence is in `artifacts/winvm/run-38lA78eQ`. It validated all required tests
  and fuzz seeds without elevation.
- The production daemon dependency list does not contain the isolation
  diagnostics package. No host network setting was changed by these checks.
