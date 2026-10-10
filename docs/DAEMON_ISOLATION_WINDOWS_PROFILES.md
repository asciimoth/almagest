# Windows role profiles

Checkpoint 25 is complete for the Windows Server 2022 build 20348/amd64 profile
components in `bin/internal/isolation/windowsprofile`. The package provides
role launch restrictions, a trusted caller/service N launch, and persistent
R/C/H identity provisioning. It does not enable isolated daemon startup. The
direct daemon and the public admin pipe are unchanged.

## Revised production target

The 2026-10-10 architecture revision keeps R in its full AppContainer profile
and treats sysnet-windows in N as trusted host code. N may retain the caller's
or service's launch token and the access required for Wintun, DNS, WFP, owner
matching, native services, and the installed split driver. Backend DNS parsing
and native control may remain together. Extra DNS/driver helpers and a reduced
service token are not production prerequisites.

N retains the exact caller or service credential. The launcher verifies the
child's authentication identity, user, groups, privileges, owner, primary
group, integrity, session, elevation, restrictions, virtualization, UI access,
and AppContainer state before resume. It does not request elevation or attach
to another service. R's token, job, network, child-process, and handle
restrictions and C/node-command H restrictions are unchanged. Trusted paths,
private IPC, typed ownership checks, and no runtime-selected programs remain
mandatory.

## Existing component launch boundary

Trusted bootstrap supplies a fixed executable, role, storage directory, bounded
arguments, and one to four private pipes. There is no inherited environment,
executable search, shell dispatch, elevation, service attachment, or fallback.
The first pipe is the bootstrap channel. Each pipe is duplicated for this launch;
the caller's handles and inheritance flags remain unchanged. Both file type and
pipe information are checked. Only client ends are accepted, so a role cannot
receive a pipe server's client-impersonation authority. A Winsock socket, native TUN/split-driver handle,
token, file, process, or job cannot enter the inherited list.

R, C, and H use distinct AppContainers with no capabilities. The primary token
comes from the launcher, has maximum privileges removed, and makes the
Administrators SID deny-only. The suspended
child's token is checked for its exact AppContainer SID, Low integrity, absent
capabilities, absent elevation/UI access, disabled administrator membership,
and removed privileges. Only the traversal
privilege can remain. Restrictions apply before Go package initialization and
to all threads.

N runs outside AppContainer with its unchanged launch credential. Its job
allows at most 16 processes so trusted backend-selected helpers can run while
breakaway remains denied. R, C, and H remain one-process jobs. Driver
installation and updates remain explicit deployment operations; the launcher
does not install a driver, request elevation, or provide a generic helper API.

Every role in this component has a separate kill-on-close, no-breakaway job
with UI restrictions, a process memory ceiling, and a CPU hard cap. R, C, and H
allow one active process; N allows 16. The defaults are 512 MiB and 25 percent
CPU. All setup and token checks must succeed before the first thread resumes. A
failure terminates the suspended process. `Wait` supports cancellation;
`Close` kills the job and releases its handles. Only trusted bootstrap retains
process and job handles.

Creation applies DEP, SEHOP, strict handle checking, remote-image and Low-image
denial, and System32 preference. Strict-handle and image policies are read back
before resume. The flags follow Microsoft's
[process-creation attribute contract](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-updateprocthreadattribute).
This does not claim CFG, dynamic-code prevention, or LPAC. A rejected mitigation
fails launch; the launcher never removes a flag and retries.

## Objects and provisioning

`NewIdentity` creates a temporary, role-specific AppContainer and never adopts
an existing profile. `ProvisionDeployment` creates persistent, distinct R/C/H
profiles, new role directories with protected DACLs and Low integrity labels,
and exact read/execute grants on their trusted executables. It rolls back new
profiles and directories on failure. `OpenDeployment` derives the expected
SIDs, requires the registered profile directories, and revalidates every
executable and storage ACL, including the exact role read/execute grant.
Partial provisioning reports rollback failures instead of hiding residual
profiles or ACLs. `Remove` explicitly removes profiles but never deletes
application or config data; it revokes the obsolete role SID from the storage
and executable DACLs before profile deletion.

The launcher opens each path component, including the volume root, without
write/delete sharing and retains those handles until resume. It checks owners,
DACLs, object types, reparse points, and link counts. It rejects UNC/device
paths, alternate streams, relative components, DOS names, short-name aliases,
trailing dots/spaces, and control characters. The executable directory cannot
be writable by another identity. The working directory is that protected
directory, not writable storage. Storage must grant the exact role data access,
without broad read grants or permission to change its ACL, and must have the
Low/no-write-up label. The environment contains only the OS Windows directory
and fixed role/channel/storage values. `LOCALAPPDATA` comes from the source
token's OS known-folder lookup; `TEMP` and `TMP` point to private storage.
These values are required by AppContainer creation. The block is sorted and
does not include `PATH` or caller-selected loader variables.

Private pipes remain distinct from the checkpoint 22 public admin gate. That
gate retains its existing client identity checks and never passes an
impersonation token to R. No profile adds an AppContainer network capability or
loopback exemption to make an admin connection work.

## Verification and remaining acceptance

Portable tests cover valid and invalid limits, feature and OS rejection, and
Windows path aliases. `FuzzPlan` and `FuzzPath` exercise arbitrary malformed
input. Native tests are registered in both Windows VM phases:

```sh
just test-total winvm:m25::windows-role-profiles-caller winvm:m25::windows-role-profiles-service
# Equivalent convenience command:
just windows-isolation-profiles
```

The native tests cover R/C/H and caller/service N, eight simultaneous locked
worker threads, private and foreign storage, broker process access, Low DLL
loading, direct Winsock denial, N TCP/UDP, native outbound DNS, local name
resolution, owner matching, backend-selected helper execution, regular Wintun,
split default Wintun, WFP policy, managed DNS, effective job limits,
inherited-handle exclusion, startup fault cleanup, invalid handle types, unsafe
ACLs, hard links, path replacement, cancellation, junctions, peer closure,
repeated shutdown, and persistent deployment reopen/removal. With the
provisioned split driver active, R/C/H must also fail to open its control
device. A reachable native listener before and after each probe, plus
accepted-connection counts, checks that AppContainer loopback timeouts are
actual denials. R/C/H child creation and N breakaway must report access denied.

The platform gate accepts only Windows Server 2022 build 20348 on amd64.
Cross-compilation does not qualify Windows 11 or arm64. H here is one fixed
executable with no children or native network access; profiles that need those
rights require separate qualification. C's fixed-target transaction behavior
remains in `configwriter`. Complete role composition, recovery, hostile-runtime
evidence, performance, packaging, and the default-mode switch remain later
checkpoints.

## Test record

Checks run in Docker or the locked disposable Windows VM. No host network
configuration was changed.

- Docker: focused `just check` with the profile plan/path tests and the direct
  mode selection/import-boundary tests. This includes the normal project
  formatting, lint, secret scan, shellcheck and vet steps, including Windows
  amd64/arm64 lint. The optional VM suites run separately below.
- Docker: all 41 Windows VM harness unit tests passed. Docker logs are retained
  in `artifacts/windows-profiles/checkpoint25/`.
- Docker, Go 1.26.6: the portable package passed with the race detector and
  100 percent statement coverage. Two 30-second fuzz campaigns passed:
  `FuzzPlan` executed 2,798,156 inputs and `FuzzPath` executed 2,804,958 inputs.
- The final native gate passed in `artifacts/winvm/run-oiiLHRkW` on Windows
  Server 2022 build 20348/amd64. Both registered cases ran without skips:
  `ALM-WINVM\winvm` and `NT AUTHORITY\SYSTEM`. Native package coverage was
  82.2 percent for the caller suite and 81.7 percent for the service-derived
  suite. The evidence validator passed, all artifacts were collected, and the
  VM shut down gracefully without recovery. This qualifies the tested component
  restrictions, not the remaining service-N/backend/deployment composition.

The revised trusted-N gate passed in `artifacts/winvm/run-8BLTPqH7` on Windows
Server 2022 build 20348/amd64. Both registered cases ran without skips as
`ALM-WINVM\winvm` and `NT AUTHORITY\SYSTEM`, produced 82.3 percent package
statement coverage, passed evidence validation, and shut the VM down
gracefully. The caller profile preserved its native Wintun permission denial
without losing socket, DNS, matcher, or helper features. The service profile
created and closed regular and split-default Wintun resources, WFP policy, and
managed DNS under its unchanged final token. Persistent deployment
provision/open/remove also passed under both authorities, including executable
grant readback, rollback, and reprovision after a partial provisioning failure.

Three 10-second portable fuzz campaigns passed before the native gate:
`FuzzPlan` completed 895,303 executions, `FuzzPath` completed 921,082, and
`FuzzDeploymentPlan` completed 889,396. Windows amd64 and arm64 lint and test
cross-builds passed. Failed VM iterations are retained separately; they found
invalid fixed-size token queries, a wrong AppContainer folder input, an
incorrect OutNet loopback probe, an ordinary-user Wintun permission assumption,
and an aggregate backend-close delay after all native resources were already
closed. The accepted probe uses explicit resource cleanup and the role job as
the final process shutdown bound.

This record qualifies the checkpoint-25 components, not a complete isolated
daemon. Production isolation remains disabled.
