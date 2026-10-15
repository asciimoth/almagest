# Rootless Linux isolation probes

This manual research fixture measures isolation that is available to a daemon
role with a non-root identity and no capabilities. It is outside E2E case
discovery and does not start Almagest.

The runner derives a probe image from the existing E2E runtime image. It runs
the probe with UID/GID 65534, an empty capability set, `no_new_privs`, no
network, and no host mounts or host namespaces. One run uses Docker's default
seccomp profile. The second run changes only the seccomp profile to
`unconfined`. Each container has an experiment label, a 30-second time limit,
and cleanup on runner exit.

Run:

```sh
./e2e/experiments/linux_adaptive_isolation/rootless/run.sh
```

Results go to the ignored `artifacts/isolation-design/rootless/` directory.
The log records identity and capability state, seccomp installation, Landlock
allow and deny behavior, held-descriptor behavior, namespace attempts, cgroup
access, rlimit enforcement, and attacks between processes with the same UID.

The probe is diagnostic. A denied operation is usually an expected result, so
the probe reports each errno and exits zero if it completes all measurements.
The runner fails on a build error, timeout, or nonzero probe exit. Review the
reported allowed and denied controls instead of treating exit zero as proof of
a security boundary.

The default input images are `almagest-e2e:local` and
`golang:1.26.6-bookworm`; both must already exist locally. Use
`ROOTLESS_RUNTIME_IMAGE` and `ROOTLESS_BUILDER_IMAGE` to select other image
references. The runner records their resolved IDs and checks that the
references did not change during the build. For the recorded runtime:

```sh
ROOTLESS_RUNTIME_IMAGE=sha256:bca0ffaf64c933de2b8866cf8fb14a25a1cf6239a796b0ee1785fa3ee5039d2a \
  ./e2e/experiments/linux_adaptive_isolation/rootless/run.sh
```

`ROOTLESS_OUTPUT` overrides the output directory. `ROOTLESS_PROBE_IMAGE`
overrides the derived image tag, which defaults to
`almagest-rootless-isolation-probe:local` and remains available after the run.
Use separate output directories and probe tags for concurrent runs.
