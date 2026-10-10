# Daemon host-network method inventory

This inventory records the direct-mode surface at daemon isolation checkpoint
3. Its original review covered Gonnect `v0.56.0`, sysnet-linux `v0.8.1`, and
sysnet-windows `v0.5.0`. The current Linux dependency is v0.10.1; checkpoint 24
tests its constructor and preserved interface surface. Direct mode selects one implementation in
`bin/almagestd/daemonservices/localnetwork` and passes the original objects to
the runtime. It does not use serialization, resource IDs, or data-path wrappers.

R means the future restricted runtime. N means the future network broker.

## System control

| Methods | Current use | Direct implementation | Future isolated owner |
| --- | --- | --- | --- |
| `Close` | Daemon shutdown | Selected platform `sysnet.System` | N |
| `Capabilities`, `CapabilitiesForTun` | Node availability, forms, dynamic TUN changes, warnings | Selected platform system | N publishes exact reports to R |
| `CheckTunOpts`, `CheckDefaultTunOpts` | TUN form validation and rebuild preflight | Selected platform system | N, with R allowed to do an earlier syntax check |
| `CheckRule`, `CompleteRule` | Default-TUN rule validation and completion | Selected platform system | N for host rules; pure address-rule work can stay in R |
| `AllocIP`, `AllocSubnet` | VTun, split, and generated-address nodes | Backend allocator; debug mode can use a seeded allocator | N for host reservations; R for purely virtual pools |
| `OutNet`, `LocalNet` | Outbound node, local SOCKS proxy, admin streams, and native consumers | Original backend networks | N supplies direct handles or bounded relays; keep platform-specific `LocalNet` rules |
| `OutDNS` | System outbound DNS node and `OutNet` resolution | Backend provider | R parses and forwards DNS over N-provided numeric socket channels |
| `BuildMatcher`; matcher `Match`, `Close`; `sysnet.MatchConn` | Default-TUN split rules and local accepted-connection matching | Backend matcher, called directly | N owns matchers and evaluates bounded five-tuples; R keeps endpoint reversal |
| `BuildTun`, `BuildDefaultTun` | Regular and default TUN nodes | Original backend TUN objects | N owns native devices and host policy; R receives an approved data path |
| `DefaultTunWarnings`, `TunWarnings` | Runtime status | Selected platform system | N |
| `SetTunMTU`, `SetTunAddrs`, `AddTunAddr`, `GetTunAddrs`, `SetTunRoutes`, `AddTunRoute`, `GetTunRoutes`, `SetTunName` | Dynamic TUN updates | Selected platform system | N, limited to session-owned devices |

`sysnet.DefaultTun.SetDNS` has two effects. DNS request parsing and forwarding
belong to R. Interface DNS configuration, routing, and cleanup belong to N.
The direct backend keeps its existing combined call so errors and rollback stay
unchanged. An isolated adapter must split the data endpoint from the host
configuration operation; it must not send a Go resolver through IPC.

## Gonnect network data path

The runtime uses every method of `gonnect.Network`. The surface is:

- Native status: `IsNative`.
- Generic stream and packet operations: `Dial`, `Listen`, `PacketDial`, and
  `ListenPacket`.
- Typed operations: `DialTCP`, `ListenTCP`, `DialUDP`, and `ListenUDP`.
- Configured packet operations: `ListenPacketConfig` and `ListenUDPConfig`.
  `ListenConfig.Control` is direct-mode-only and cannot cross IPC as a callback.
- Multicast: `ListenMulticastUDP`.
- Interface data: `Interfaces`, `InterfaceAddrs`,
  `InterfaceMulticastAddrs`, `InterfacesByIndex`, and `InterfacesByName`.
- Resolver data: `LookupIP`, `LookupIPAddr`, `LookupNetIP`, `LookupHost`,
  `LookupAddr`, `LookupCNAME`, `LookupPort`, `LookupNS`, `LookupMX`,
  `LookupSRV`, and `LookupTXT`.

Returned stream and packet objects retain the standard address, deadline,
close, half-close, buffer, packet metadata, multicast, batch, and syscall
semantics of their concrete interfaces. In direct mode, native networks remain
native. Transforming networks, such as DNS-over-TUN and proxy wrappers, remain
non-native so consumers cannot bypass their transformations.

The returned-object surface is also part of the inventory:

- `net.Conn` and `net.PacketConn`: read/write, local and remote addresses,
  deadlines, packet source/destination metadata, and close.
- `gonnect.TCPConn`: `ReadFrom`, `WriteTo`, keepalive controls, linger,
  no-delay, `CloseRead`, and `CloseWrite`, in addition to `net.Conn`.
- `gonnect.UDPConn`: typed address reads/writes and message/OOB reads/writes,
  in addition to `net.Conn` and `net.PacketConn`.
- `gonnect.TCPListener`: `Accept`, `AcceptTCP`, address, deadline, and close.
- `gonnect.MulticastPacketConn`: group join/leave, control-message selection,
  control-message reads/writes, deadlines, address, and close.
- `dns.Interface`: `Requests` and `Close`; each request keeps its cancellation
  context and exactly one response path.

R owns virtual-network listeners and all processing above these methods. N
will own native sockets and numeric resolver transports in isolated mode.

## TUN data path

The runtime uses the complete `tun.Tun` interface: `File`, `IsNative`, `Read`,
`Write`, `MWO`, `MRO`, `MTU`, `Name`, `Events`, `Close`, and `BatchSize`.
WireGuard, DNS-over-TUN, packet copy, and spoofer code use batch size and offset
values. Default-TUN code preserves the native file fast path when the source is
native. Failure observers depend on non-terminal errors remaining retryable and
on close unblocking I/O and closing the event channel.

Direct mode returns the backend TUN without an execution-mode wrapper. Future
isolated relays must preserve batching, offsets, partial writes, event order,
errors, and shutdown. A relayed TUN is not native and returns no file. An
approved direct handle can report native only when bypassing methods does not
bypass required policy or transformation.

## DNS ownership

Runtime packages own DNS messages and resolver composition:

- `lib/dns` owns graph DNS providers, routing, caching integration, tunnel DNS
  packet parsing, UDP/TCP forwarding, and TUN DNS adapters.
- `OutDNS` is exposed to the runtime as a resolver and must bypass the managed
  default TUN.
- A linked default-TUN DNS provider is selected and detached by the runtime.

The local host-network implementation owns host effects:

- Selecting and constructing the platform DNS backend.
- Installing, replacing, and removing host/interface DNS settings.
- Binding any backend-required native DNS endpoint and restoring prior host
  state during TUN replacement or close.

The current direct backend can implement both parts behind
`DefaultTun.SetDNS`; the boundary is logical because all roles share one
process. The isolated implementation must keep message parsing and forwarding
in R and expose only typed host configuration and bounded socket/data-channel
operations from N.

## Platform and optional behavior

- Linux keeps application marks, routing and kill-switch state, DNS provider
  selection, callbacks, matcher behavior, and its native `LocalNet` behavior.
- Windows keeps Wintun/NetIO, underlay binding, managed DNS configuration,
  owner matching, and loopback-restricted `LocalNet` behavior.
- Other platforms keep the debug/stub system until they have a native backend.
- Backend callback dispatch remains an allowed-command service input. It is not
  a generic command or argument surface.
- Optional concrete fast paths are `gonnect.Network.IsNative` and
  `tun.Tun.File`/`IsNative`. Runtime consumers also inspect TUN source
  generation where a backend supplies it. The extraction does not mask these
  methods or change their values.
