# Docker E2E Tests

The `e2e/` suite runs the real `almagestd` daemon in a Docker container and
drives it through the admin API with `almagest-cli`. These tests are intended
for behavior that depends on process startup, daemon config files, installed
programs, Linux networking, daemon logs, or command-line tools.

Run all cases:

```sh
just test-e2e
```

The runner starts up to four cases in parallel. Set `E2E_JOBS` to change the
limit. Use `E2E_JOBS=1` for sequential execution. Each case's output stays
grouped, and the runner reports all case failures after the active cases
finish.

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
when you update a base image.

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
- `requires-privileged`: optional marker for cases that need privileged Docker
  networking. Default all-case runs skip these unless `E2E_INCLUDE_PRIVILEGED=1`
  or `E2E_DOCKER_RUN_ARGS` contains `--privileged`.
- `docker-build-args`: optional list of `NAME=value` Docker build arguments,
  one per line. The runner builds a case-specific cached image when this file
  exists. Use this only when the case needs an incompatible system package
  variant, such as one of the mutually exclusive resolvconf implementations.
- `docker-run.sh`: optional executable case runner for a multi-container
  topology. The harness passes the case name as its only argument and provides
  `E2E_CASE_IMAGE`, `E2E_CASE_CIDFILE`, `E2E_CASE_COVERAGE`, and
  `E2E_CASE_COVERAGE_DIR`. The script must remove its service containers,
  network, and volumes on every exit. Use the main container CID file supplied
  by the harness so an interrupted suite can still remove that container.

The harness copies a case directory to `/tmp/almagest-e2e/<case-name>` before
running it so daemon config autosaves and generated files never modify the
source tree.

## Daemon Rules

Use `start_daemon` from `common.sh`. It always starts `almagestd` with:

- `--debug-nodes`
- debug logging
- `--unsafe-config`
- an isolated Unix admin API socket from `protected_alm.hujson`
- the web admin API disabled

By default it also uses isolated temporary file storage. A case that must check
daemon storage across restarts can set `ALMAGEST_E2E_TMP_FILE_STORAGE=0` and
`ALMAGEST_E2E_FILE_STORAGE` before calling `start_daemon`. Use a path accepted
by daemon private-directory checks; on Linux, do not put persistent storage
under `/tmp`.

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
5. Keep the case hermetic when possible. If it needs elevated Docker features,
   add `requires-privileged`, document them, and run with
   `E2E_DOCKER_RUN_ARGS`, for example:

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
