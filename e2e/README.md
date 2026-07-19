# Docker E2E Tests

The `e2e/` suite runs the real `almagestd` daemon in a Docker container and
drives it through the admin API with `almagest-cli`. These tests are intended
for behavior that depends on process startup, daemon config files, installed
programs, Linux networking, daemon logs, or command-line tools.

Run all cases:

```sh
just test-e2e
```

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

The harness copies a case directory to `/tmp/almagest-e2e/<case-name>` before
running it so daemon config autosaves and generated files never modify the
source tree.

## Daemon Rules

Use `start_daemon` from `common.sh`. It always starts `almagestd` with:

- `--debug-nodes`
- debug logging
- `--unsafe-config`
- isolated temporary file storage
- an isolated Unix admin API socket from `protected_alm.hujson`
- the web admin API disabled

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
