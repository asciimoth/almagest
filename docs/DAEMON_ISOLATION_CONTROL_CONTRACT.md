# Daemon isolation control contract

Status: checkpoint 9 control contract, version 1. Checkpoint 13 selected
broker-owned relays and froze the separate
[version 1 data-channel contract](DAEMON_ISOLATION_DATA_CHANNEL_CONTRACT.md).
Its implementation and production qualification are later checkpoints.

The Go definitions are in `bin/internal/isolation/protocol`. Policy definitions
are in `bin/internal/isolation/policy`. The checkpoint 14 transport is in
`bin/internal/isolation/transport`; see its
[implementation contract](DAEMON_ISOLATION_CONTROL_TRANSPORT.md). These packages
are not connected to daemon startup and do not enable isolated production mode.
Checkpoint 15 implements the TUN owner in `bin/internal/isolation/network`;
see its [ownership and result contract](DAEMON_ISOLATION_NETWORK_CONTROL.md).

## Authority and identity

Trusted bootstrap creates a session ID, nonzero session epoch, normalized policy
digest, protocol version, and fixed role channels. A peer cannot select its role.
Each request has a request ID, session ID and epoch, and graph epoch. Each
mutation also has an operation ID. Request IDs correlate one exchange. Operation
IDs identify side effects and retained receipts across a canceled request or lost
reply.

N generates opaque 128-bit resource IDs. A resource reference binds the ID to
its session, resource type, graph epoch, and generation. Authorization occurs
before lookup. A generation changes when the represented native resource or its
control state is replaced. A graph epoch changes for a complete graph restore or
shutdown and invalidates pending work and all resources from the old epoch.
Names, paths, process IDs, descriptor numbers, pointers, and native handles are
not resource IDs.

Cancellation asks an owner to stop work if it can. It does not assert that no
effect occurred. Mutation results are `rejected`, `committed`, `compensated`, or
`uncertain`. Owners retain bounded results by operation ID. A repeated ID with
different input is a conflict. After receipt expiry the result is unknown and
the caller reconciles state; it does not replay a non-idempotent operation.
An uncertain host mutation sets recovery-required state until N proves or
repairs the native state.

Default network authority is all typed sysnet behavior that the selected backend
supports under the launch credentials and existing protected settings. The
default policy has no network-operation allowlist. Optional protected policy can
only reduce this authority. Capability reports preserve `supported`,
`unsupported`, `unavailable`, and `unknown`, plus reasons, limitations, and an
explicit `AttemptAllowed` value. Thus a valid attempt-on-unknown remains valid.
An unknown operation is always rejected.

## Operation review

The following table maps every host-network inventory group to an owner and a
version 1 typed operation. Array values use the compiled 4,096-item ceiling and
can have lower policy limits. All mutations carry an operation ID, expected
graph epoch, and expected resource generation where a resource exists.

| Inventory surface | Owner and typed contract | Result, cancellation, and uncertain-result rule |
| --- | --- | --- |
| `Close` | N: `CloseResource` for one ID; `ReleaseGraphEpoch` for graph teardown | Idempotent close. It wakes blocked operations. A native teardown that cannot be proved is uncertain and requires recovery. |
| `Capabilities`, `CapabilitiesForTun` | N: `Capabilities`; resource state supplies the per-TUN revision | Bounded snapshot with four-state capability records. Read cancellation has no host effect. |
| `CheckTunOpts`, `CheckDefaultTunOpts` | N: `CheckTun`, `CheckDefaultTun` with typed options, rules, and strict flag | Read-only normalized validation. Mutation revalidates. |
| `CheckRule`, `CompleteRule` | N: `CheckRule`, `CompleteRule` with a fixed rule context | Completion returns at most 128 typed rules. It is not a process or file browser. |
| `AllocIP`, `AllocSubnet` | N: `ReserveAddress` with kind and address family; `CloseResource` releases it | Returns an allocation ID and observed value. Canceled allocation is reconciled by operation receipt. Pure virtual pools stay in R. |
| `OutNet`, `LocalNet` | N: `OpenSocket` with purpose, fixed socket kind, typed endpoints, and named options | Returns a socket/listener ID for the frozen relay contract. Backend-specific local-network scope is preserved. |
| `OutDNS` | R parses DNS; N: `ResolverSnapshot` and outbound numeric `OpenSocket` | Bounded numeric server snapshot. No resolver object or host path crosses the contract. |
| `BuildMatcher`, `Matcher.Match`, `Matcher.Close` | N: `CreateMatcher`, `Match`, `CloseResource` | `Match` accepts one bounded protocol/local/remote five-tuple and returns match and quality only. Cancellation or overload is an error, not a nonmatch. |
| `BuildTun`, `BuildDefaultTun` | N: `CreateTun` with regular/default kind and typed options | Returns an owned ID and observed state for packet relay. Partial native setup is compensated or marked uncertain. |
| TUN warnings | N: `GetTunState` operation represented by resource state/status reads | Bounded warnings; diagnostics are not operation completion. |
| TUN MTU, address, route, and name getters/setters | N: `UpdateTun` with one closed update kind; `GetTunState` | Complete replacement sets avoid unsafe read-add-write where practical. Generation conflict rejects before mutation. |
| Default-TUN DNS host effect | N: `ConfigureDNS` with owned default-TUN and endpoint references | N verifies both IDs and applies or removes host configuration. DNS packets stay in R. Partial replacement follows the uncertain-result rule. |
| Network interfaces and addresses | N: `ListInterfaces` typed snapshot | At most 4,096 entries. It is not netlink, ioctl, or registry access. |

The complete Gonnect call mapping is:

- `Dial`, `DialTCP`, `Listen`, `ListenTCP`, `PacketDial`, `ListenPacket`,
  `DialUDP`, `ListenUDP`, `ListenPacketConfig`, `ListenUDPConfig`, and
  `ListenMulticastUDP` map to a closed `OpenSocket` kind and named options.
  `ListenConfig.Control`, arbitrary socket options, `syscall.RawConn`, and a
  runtime callback do not cross the boundary.
- `Interfaces`, `InterfaceAddrs`, `InterfaceMulticastAddrs`,
  `InterfacesByIndex`, and `InterfacesByName` use one bounded
  `ListInterfaces` snapshot. R performs index/name selection on that snapshot.
- Resolver lookup methods stay in R. R sends DNS messages through numeric
  sockets from `ResolverSnapshot`; it does not ask N to parse hostnames.
- `IsNative` is false for the selected relay. Version 1 transfers no native
  handles. The direct-mode backend retains its original value.

The returned `net.Conn`, packet, TCP, UDP, listener, multicast, DNS-request, and
TUN read/write surfaces use the separate version 1 data-channel contract, not
generic control messages. Its implementation must preserve deadlines, half-close,
metadata, multicast, batching, offset, event, partial-write, error, cancellation,
and close behavior. Checkpoint 13 selects broker-owned relay for every resource;
shared memory and direct handles are excluded. A proxied TUN returns no file and
is not native.

## Other owner contracts

C receives only `ReadCurrent`, `Replace(expectedRevision, operationID, bytes)`,
and `GetResult(operationID)`. Bootstrap fixes its sole regular-config target.
No request contains a path, owner, mode, ACL, temporary name, protected selector,
or backup location. Config is bounded opaque data. Revision conflicts reject
before write; lost replies use the retained operation result.

H receives `RunNodeCommand(commandID, slots, operationID)` only for protected
entries with node authority. N uses the separate `RunCallback` request on its
trusted channel and derives its event and resource values from committed broker
state. Neither request contains executable path, argv, environment, working
directory, identity, or sandbox values. Those values belong to the protected
profile. A runtime label cannot acquire callback authority.

Diagnostic records keep severity, timestamp, source, message, launch/session,
trusted process instance, sequence, and optional resource/generation/operation
correlation. The collector assigns role and process instance from the channel.
Records exclude packet content, config content, secrets, command slots, and raw
process metadata. A diagnostic is never an acknowledgement, instruction, or
lifecycle signal.

## Limits and prohibited requests

The compiled control ceilings are 64 KiB per control frame, 32 levels, 128
concurrent requests per session, 64 KiB per config chunk, and 8 MiB total config.
Policies can lower but not raise them. Operation-specific arrays and strings have
finite constants in the protocol package. Data-frame limits are defined
separately in the frozen data-channel contract.

There is no generic syscall, file, path, argv, environment, process-spawn,
socket-option, ioctl, netlink, registry, service-control, D-Bus, WFP, driver,
native-control, DLL, namespace, or protected-policy request. Adding one of these
requires a new reviewed typed operation and protocol version; a generic escape
hatch is not compatible with this contract.

## Test evidence

Unprivileged package tests exercise zero and forged resource IDs, foreign
sessions, wrong resource types, stale generations, stale graph epochs, revision
conflicts, cancellation after commit, a lost reply recovered by operation ID,
operation-ID reuse with different input, expired receipts, default authority,
optional restrictions, unknown capability attempts, and separation of node and
callback command authority.

On 2026-10-09, focused race-enabled `just check` selectors passed for resource
ownership, cancellation/lost replies, and default authority. The complete
`just check` gate then passed on Linux 7.2.8, amd64, Go 1.26.7, on an AMD Ryzen
5 5600H host. It included lint, formatting, vet, secret and vulnerability scans,
regular Go tests, all 99 panel tests, all 77 Docker E2E cases including the
privileged cases, all 9 ordinary and 20 privileged Windows VM cases, and all
116 Windows packet-flow observations. Combined statement coverage was 86.0%.
Windows evidence is in `artifacts/winvm/run-9COTVRpO` and
`artifacts/winvm/run-flow-bSTXrb7r`. No test failed in the final run.

### Protocol and transport regression tests

The expanded package tests check every request type and its mutation rule,
identity encoding, session and diagnostic validation, exact JSON and frame
limits, truncated frames, short writes, handshake mismatches, closed error
codes, request and event sequences, reserved reply capacity, late replies,
and config-transfer ordering, replay, reset, and fixed deadlines. Linux tests
also check credential mismatches and descriptor cleanup when later ancillary
metadata is invalid.

Run checks in disposable Docker containers or the Windows VM, not in the host
network namespace. Inside the Docker checkout, use:

```sh
go test -race -cover -timeout 90s ./bin/internal/isolation/protocol ./bin/internal/isolation/transport
go test ./bin/internal/isolation/protocol -run '^$' -fuzz '^FuzzOpaqueIdentity$' -fuzztime=30s -parallel=2
go test ./bin/internal/isolation/protocol -run '^$' -fuzz '^FuzzRequestMetadata$' -fuzztime=30s -parallel=2
go test ./bin/internal/isolation/transport -run '^$' -fuzz '^FuzzStrictJSONRoundTrip$' -fuzztime=30s -parallel=2
go test ./bin/internal/isolation/transport -run '^$' -fuzz '^FuzzConfigRoundTrip$' -fuzztime=30s -parallel=2
```

These fuzz tests check parser results against independent validity rules, stable
canonical frames, exact config reassembly, and transfer-ID replay rejection.
The existing `FuzzControlIdentity`, `FuzzControlDecode`, `FuzzControlFrame`,
`FuzzConfigAssembly`, and Linux `FuzzAncillary` targets remain available.
The Linux ancestor-credential test needs `CAP_SYS_ADMIN` and a namespace-capable
seccomp profile inside Docker; other portable tests need neither.

`just test-total winvm:m14::private-control-transport` runs both complete packages
in the Windows VM, including the fuzz seed cases and Windows endpoint tests.
