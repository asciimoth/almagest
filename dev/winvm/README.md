# Disposable Windows VM harness

This Linux QEMU/KVM appliance targets Windows Server 2022/amd64 only. It does
not qualify Windows 11 or arm64. Use separate Windows 11 test environments,
including a native arm64 host, for those targets. An amd64 guest or an emulated
application cannot provide arm64 qualification.

The image, QGA client, and lifecycle helpers are adapted from
`asciimoth/sysnet-windows/dev/winvm` at revision
`a056c520f0faf966f54dd90ef63b15d9aa7382ec`. This adaptation changes the project
paths, source packaging, guest toolchain, dependency provisioning, SSH key
verification, and run/test orchestration. The upstream harness uses GPL-3.0;
its license is retained in [COPYING](COPYING) for this derived harness.
The Windows, VirtIO, and OpenSSH locks use
the integration plan's reference inputs. Go is locked to 1.26.6; its hash is
from the Go download manifest. No sibling checkout is needed. This baseline
does not install Wintun or the split driver. Later privileged suites must
provision the deployment contract's locked dependencies in disposable guests.
Do not run privileged networking tests on a normal workstation or shared host.

## Host setup

Use an x86_64 Linux host with writable `/dev/kvm`, 8 GiB of available memory,
and at least 90 GiB of free cache storage. Install QEMU, OVMF, xorriso, OpenSSH
client tools, Python 3, jq, curl, Git, tar, flock, coreutils, Go 1.26.6 or newer,
Node.js, and npm. The Nix development shell supplies the VM packages on
x86_64 Linux. Set `WINVM_OVMF_CODE` and `WINVM_OVMF_VARS` if firmware is not in
one of the paths listed in `common.sh`. Use a matching firmware pair.

Copy `env.example` to `env` and set absolute paths to the Windows and VirtIO
ISOs. Supply legally obtained media with the exact hashes in `image-lock.json`.
Media are not downloaded. Go and OpenSSH archives are downloaded over HTTPS and
checked against their locks during image creation. Keep the private cache,
media, and secrets outside the repository. The default cache is
`$XDG_CACHE_HOME/almagest/winvm`, or `$HOME/.cache/almagest/winvm`.

From the repository root:

```sh
just test-winvm-harness
just winvm-doctor
just winvm-image
just winvm-smoke
```

`doctor.sh --validate` checks configuration without VM prerequisites.
`doctor.sh --print-input-hashes` helps identify local media. Wrong hashes and
missing prerequisites fail; they are not skips.

Image creation performs unattended installation, installs QEMU Guest Agent
and OpenSSH, and creates a standard account named `winvm`. Windows Firewall
stays enabled; one guest rule permits SSH. Authentication uses a private cache
key. The image builder obtains the SSH host key through its private QGA socket
and then verifies SSH against that key. Password authentication is disabled.
QGA has SYSTEM privileges and is available only through a private host socket.

## Isolation, source identity, and evidence

Each run has a unique `artifacts/winvm/run-*` directory, qcow2 overlay, firmware
copy, sockets, and locked loopback SSH port. A shared cache lock serializes VM
runs and image creation to limit memory use. Per-image and per-run locks also
protect recovery runs. Port contention fails before a run can claim success.
Readiness requires two matching provisioning-token and Windows-boot-time
observations. Provisioning, guest commands, transfers, and shutdown have limits.

`source.tar` contains current tracked files and eligible untracked files, with
tracked deletions omitted. Clean and dirty trees use the same policy.
`source.json` records the revision, dirty state, archive SHA-256, and each
included file hash. Archive metadata is deterministic. Source symlinks fail
packaging because Windows extraction must not follow links outside staging.

Packaging excludes Git metadata, local environment files, conventional secret
directories and key files, VM media/disks/firmware, caches, artifacts, and ignored
build output. Arbitrarily named credentials cannot be detected: keep them outside
the repository. The full policy is in `package-worktree.py` and applies even to
tracked files. Five named public certificate fixtures are included; the packager
rejects private-key data in these fixtures. Review `source.json` when adding new
local inputs.

The host extracts that archive into the run directory and regenerates the Go
workspace, certificate catalog, and embedded panel there. It uses the archived
catalog data and npm lock. `generated.sha256` and `generator-versions.txt` record
the generated files and generator tools. `payload.sha256` identifies the complete
guest payload; the guest verifies that hash before extraction. The checkout's
ignored workspace and assets are not used or changed.

The smoke run verifies transport, standard-user identity, OS, architecture,
toolchain, and artifact retrieval. It records `testExecution: not-run`:
**this is not the Windows test-suite gate or a support qualification.**
Step 5 adds ordinary and privileged test-suite execution. The host-only harness
tests run through both `just lint` and `just test-total` without starting a VM.

## Failure and recovery

Runs retain disk, firmware, serial and QEMU logs, console output, source and
payload hashes, image identity, and `run.json`. Failed guest runs also attempt
bounded QGA diagnostics. Missing, invalid, or failed guest artifacts fail the
run. The guest must shut down within the configured limit; otherwise the run
fails and the host sends TERM, then KILL, with separate limits. A process that
survives KILL is recorded and blocks subsequent runs until that exact process
has stopped. There is no unbounded process wait.

The failure message prints this recovery command:

```sh
just winvm-shell /absolute/path/to/artifacts/winvm/run-XXXXXXXX
```

Recovery reuses that run's overlay and firmware and checks its base image and
SSH identity. Do not run recovery while the original process is still active.
Failed installation work remains in the private cache with its provisioning
ISO and logs; `just winvm-image` retries installation in a new directory.
These files can contain generated credentials. Successful image creation removes
its temporary provisioning media. All run directories, including successful
runs, are retained for inspection; remove an exact run directory only after its
QEMU process has stopped and its artifacts are no longer needed.

Native provisioning and smoke execution require the locked media and host tools.
Host-only tests do not prove that native provisioning succeeded.
