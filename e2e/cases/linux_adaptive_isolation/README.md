# Adaptive Linux isolation

This case exercises the packaged `linux-user-v1` profile as an ordinary user. It
starts the daemon directly because the common E2E helper provisions the
root-only full profile.

The case verifies:

- explicit automatic selection with no capabilities;
- explicit automatic selection with only ambient `CAP_NET_ADMIN` when the
  container's `setpriv` supports ambient capabilities;
- reduced-profile status, credentials, capability sets, resource status, and
  startup warnings;
- fail-closed strict selection for a non-root caller;
- explicit direct mode through `--isolation=off` and `--no-isolation`;
- rejection of conflicting and unknown policy options;
- rejection of implicit state roots, unavailable per-role cgroup requests, and
  isolated temporary storage;
- runtime and config-writer applied credentials, capability sets, and
  mechanisms.

The custom runner adds only `CAP_NET_ADMIN` to the disposable container so it
can create the ambient-capability launch condition. Each daemon launch drops all
capabilities that are not part of its scenario. The container uses Docker's
private `none` network. It does not use host networking, the host PID namespace,
host system mounts, or the Docker socket.

`requires-direct` prevents the standard harness from adding the privileged
full-profile container options. It does not describe the daemon modes tested
inside the case. The custom runner rejects `E2E_DOCKER_RUN_ARGS` because those
arguments could override its namespace and capability boundary.
