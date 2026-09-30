# Windows support matrix

This document records the Step 1 support contract from
`WINDOWS_BACKEND_INTEGRATION_PLAN.md`. The machine-readable sources are
[`windows/support-matrix.json`](windows/support-matrix.json) and
[`windows/test-manifest.json`](windows/test-manifest.json).

## Status meaning

- `supported`: The backend contract implements the feature. The operation can
  still be temporarily unavailable when a listed dependency or live host
  resource is absent.
- `unavailable`: Almagest does not yet provide or qualify the function on
  Windows. A later milestone can change this state after its required native
  suites pass.
- `unsupported`: The contract intentionally rejects the operation. Almagest
  must not emulate it with weaker semantics.

All three platform entries are currently unavailable. Windows Server 2022 on
amd64 is the first qualification target. Windows 11 on amd64 and Windows 11 on
arm64 need separate native evidence. The arm64 entry requires execution on a
native arm64 Windows host.

## Feature summary

| Area | Supported | Unavailable | Unsupported |
| --- | --- | --- | --- |
| Node operations | Address allocation; regular TUN creation and updates; non-strict full default routing; default-TUN rebuilds; OutNet; OutDNS; loopback LocalNet; managed DNS | A supported operation can be temporarily unavailable when Wintun, an underlay, permissions, or another required resource is absent | Adapter rename; include routing; strict routing; preferred-source routes; OutNet multicast and raw sockets |
| Rules | `win-exe-tree` exclusions; best-effort `win-pid` and `win-exe-path` ownership matchers | `win-exe-tree` becomes unavailable when the split driver cannot be owned | Linux rule semantics on Windows |
| Menus | Capability-driven class visibility, fields, rule choices, and validation | Fields report unavailable dependency and resource states | `win-exe-tree` value completion |
| Daemon | None of the native Windows daemon functions are qualified yet | Native backend selection; Windows config, storage, ACLs, and snapshots; named-pipe and authenticated web administration; console/SCM and child-process lifecycle; Windows authority stores | Unix transports, credentials, and UID/GID access rules are not Windows alternatives |

The JSON matrix is the complete feature mapping. It names the applicable OS
and architecture entries, dependencies, required suites, and the corresponding
Almagest or `sysnet` operation for every feature.

## Qualification rule

A platform entry can become supported only when all its required suites have
passing evidence from the same OS and architecture. Each required test must be
present and pass. An absent test, a skipped test, a failed suite, or evidence
from a different platform fails qualification.

The `windows-cross-build` suite checks portability only. Its execution type is
`cross-build`, so the validator does not permit it in a platform's native suite
list. A cross-build or emulated application run cannot qualify native support.

Required dependencies are deployment inputs, not daemon provisioning tasks.
Step 2 will pin their exact versions and privilege boundaries.
