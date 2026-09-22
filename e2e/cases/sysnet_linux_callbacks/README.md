# sysnet_linux_callbacks

Exercises native sysnet-linux lifecycle command callbacks.

The case starts a regular `almagest/Tun`, a `almagest/DefaultTun`, and a small
killswitch daemon so every sysnet-linux callback command path is triggered. The
configured commands write marker lines under the e2e runtime directory and the
test verifies:

- callback commands run from protected `System.Linux.Callbacks`;
- text/template argv rendering works and empty rendered argv elements are
  dropped;
- configured env, UID, GID, sync execution, and timeout behavior are applied,
  including prompt process-tree termination at the timeout;
- daemon/API logs expose command names and result status without leaking command
  argv or stdout/stderr.

Run with privileged Docker networking:

```sh
E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun" ./e2e/run.sh sysnet_linux_callbacks
```
