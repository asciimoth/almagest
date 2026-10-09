# Windows isolation experiment

This directory describes the development-only checkpoint 11 harness. The
implementation uses the locked disposable Windows Server 2022 VM in
`dev/winvm`. It does not connect isolation to `almagestd` and does not change
direct mode.

Run the experiment from an x86-64 Linux host with the WinVM prerequisites:

```sh
just windows-isolation-experiment
```

The harness runs the same compiled Go launcher twice. The caller profile starts
through the standard `winvm` account. The service profile starts as Local
System through the private QEMU Guest Agent. Each launcher creates a unique
AppContainer, grants that identity access only to the probe executable and its
private storage, and creates an exact inherited pipe endpoint. It creates the
child suspended with process mitigations, assigns it to a one-process,
non-breakaway Job Object, and then resumes its initial thread.

The restricted child tests access during package initialization and during
normal Go execution. A failed restriction is a hard error. Raw JSON and
environment evidence is saved below `artifacts/winvm/run-*/` and checked again
on the Linux host.

See
[`DAEMON_ISOLATION_WINDOWS_EXPERIMENT.md`](../../docs/DAEMON_ISOLATION_WINDOWS_EXPERIMENT.md)
for results and compatibility gaps. This harness supports Windows Server
2022/amd64 only. It has no fallback to a restricted token or a weaker profile.
