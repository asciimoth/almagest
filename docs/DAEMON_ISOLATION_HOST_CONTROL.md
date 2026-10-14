# Host route, firewall, strict-mode, and DNS control

Checkpoint 17 is implemented in `bin/internal/isolation/network` and is used by
the packaged Linux isolated profile. The direct daemon does not use this path.

## Native ownership

The existing typed `CreateDefaultTun` request is the complete route, firewall,
strict-mode, source-route, rule, DNS-address, and underlay input. N converts it
to `sysnet.DefaultTunOpts`. The selected sysnet backend continues to select and
own its native mechanisms. R cannot send marks, routing-table numbers, firewall
objects, D-Bus calls, file paths, executable names, WFP calls, or driver calls.

This preserves the existing provider-specific implementations:

- Linux keeps its selected systemd-resolved, NetworkManager, fixed resolvconf,
  direct DNS-file, policy-routing, nftables/connmark, and kill-switch behavior.
- Windows keeps its per-adapter managed DNS, IP Helper routes, WFP policy,
  underlay monitor, and optional exclusive split-driver behavior.

The isolation adapter does not emulate these mechanisms or flatten their
capability results. Missing rights and dependencies continue through the
backend's supported, unavailable, unsupported, and unknown states. In
particular, N does not require a socket mark for a configuration where the
backend does not require default-TUN bypass.

The default-TUN resource is the ownership record for these effects. It contains
the session, graph epoch, generation, native identity, expected interface state,
and the exact normalized creation request. Native backend cleanup remains
responsible for comparing its recorded route, DNS, firewall, and driver state
before restoration or deletion. The broker verifies native identity before it
requests cleanup. It never adopts an object by name.

## Managed DNS attachment

`ConfigureDNS` is now dispatched by the broker. It accepts only an owned
default-TUN reference, an enable flag, and an opaque `ResourceDNS` endpoint.
Trusted bootstrap registers that endpoint from the bounded R/N DNS relay; R
cannot register a provider or select a native DNS mechanism. The endpoint
provides the application resolver only. Provider selection and host DNS changes
stay in N. Application resolver/node code stays in R. The native provider and
managed DNS proxy may parse and forward DNS in trusted N. A bounded relay to
R's resolver does not require moving or executing that resolver's code in N.
`ApplicationDNSEndpoint` and runtime `ServeDNS` implement this path over the
authenticated packet lane with bounded concurrency and correlation checks.

The revised production design reuses native providers and their host access.
It does not require a separate DNS writer, D-Bus helper, or upstream provider
split. The generic file/command/native-operation prohibitions apply to the API
offered to R, not to trusted provider internals. The checkpoint-24 Linux matrix
qualifies split DNS, dynamic upstream changes, and bypass behavior. Equivalent
Windows provider parity remains a later platform requirement.

Enable and disable are retained mutations. They use operation receipts, update
the default-TUN generation, and report rejected, committed, or uncertain
results. Repeating an operation ID cannot repeat a host effect. An equivalent
new request is a no-op. Provider failure after a possible effect latches
recovery-required state; only a provider error that explicitly proves no effect
is rejected without that latch.

Closing an attached DNS endpoint first verifies its native relay identity, then
detaches it from every owned default TUN, advances the affected generations,
and closes the relay. A stale or replaced endpoint is not detached or closed.
Graph release, peer loss, and shutdown use the same cleanup path. Closing a
default TUN uses the platform backend's ownership-aware cleanup for all host
effects and does not close an otherwise reusable DNS endpoint.

Optional protected policy can deny host DNS, strict mode, default routes, or
specific native rule types. The default remains all typed behavior supported by
the selected backend under the launch credentials. Policy normalization sorts
denied rule types and rejects duplicates.

## Failure contract

Non-strict cleanup restores only state which the native backend still proves it
owns. A foreign change or an unproved partial result requires recovery instead
of overwriting the host.

Strict mode remains a traffic policy, not a process-isolation property. Its
kernel persistence and crash behavior are exactly the selected backend's
reported capability. This component does not claim that a userspace broker
crash preserves a block when the backend does not provide that guarantee.

## Verification

Portable race tests cover attach, detach, replacement generations, retained
receipts, equivalent requests, policy denial, wrong resource types, foreign
sessions, endpoint registration limits, provider errors, no-effect errors,
identity replacement, endpoint-first cleanup, peer loss, and wire dispatch.
The sysnet adapter test verifies native `SetDNS` attachment, detachment,
cancellation, foreign TUN rejection, and precise unavailable-provider errors.
Structured fuzzing exercises the closed DNS request shape and resource types.

Run portable checks with:

```sh
go test -race ./bin/internal/isolation/network ./bin/internal/isolation/policy ./bin/internal/isolation/protocol ./bin/internal/isolation/transport
go test ./bin/internal/isolation/network -run '^$' -fuzz '^FuzzHostControlValidation$' -fuzztime=5s -parallel=2
```

Native Linux route, firewall, DNS, and strict-mode checks must run only in the
documented disposable Docker network namespace. Windows native checks must run
only in the disposable Windows VM. Linux profile qualification is complete in
checkpoint 24. Final restricted-role crash persistence, Windows qualification,
and complete daemon composition remain checkpoint 25, 30, and 31 work.

Verified on 2026-10-09:

- Docker with `--network none`: the network, policy, protocol, and transport
  packages pass with the race detector. The privileged Linux default-TUN test
  passes with `CAP_NET_ADMIN`, `/dev/net/tun`, and its synthetic container-only
  underlay. It now includes native managed-DNS attach and detach.
- Windows Server 2022 amd64: `winvm:m15::native-tun-control` passes in the
  privileged disposable VM and now includes native managed-DNS attach and
  detach. Evidence is in `artifacts/winvm/run-WJ4fnfWU`.
- The host-control fuzz target completed 60,782 executions in five seconds with
  two workers. The focused host-control tests pass 20 consecutive race runs.
- Linux lint and vet pass for the complete `bin` module. Windows amd64 and arm64
  test binaries compile. The complete `bin` Go test suite passes.
