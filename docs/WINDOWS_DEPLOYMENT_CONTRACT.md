# Windows deployment contract

This document defines the Windows deployment and privilege boundaries. The Go
module files lock source dependencies. The Nix flake locks the Wintun and split
driver artifacts. Do not copy their versions or hashes into this document or
application code.

## Native dependencies

Deploy the architecture-matched `wintun.dll` from the flake output with the
Almagest executable. The daemon loads it only from the application directory
or `System32`. Only administrators and the deployment identity can write these
locations.

Install the flake-provided Mullvad split driver as the
`mullvad-split-tunnel` demand-start kernel service. Its binary path is
`%SystemRoot%\System32\drivers\mullvad-split-tunnel.sys`. The service is a
global exclusive resource, so independent VPN controllers must not use it at
the same time.

External deployment tooling owns dependency download, installation, service
registration, service start, update, repair, and removal. The daemon does not
do these operations. Wintun adapter creation can still cause privileged driver
and operating-system changes through the deployed DLL.

## Privileges

A standard user can construct the system, inspect capabilities, allocate
candidate addresses, use OutNet, OutDNS, and LocalNet, and use ownership
matchers. A protected process can produce an unknown matcher result.

An elevated service identity or administrator is required to create or delete
Wintun adapters, change addresses, routes, MTU, or DNS, manage WFP state, and
open or control the split driver. Run the service as Local System for the
default machine administration pipe. Elevated administrators are also allowed
by its ACL.

## Degraded operation

An absent, busy, or unusable split driver disables executable-tree exclusions.
It must not disable regular TUNs, default routing without exclusions, OutNet,
OutDNS, LocalNet, or the best-effort process and executable-path matchers.

The daemon must preserve permission and capability errors. It must not report
permission denial as unsupported behavior or report a deferred ownership check
as available.
