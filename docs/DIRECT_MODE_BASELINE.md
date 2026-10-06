# Direct mode baseline

This record is the comparison baseline for the daemon isolation work. It
captures the direct implementation before the runtime and host services are
refactored.

## Source and build

- Baseline source revision:
  `765792710fc25ed4b242934aa68bf7973aa5fd3f`
- Revision date: 2026-10-06T15:46:00+04:00
- Baseline worktree: clean
- Build command: `go build -o ./almagestd ./bin/almagestd/`
- Build tags and linker flags: none
- `GOFLAGS` and `GOEXPERIMENT`: empty
- CGO: enabled
- Go: `go1.26.7 linux/amd64`

The new `--no-isolation` flag selects this implementation without adding a
process, broker, IPC channel, sandbox, serialization layer, or packet-path
branch. Use it for all later direct-mode comparisons.

## Functional matrix

The `just check` gate is the reproducible functional baseline. It covers the
following matrix:

| Area | Evidence |
| --- | --- |
| Config parsing, protected settings, autosave, snapshots, storage, admin API, nodes, and shutdown | Go tests in `bin` and `lib` |
| Panel and API contracts | Panel JavaScript tests |
| Linux daemon and native network behavior | Docker E2E cases |
| Privileged Linux TUN, routing, DNS, and process cases | `E2E_INCLUDE_PRIVILEGED=1 just check` |
| Ordinary and privileged Windows daemon and native network behavior | `RUN_WINVM_TESTS=1 just check` |
| Invalid startup before storage or listener creation | `daemon_startup_validation` E2E case |

The full `just check` gate passed on 2026-10-06. This run completed all 76
Docker E2E cases, including the privileged Linux cases. It also completed the
ordinary and privileged Windows VM suites and validated 116 observations in
the Windows packet-flow gate. The combined statement coverage was 86.1%.

An ordinary-user Linux smoke run used a debug system, stdin config, temporary
file storage, and disabled admin listeners. It started and stopped normally as
one daemon process with no child process. The startup log reported
`Execution mode: direct (no sandbox)`.

Optional platform suites remain host-dependent for later runs. A skipped
optional suite is not a platform pass.

## Performance baseline

The retained native-path microbenchmark uses the pre-refactor library packet
router. Run it with:

```sh
go -C lib test ./tun -run '^$' \
  -bench '^BenchmarkAutoTunSplitterRouter$' \
  -benchmem -benchtime=500ms -count=3
```

Environment:

- CPU: 12th Gen Intel Core i7-12800H
- Kernel: Linux 7.2.8, x86-64, PREEMPT_DYNAMIC
- Logical benchmark workers: 20
- CPU scaling governor: `powersave`

Results:

| Case | Time range | Bytes/op | Allocations/op |
| --- | ---: | ---: | ---: |
| IPv4 small match | 9.370-11.02 ns/op | 0 | 0 |
| IPv4 large late match | 26.13-29.39 ns/op | 0 | 0 |
| IPv4 large miss | 18.40-19.58 ns/op | 0 | 0 |
| IPv6 large late match | 56.28-68.35 ns/op | 0 | 0 |
| IPv4 equal-prefix source match | 16.87-19.83 ns/op | 0 | 0 |

The benchmark passed on 2026-10-06. It measures a stable packet-routing hot
path and allocation count. It does not replace the native TUN, TCP, and UDP
experiments required before an isolated data path is selected. Existing
real-daemon memory and throughput observations are in
[`MEMORY_CONSUMPTION_REPORT.md`](MEMORY_CONSUMPTION_REPORT.md) and
[`MEMORY_NON_GVISOR_OVERHEAD_REPORT.md`](MEMORY_NON_GVISOR_OVERHEAD_REPORT.md);
those reports are supplemental because they predate this exact source revision.
