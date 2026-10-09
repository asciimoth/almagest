# Windows role profiles

Checkpoint 25 adds `bin/internal/isolation/windowsprofile`. The package provides
role launch restrictions. It does not enable isolated daemon startup. The
direct daemon and the public admin pipe are unchanged.

## Launch boundary

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

N currently supports the ordinary caller profile. It retains the caller's
identity with maximum privileges removed and runs outside AppContainer so it
can use native sockets. Elevated and LocalSystem N launches fail. A service N
profile needs a separately provisioned and qualified service SID and native
backend access matrix. This is an isolation-profile limitation, not a change to
the native backend's capabilities. Driver installation and loading, native
helpers, and elevation are not available through this launcher.

Every role has a separate job with one active process, kill-on-close, no
breakaway, UI restrictions, a process memory ceiling, and a CPU hard cap. The
defaults are 512 MiB and 25 percent CPU. All setup and token checks must succeed
before the first thread resumes. A failure terminates the suspended process.
`Wait` supports cancellation; `Close` kills the job and releases its handles.
Only trusted bootstrap retains process and job handles.

Creation applies DEP, SEHOP, strict handle checking, remote-image and Low-image
denial, and System32 preference. Strict-handle and image policies are read back
before resume. The flags follow Microsoft's
[process-creation attribute contract](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-updateprocthreadattribute).
This does not claim CFG, dynamic-code prevention, or LPAC. A rejected mitigation
fails launch; the launcher never removes a flag and retries.

## Objects and provisioning

`NewIdentity` creates a new, role-specific AppContainer. An existing profile is
not adopted. `ProvisionDirectory` creates a new directory with a protected
DACL, an exact role grant, and an inheritable Low integrity label. It does not
edit an existing tree. Use separate directories for R's data, C's fixed config
target, and H's private data. `GrantExecutable` is an explicit provisioning
operation that adds read/execute access for that identity to a trusted binary.
Deployment must remove obsolete identity ACEs when it removes a profile.

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

The native tests cover R/C/H and caller N, eight simultaneous locked worker
threads, private and foreign storage, broker process access, Low DLL loading,
direct Winsock denial, caller-N TCP/UDP, local name resolution and socket-owner
lookup, driver-service creation denial, normal and breakaway
child creation, effective job limits, inherited-handle exclusion, startup fault
cleanup, invalid handle types, unsafe ACLs, hard links, path replacement,
cancellation, junctions, peer closure, and repeated shutdown. A reachable native
listener before and after each probe, plus accepted-connection counts, checks
that AppContainer loopback timeouts are actual denials. The job process limit
can report `ERROR_NOT_ENOUGH_QUOTA`; breakaway must report access denied.
Service N rejection is an
expected denial, not successful service-N qualification.

The platform gate accepts only Windows Server 2022 build 20348 on amd64.
Cross-compilation does not qualify Windows 11 or arm64. Full profile acceptance
remains open for service N, final-token TUN/DNS/owner-matcher/backend tests,
deployment identity/ACL provisioning, and complete R/C/H integration. H here
is one fixed executable with no children or native network access; profiles
that need those rights require separate qualification. C's fixed-target
transaction behavior remains in `configwriter`. The default-mode switch stays
blocked until those conditions and the later complete-daemon gates pass.

## Test record

Checks run in Docker or the locked disposable Windows VM. No host network
configuration was changed.

- Docker: focused `just check` with the profile plan/path tests and the direct
  mode selection/import-boundary tests. This includes the normal project
  formatting, lint, secret scan, shellcheck and vet steps, including Windows
  amd64/arm64 lint. The optional VM suites run separately below.
- Docker: all 41 Windows VM harness unit tests passed. Docker logs are retained
  in `artifacts/windows-profiles/checkpoint25/`. Initial container setup attempts
  lacked `jq` or a writable lint cache; reruns used the full toolchain and an
  explicit writable cache.
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

The final VM run used base image key
`1b48a0a0ded885792a9f53779043ddc61ecc4a02c22ec9415df1a235bccc2326`
and source archive SHA-256
`815fa1179aa2796144a7dd5477023dcf02bf624fb7e96c44619ad09e832560f1`,
from revision `238b4ab8880b0d8a9e6e6ff5636243d9b2633db2` plus this checkpoint's
working changes. The archived profile source matches the final profile package.

Earlier VM runs are retained at `artifacts/winvm/run-njuXFHOj`,
`artifacts/winvm/run-DpIGSMYH`, and `artifacts/winvm/run-QEbZAm1t`. They identified
the TrustedInstaller-owned volume root, required AppContainer environment
values, fixed-size token-query rules, distinct restricted-token indicators,
and the need to disable the administrator group as well as remove privileges.
The tests also establish Windows' native loopback-timeout and process-quota
errors using successful control operations. Those earlier failed runs are not
qualification evidence.
