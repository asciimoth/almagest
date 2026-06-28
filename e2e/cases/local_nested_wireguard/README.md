# local_nested_wireguard

## Setup

The case runs inside the shared e2e Docker image and is based on the nested
WireGuard graph from `local/alm.hujson`.

It does not require privileged Docker networking. The graph uses a userspace
`MockNetwork`, two outer WireGuard nodes, two inner WireGuard nodes, VTun nodes
for each tunnel, and debug HTTP/UDP clients inside the innermost tunnel.

## Initial Daemon Config

- `WireGuard Client 1501` and `WireGuard Server 1502` form the outer tunnel
  over `MockNetwork 1500`.
- `Inner WireGuard Client 1507` and `Inner WireGuard Server 1508` form the
  inner tunnel over the outer VTun network.
- `Inner VTun Client 1509` owns `10.199.10.1/24`; `Inner VTun Server 1510`
  owns `10.199.20.1/24`.
- `UDPPingServer 1505` listens on `10.199.20.1:26120`.
- `UDPPingClient 1506` sends `nested-wireguard-udp-ping` from
  `10.199.10.1`.
- `HttpServer 1607` listens on `10.199.20.1:8080`.
- `HttpClient 1612` requests `http://10.199.20.1:8080/`.

## Expected Behavior

- The daemon restores the local graph with the expected nodes.
- Outer and inner WireGuard nodes reach the `up` state.
- All VTun nodes become ready with the configured names.
- Sending the UDP ping through the admin menu returns
  `pong:nested-wireguard-udp-ping`.
- Sending the HTTP request through the admin menu returns `200 OK` with
  `Debug http server response`.
- The innermost HTTP and UDP servers log the tunneled requests.
