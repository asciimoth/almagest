# Daemon isolation Windows experiment

Status: checkpoint 11 implementation and Windows Server 2022/amd64
qualification are complete. This is development evidence, not a production
isolation profile. The production daemon remains unchanged and requires
`--no-isolation`.

## Scope and reproduction

The harness is in [`dev/windows-isolation`](../dev/windows-isolation/README.md).
Run it in the locked disposable Windows VM with:

```sh
just windows-isolation-experiment
```

The experiment uses the actual Go launcher and restricted probe. It runs two
profiles:

- `caller` starts from the standard `ALM-WINVM\winvm` account without
  elevation;
- `service` starts from Local System through the VM's private QEMU Guest Agent
  and models an installed service launcher.

The launcher creates a restricted primary token with maximum privileges
disabled and combines it with a unique AppContainer that has no capabilities.
It grants that AppContainer read and execute access to the probe and full
access to one private storage directory. It does not grant access to the
bootstrap-only host input.

The launcher creates the child suspended with an exact two-handle inheritance
list. The handles are a result pipe and a diagnostic pipe. It verifies that a
separate inheritable sentinel handle is absent from the suspended child's
handle table. It then assigns the child to a one-process Job Object with
kill-on-close and UI restrictions. The job does not permit breakaway. The
launcher resumes the initial thread only after these operations succeed.

The host validates the two JSON results after it retrieves them from the VM.
A missing profile, check, environment record, or expected denial fails the
experiment.

## Qualified result

The accepted run used source revision
`ce85d6731d8a8497a0c5dd329e66bd137e75f7fe` plus the checkpoint 11 working
change, Windows Server 2022 build 20348 amd64, and Go 1.26.6. The locked base
image key was
`1b48a0a0ded885792a9f53779043ddc61ecc4a02c22ec9415df1a235bccc2326`.
The source archive SHA-256 was
`08c10a05b55b6554132eeec63b5476e89debb6819eb24af2b2ca21480ad07a2b`.

The command was `just windows-isolation-experiment`. Evidence is in
`artifacts/winvm/run-vu1ISBIr`. The run completed without recovery and the
host evidence validator passed.

Both restricted children verified these properties:

- the token is an AppContainer token, is a restricted token, and has Low or
  lower integrity;
- DEP is enabled, the process is in the configured Job Object, and private
  storage is writable;
- the result pipe works and the inheritable sentinel is not present;
- the protected host input cannot be read during package initialization or
  from `main`;
- the child cannot open the launcher's token, start a breakaway process, open
  the service-control manager for driver creation, or create an unrestricted
  native network connection;
- the child cannot open Wintun or duplicate its own Winsock socket into the
  launcher process.

The caller profile used no UAC prompt, elevation, installed privileged
service, or connection to the service profile. The service profile is a
separate launch-authority experiment. It is not a way for the caller profile
to acquire authority.

## Native-resource and mitigation results

The no-capability AppContainer correctly prevents R from opening Wintun and
from creating unrestricted Winsock connections. It also prevents R from using
`WSADuplicateSocket` to inject a socket into N. Therefore an isolated adapter
must not create native sockets or Wintun sessions as a fallback.

This checkpoint does not approve a direct Winsock or Wintun data handle.
Broker-to-runtime Winsock duplication and Wintun session/ring transfer remain
data-channel candidates for checkpoints 12 and 13. Any selected direct handle
must have a separate authority and revocation review. The current Wintun Go API
does not expose a supported transferable session/ring contract.

The qualified launcher applies DEP and SEHOP at process creation and verifies
the effective DEP policy in the child. A broader experimental policy that also
requested forced relocation, extension-point disablement, and Control Flow
Guard was rejected by `CreateProcess` with `ERROR_INVALID_PARAMETER` on this
binary and OS. Those policies are not claimed. Additional creation-time
policies need individual compatibility tests with the shipped Go binary before
they can be enabled. AppContainer, restricted token, Low integrity, exact
handles, DEP/SEHOP, suspended launch, and Job Object restrictions are enforced
before package initialization.

## Limits and follow-up

- This run qualifies Windows Server 2022 build 20348 on amd64. It does not
  qualify Windows 11 or arm64.
- AppContainer still permits the Windows and executable files required to load
  the process. The host-file result applies to inputs whose ACL does not grant
  AppContainer access. Bootstrap must grant R only its reviewed runtime files
  and private storage.
- Anonymous inherited pipes prove exact private IPC access for this
  checkpoint 11 result. Checkpoint 14 now implements the connected-pipe
  transport, framing, peer binding, and cancellation. Its
  [separate evidence](DAEMON_ISOLATION_CONTROL_TRANSPORT.md) includes new caller
  and service runs with an inherited duplex connected pipe.
- The one-process job intentionally prevents child creation. A future runtime
  that needs a reviewed helper must use a separate launcher-owned role and not
  relax R's job.
- The experiment creates and deletes its AppContainer profile. It does not
  install a daemon service, driver, production account, or persistent policy.
- Failed development iterations are retained in earlier `artifacts/winvm`
  directories. They found invalid mitigation combinations, a stale-handle
  cleanup defect, and unsafe invalid-handle probes. The accepted run includes
  all corrections.

These limits do not reduce direct-mode features and do not cause elevation,
an unrestricted helper, or a hidden native fallback.

## Project gate

The complete `just check` gate passed on 2026-10-09 with the optional Windows
and privileged Linux paths enabled by the development environment. It included
formatting, static analysis, secret scanning, vet, regular Go tests, all 99
panel tests, all 77 Docker E2E cases, all 9 ordinary and 20 privileged Windows
VM cases, and all 116 Windows packet-flow observations. Combined statement
coverage was 84.7%. The native Windows evidence is in
`artifacts/winvm/run-Oiye5WwU`; packet-flow evidence is in
`artifacts/winvm/run-flow-FJKaeLaM`.

After the gate, the qualified DEP/SEHOP startup attribute was restored to the
experiment launcher and rechecked with Windows amd64 and arm64 lint, the
focused host evidence tests, and both live isolation profiles. Production code
and the direct daemon path were not changed by that final experiment-only
adjustment.
