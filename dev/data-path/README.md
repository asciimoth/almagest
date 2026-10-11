# Daemon isolation data-path experiment

This directory contains the development-only checkpoint 12 harness. It does
not connect an isolated data path to `almagestd` and does not change direct
mode.

Run the Linux stage only through Docker:

```sh
just data-path-experiment
```

The restricted runtime uses the checkpoint 10 UID, capability, namespace,
Landlock, and seccomp profile. The benchmark broker remains outside the
runtime network namespace and owns the native IPv4 and IPv6 TCP and connected
UDP sockets. The runtime has only one inherited bounded Unix sequence-packet
channel. Each relay sample includes a native loopback round trip in the broker.

The default smoke-sized run uses seven interleaved runs and 200 samples for
each packet size. Set `ALMAGEST_DATA_PATH_SAMPLES` to increase the sample count.
This stage is optional diagnostic and optimization evidence. No physical-host
performance acceptance run is required.

The command runs disabled, normal, and overloaded logging stages. It includes
native TCP, connected and unconnected UDP, native TUN, shared memory, eligible
direct handles, and matcher RPCs. It can run for a long time with acceptance
sample counts. Coordinate with other host users before starting it. Run the
Windows half with `just windows-data-path-experiment`.

Results are stored below `artifacts/data-path/`, which is ignored by git. See
[`DAEMON_ISOLATION_DATA_PATH_EXPERIMENT.md`](../../docs/DAEMON_ISOLATION_DATA_PATH_EXPERIMENT.md)
for scope and open qualification work.

## Hostile-client review

Run the checkpoint 13 authority audit only in Docker:

```sh
just data-path-security-experiment
```

It uses the checkpoint 10 restricted launcher and real container-owned sockets,
TUN, shared memory, and notifications. The C probe checks authority after N
closes its network handles, UDP destination override, shared-memory modification
and truncation, notification saturation, and resource lifetime through R exit.
The runner has a 90-second limit and requires every evidence field. A successful
run means the rejection observations were reproduced, not that isolation is
qualified. Do not execute `security-probe` on the host.

See the [checkpoint 13 decision](../../docs/DAEMON_ISOLATION_DATA_PATH_DECISION.md)
for the per-OS/resource matrix, Windows transfer evidence, rejected candidates,
and accepted performance baseline. The conservative relay
[contract](../../docs/DAEMON_ISOLATION_DATA_CHANNEL_CONTRACT.md) is frozen for
implementation. Production qualification remains pending.
