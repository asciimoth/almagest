# Linux adaptive isolation capability experiment

This manual research fixture compares two ways to give a non-root process only
`CAP_NET_ADMIN`:

- a trusted bootstrap establishes a non-root network stage and an ambient
  capability before two ordinary executable transitions;
- a file capability gives the capability directly to a non-root broker analogue
  executable.

It also checks capability removal, `no_new_privs`, the Docker seccomp state, a
custom seccomp denial, TUN creation, dummy-interface and route changes, and the
lack of authority to write the root-owned DNS configuration file.

The file-capability probe also shows an important limit: the broker analogue can
issue the TUN ioctl itself, but an ordinary `exec` of `ip` has no capability
because the file-capability path has an empty ambient set. The ambient handoff
preserves the capability for such helper executions.

For the `no_new_privs` check, the container starts through the plain executable
with an empty permitted/effective/inheritable/ambient set but with
`CAP_NET_ADMIN` still in the bounding set. It cannot drop that bounding bit
without `CAP_SETPCAP`, and a later execution of the file-capability binary does
not acquire the capability.

Run it from the repository:

```sh
./e2e/experiments/linux_adaptive_isolation/capabilities/run.sh
```

The runner uses immutable builder and runtime image references. Each probe uses
its own Docker network and PID namespaces. It uses `--network none` and does not
use the host network, host PID namespace, Docker socket, privileged mode, or a
host filesystem mount. `/dev/net/tun` is the only passed device. Network changes
exist only for the life of a disposable container.

The output is a manual research artifact at
`artifacts/isolation-design/capabilities/capability-experiment.log`. This
fixture is not an automatic E2E case and does not qualify the full daemon.

Expected results:

- Both non-root network-stage executions have only `CAP_NET_ADMIN` in their
  inheritable, permitted, effective, bounding, and ambient sets. TUN creation,
  dummy-interface creation, and isolated route creation succeed. A write-open of
  `/etc/resolv.conf` fails because `CAP_NET_ADMIN` gives no file authority.
- The forked runtime clears its ambient, effective, permitted, and inheritable
  sets, sets `no_new_privs`, and executes its plain final stage. TUN and
  dummy-interface creation fail after that exec. The network parent keeps its
  capability.
- A runtime with an empty active set cannot remove an inherited bounding bit
  without `CAP_SETPCAP`. With `no_new_privs`, it cannot regain authority by
  executing the file-capability binary.
- A file capability permits the broker analogue to issue a direct TUN ioctl but
  does not propagate authority to an ordinary `ip` helper execution because the
  ambient set is empty.
- The custom external seccomp profile denies the TUN ioctl while dummy and route
  netlink operations still succeed. This proves that capability presence alone
  does not establish feature availability.

The bootstrap analogue starts as UID 0 with `CAP_SETUID`, `CAP_SETGID`,
`CAP_SETPCAP`, and `CAP_NET_ADMIN` because Docker cannot directly place an
ambient capability on a non-root entry process. Its child changes to UID/GID
65534, retains only `CAP_NET_ADMIN`, raises that capability into the ambient
set, and performs two executable transitions. The second stage is the network
broker analogue. It forks a runtime child, removes all active capability sets
from that child, and keeps `CAP_NET_ADMIN` in the non-root network parent. The
fixture does not claim that the UID 0 bootstrap is part of Almagest. The
deployment's service manager must supply this initial authority; the proposed
daemon does not install an elevation helper. The runner labels all derived
resources with a unique run ID and removes its containers and temporary image
tags on normal exit or interruption.

By default, the runner resolves the existing `almagest-e2e:local` tag to its
immutable image ID before it creates the run-specific build alias. Set
`CAP_RUNTIME_IMAGE` to an image name or ID to reproduce a recorded run. For
example, the original research run used:

```sh
CAP_RUNTIME_IMAGE=sha256:bca0ffaf64c933de2b8866cf8fb14a25a1cf6239a796b0ee1785fa3ee5039d2a \
  ./e2e/experiments/linux_adaptive_isolation/capabilities/run.sh
```

`CAP_PROBE_TIMEOUT` sets the per-container timeout and defaults to 30 seconds.
