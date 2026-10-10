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

Linux also requires explicit results for eBPF process filtering, kernel and
userspace process rules, marks on existing sockets, fork/exec/exit changes,
process-owner lookup, and DNS provider selection. A socket-only matcher test
cannot cover these results. Native checks must record the process backend's
retained authority, denial of arbitrary process operations requested by R, and
cleanup. A separate process helper or reduced N token is not required. These
requirements do not apply Linux-specific mechanisms to the Windows manifest.

Compare every feature with direct mode under equal launch authority. Do not
disable process rules, eBPF, or a selected DNS provider to make the isolated
profile pass. An actual native limitation must be observed in both modes;
an isolation restriction or missing adapter is not a native limitation.

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

Both manifests also require proof that backend roles execute no node code and
offer no generic host-operation API to R. The native backend is trusted code:
backend DNS parsing, process inspection, and backend-selected utility execution
are allowed in N under existing launch authority. These checks do not weaken
R's all-thread, filesystem, network, process, or inherited-handle restrictions.
Compromise of N itself is outside this node-containment claim.

The revised manifest replaces mandatory process-helper checks with
`process_backend_authority`, `process_backend_runtime_request_denial`, and
`process_backend_cleanup`. Both platforms add `backend_node_code_absent` and
`backend_generic_host_api_denied`. Earlier component evidence cannot satisfy
these by renaming results; producers must measure the actual revised launch
path. No production profile was qualified under the earlier manifest.

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

No complete daemon profile has passed the frozen manifest. Linux implements
trusted N preparation, restricted R/C/H, bounded role cgroup provisioning,
native backend comparison, and a restricted-runtime composition test. Separate
upstream DNS and p-mark fixtures provide additional component coverage; they
are not complete direct/isolated daemon parity evidence. See the
[Linux component reference](DAEMON_ISOLATION_LINUX_PROFILES.md).

Deployment identities, storage quotas, full daemon assembly, and integrated
recovery/hostile-runtime/performance evidence remain required. Windows still
needs trusted service N and complete native backend/deployment acceptance.
Missing results fail qualification; they are not successful skips.

Production isolated startup remains disabled. Checkpoint 32 must not enable
a profile before complete evidence passes this gate. Direct mode remains
available through `--no-isolation`, which experimental classes also require.

The validator has race-enabled tests and structured/raw JSON fuzz targets.
Passing those tests proves validator behavior, not acceptance of a platform.
Native producers run only in disposable Docker environments or a locked VM.
