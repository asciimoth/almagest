# Docker E2E Tests

The `e2e/` suite runs the real `almagestd` daemon in a Docker container and
drives it through the admin API with `almagest-cli`. These tests are intended
for behavior that depends on process startup, daemon config files, installed
programs, Linux networking, daemon logs, or command-line tools.

Run the regular cases:

```sh
just test-e2e
```

Run the regular and privileged cases:

```sh
just test-e2e-all
```

The full project gate runs regular E2E cases by default:

```sh
just test-check
```

Set `E2E_INCLUDE_PRIVILEGED=1` to include privileged Docker cases in
`just test-e2e`, `just test-check`, and `just check`. Set
`RUN_WINVM_TESTS=1` to include the native Windows VM gate in `just test-e2e`,
`just test-e2e-all`, and `just test-e2e-light`. This also includes the gate in
`just test-total`, `just test-total-light`, and `just check`. Both groups are
disabled by default. A developer `.env` file can enable them on a machine that
has the required Docker and VM setup.

```sh
E2E_INCLUDE_PRIVILEGED=1
RUN_WINVM_TESTS=1
```

Run all unit and e2e tests except cases marked as heavy:

```sh
just test-total-light
```

Run only the non-heavy e2e cases:

```sh
just test-e2e-light
```

When `RUN_WINVM_TESTS=1`, the native Windows VM gate and the Docker e2e suite
run in parallel and write to the same command output. The aggregate waits for
both gates before it creates the coverage report.

The Docker runner starts up to four cases in parallel. Set `E2E_JOBS` to change
the limit. Use `E2E_JOBS=1` for sequential execution. The runner shows each
case's direct or isolated privilege profile when it starts. It discards output from
passing cases and shows the grouped output from failed cases. Completion lines
include each case's run time. The final summary includes the full suite run
time and the 15 slowest cases. The runner reports all case failures after the
active cases finish. It randomizes the case order and prints the seed. Set
`E2E_RANDOM_SEED` to that value to reproduce the order from an earlier run.
The scheduler keeps the randomized relative order of runnable cases. If an
exclusive case cannot start, later regular cases can use the available job
slots. Exclusive cases run alone after the active regular cases finish.

The `just test-e2e`, `just test-total`, and `just check` paths run the daemon
with Go coverage instrumentation. This writes raw Go coverage data, a package
summary, a function report, and a text coverage profile under `e2e/coverage/`.
Set `E2E_COVERAGE_DIR` to write to another path. Set `E2E_COVERPKG` to change
the package set passed to `go build -coverpkg`.

The runner retries a failed Docker image build up to 10 times. The delay starts
at one second, doubles after each failure, and stops growing at 60 seconds. Set
`E2E_BUILD_ATTEMPTS`, `E2E_BUILD_RETRY_DELAY`, or
`E2E_BUILD_RETRY_MAX_DELAY` to change these values.

The runner labels each image with a hash of its source files and build
arguments. Later runs reuse the image without contacting an image registry when
these inputs did not change. Set `E2E_REBUILD=1` to force a Docker build. The
Dockerfile pins its base images by digest; update the tag and digest together
when you update a base image. Case-specific images reuse the common runtime
package layer. Keep packages that all cases need in that common layer.

Run selected cases:

```sh
./e2e/run.sh local_proxy_mock_http
```

## Layout

Each case lives in `e2e/cases/<case-name>/` and should include:

- `README.md`: the expected behavior, setup requirements, and assertions for
  review.
- `alm.hujson`: the initial daemon config for the case.
- `protected_alm.hujson`: the initial protected config. Use
  `%E2E_ADMIN_SOCK%` for the Unix admin socket path; the harness replaces it
  with the case runtime socket before startup.
- `setup.sh`: case-specific setup inside the container, such as network
  namespace setup, extra config files, or sanity checks for installed programs.
- `test.sh`: test logic. Source `/e2e/lib/common.sh`, call `start_daemon`, use
  `cli` for admin API actions and assertions, and use ordinary commands such as
  `curl`, `ip`, or `jq` for external checks.
- `requires-direct`: optional marker for a case that cannot use the production
  isolation profile. Examples include tests of direct startup arguments and
  experimental or debug system integrations. The harness starts these cases
  with `--no-isolation --experimental`.
- `also-direct`: optional marker for a small, isolation-compatible case that
  must run once with isolation and once in direct mode. Do not combine it with
  `requires-direct`.
- `requires-privileged`: optional selection marker for cases that need extra
  privileged Docker networking or host integration. Regular runs skip these
  cases. Set `E2E_INCLUDE_PRIVILEGED=1` to include them. All isolated cases use
  `--privileged` and `/dev/net/tun` because the production Linux profile must
  create namespaces, cgroups, and network resources. A direct case receives
  these options only when it has this marker. `E2E_DOCKER_RUN_ARGS` applies
  additional arguments to all selected cases but does not change selection.
- `heavy`: optional marker for slow or resource-intensive cases. The
  `just test-total-light` command skips these cases.
- `exclusive`: optional marker for resource-intensive cases that must run
  without other E2E cases. A blocked exclusive case does not prevent later
  regular cases from using available job slots.
- `docker-build-args`: optional list of `NAME=value` Docker build arguments,
  one per line. The runner builds a case-specific cached image when this file
  exists. Use this only when the case needs an incompatible system package
  variant, such as one of the mutually exclusive resolvconf implementations.
- `docker-run.sh`: optional executable case runner for a multi-container
  topology. The harness passes the case name as its only argument and provides
  `E2E_CASE_IMAGE`, `E2E_CASE_CIDFILE`, `E2E_CASE_COVERAGE`,
  `E2E_CASE_COVERAGE_DIR`, and `E2E_RUN_ID`. Label every container, network,
  and volume with `io.almagest.e2e.run=$E2E_RUN_ID` so the scheduler can remove
  resources after interrupted cleanup. The script must remove its service containers,
  network, and volumes on every exit. Use the main container CID file supplied
  by the harness so an interrupted suite can still remove that container.

Cases that start multiple daemons in one container must use
`start_isolated_daemon_instance` from `common.sh`. Each instance gets a private
isolation root and delegated cgroup, and its `admin hello` response must report
an active isolation profile.

The harness copies a case directory to `/run/almagest-e2e/<case-name>` before
running it so daemon config autosaves and generated files never modify the
source tree.

## Daemon Rules

Use `start_daemon` from `common.sh`. By default, it starts the production Linux
isolation composition with:

- `--debug-nodes`
- debug logging
- `--unsafe-config`
- an isolated Unix admin API socket from `protected_alm.hujson`
- the web admin API disabled
- separate runtime, config-writer, and command-helper identities
- trusted role executables and a static native launcher
- separate delegated cgroups and private role storage directories

The startup check reads `admin hello` and fails unless the daemon reports the
expected execution mode and an active isolation protocol. A `requires-direct`
case uses `--no-isolation --experimental` and must report direct mode instead.

Direct cases use temporary file storage by default. Isolated cases use the
runtime role's private storage. A case that must check daemon storage across
restarts can set `ALMAGEST_E2E_TMP_FILE_STORAGE=0` and
`ALMAGEST_E2E_FILE_STORAGE` before calling `start_daemon`. In isolation mode,
the harness bind-mounts that directory on the fixed runtime storage path. Use a
path accepted by daemon private-directory checks; on Linux, do not put
persistent storage under `/tmp`.

Assertions after daemon startup or config changes should use polling helpers
such as `retry`, `wait_log_contains`, and `wait_node_class_available`. Avoid
fixed sleeps except for short command-level timeouts. Node and log state can be
eventually consistent after graph changes.

## Adding A Case

1. Create `e2e/cases/<case-name>/`.
2. Add `README.md` documenting the setup, initial config, actions, and expected
   behavior.
3. Add `alm.hujson` and `protected_alm.hujson`.
4. Add executable `setup.sh` and `test.sh`.
5. Keep the case hermetic when possible. Add `requires-privileged` when a case
   needs extra privileged host behavior and document the requirement. Add
   `requires-direct` only when the tested feature is incompatible with the
   production isolation contract.

Use `E2E_DOCKER_RUN_ARGS` for additional Docker options during a local run:

```sh
E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun" ./e2e/run.sh <case-name>
```

Prefer admin API assertions through `almagest-cli`. Use daemon logs or external
commands when the behavior is only observable outside the API.

## Upstream Compatibility Cases

The following cases run daemon nodes against the external tools and complex
Linux environments used by Almagest's direct dependencies:

- `socks_third_party_compat`: curl, Gost, and Tor clients from the `socksgo`
  v0.4.12 compatibility suite.
- `wireguard_kernel_compat`: Linux kernel WireGuard, preshared-key rotation,
  and peer mutation from the WGO v0.7.0 compatibility suite.
- `wireguard_amneziawg_go_compat`: upstream `amneziawg-go` with a non-default
  AmneziaWG profile.
- `wireguard_amnezia_extensions`: concurrent vanilla and AmneziaWG profiles,
  plus the WGO packet-signature checks.
- `amnesia_selfhost_controller` and `amnesia_selfhost_managed`: WGO self-host
  import and gateway scenarios through Almagest Amnesia nodes.
- `sysnet_dns_debian_resolvconf`, `sysnet_dns_openresolv`, and
  `sysnet_dns_systemd_resolved`: DNS-manager auto-detection and system queries
  from the sysnet-linux v0.2.19 Docker suite.
- `default_tun_routing`, `default_tun_zero_prefix_routes`,
  `default_tun_dns_provider`,
  `killswitch_default_tun_exclude`, and `tun_splitter_process_routing`:
  sysnet-linux routing, DNS, strict mode, process marks, and lifecycle behavior.
- `out_network_migration`: repeated ethernet, no-network, and Wi-Fi
  transitions through simple OutNetwork and realistic DefaultTun graphs.
