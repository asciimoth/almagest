# Complete isolation profile qualification

Checkpoint 30 adds the strict complete-profile evidence gate in
`bin/internal/isolation/qualification` and the development-only
`isolation-qualify` command. It does not enable isolated daemon startup. A
profile stays unavailable until one evidence record passes all checks. Direct
mode is unchanged.

## Evidence boundary

The gate has closed manifests for Linux amd64 and Windows Server 2022 amd64.
Each record must identify the exact profile, platform, source revision, launch
credential class, and protected-settings digest. Direct and isolated parity
runs must use the same credential class and protected settings. The baseline
runs cannot use optional restrictions.

Every manifest feature needs both a direct and isolated result. Both results
must pass. A native limitation is accepted only when both modes report the
same nonempty limitation identifier. Thus, a missing isolated feature cannot
be reported as native unavailability. Missing, duplicate, unknown, failed, or
skipped results reject the complete record.

The feature inventory includes regular and default TUN, native and userspace
listeners, UDP metadata, outbound TCP and UDP, application and host DNS, rule
completion and matching, commands and callbacks, both admin protocols, config
transactions, startup and live snapshot operations, logging, crash recovery,
partial native capability, and direct-mode process/transport absence.

Optional policy tests are a separate group. They cover default-route denial,
host-DNS denial, command denial, and resource limits. They cannot replace an
unrestricted parity result.

## Hostile runtime requirements

Every hostile-runtime observation must pass. The manifest covers arbitrary
runtime code, forged identity and callback origin, path escape, policy changes,
host handles, foreign resources, arbitrary commands, broker exhaustion, host
files and sockets, broker process or token access, malicious persisted config,
all-thread restrictions, inherited handles, and control progress during
overload.

Qualification also requires at least eight tested runtime threads, no
unexpected inherited handle, experimental classes absent, and a 30-minute
overload run. Platform manifests add native TUN flow, IPv4/IPv6 listeners,
route/firewall/DNS cleanup, matcher lifecycle, and final restriction checks.
The Windows manifest also requires the service network role and native flow
qualification. Cross-compilation is not native evidence.

The JSON decoder has a 256 KiB limit, rejects duplicate object members, rejects
unknown fields and trailing values, and validates exact manifest membership.
This prevents an evidence producer from using ambiguous input or silently
adding an unreviewed result type.

Use the explicit development entry point after the Docker or Windows VM runner
has produced one complete record:

```sh
go -C bin run ./cmd/isolation-qualify -list-profiles
go -C bin run ./cmd/isolation-qualify -evidence ../artifacts/isolation-qualification/evidence.json
```

Portable validation and fuzz checks are:

```sh
go -C bin test -race -cover ./internal/isolation/qualification
go -C bin test ./internal/isolation/qualification -run '^$' -fuzz '^FuzzEvidenceDecoder$' -fuzztime=30s -parallel=2
go -C bin test ./internal/isolation/qualification -run '^$' -fuzz '^FuzzEvidenceValidation$' -fuzztime=30s -parallel=2
```

Run native producers only in the disposable Docker environment or locked
Windows VM. Do not run native TUN, route, firewall, DNS, or hostile socket
probes on the host.

## Current qualification state

No profile is qualified by this change. Linux still needs the complete
default-TUN backend, process matching and host-DNS helper under the final
profile, plus deployment identities and cgroups. Windows still needs the
service network role, final-token TUN/DNS/matcher checks, and deployment
provisioning. These are profile failures, not skipped results. The evidence
command will reject a record that omits them.

Production startup continues to require explicit `--no-isolation`. A later
change must add the platform evidence and pass this gate before checkpoint 32
can make that profile available.

## Checkpoint 30 gate results, 2026-10-10

- The race-enabled package suite passed with 93.1 percent statement coverage.
  The focused `just check` passed formatting, spelling, secret scanning, panel
  generation, Linux and Windows amd64/arm64 lint, vet, harness tests, and the
  selected race-enabled qualification test.
- Two fuzz campaigns ran in a read-only, network-disabled Go 1.26.6 container.
  The structured evidence campaign completed 1,021,137 executions in 15
  seconds. The raw JSON campaign completed 352,471 executions in 16 seconds.
- The first raw JSON fuzz run used a 4 MiB parser ceiling and had poor progress
  on large mutations. The final gate uses a 256 KiB ceiling and repeats the
  campaign successfully. This is a qualification record, not a daemon config
  or snapshot limit.
- No native network operation ran on the host. No Windows VM test ran because
  there is no complete Windows profile to qualify. Linux and Windows remain
  unavailable for isolated production startup; this is an explicit failed
  qualification state, not a skipped passing result.
