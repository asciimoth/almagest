# wireguard_amnezia_extensions

## Setup

The case runs three real `almagestd` processes inside the shared e2e Docker
container. It does not require privileged Docker networking.

Daemon A and daemon B each restore a WireGuard node, a VTun node, an
`OutNetwork`, and an HTTP server behind the VTun network. Daemon C restores one
WireGuard node with two peers, one VTun node, an `OutNetwork`, and two HTTP
clients.

## Initial Daemon Config

- `WireGuard A` listens on UDP `127.0.0.1:51830` with default WireGuard behavior
  and serves `10.201.10.1/32`.
- `WireGuard B` listens on UDP `127.0.0.1:51831` with non-default AmneziaWG
  obfuscation parameters and serves `10.201.20.1/32`.
- `WireGuard C` listens on UDP `127.0.0.1:51832` and has two peers:
  - `daemon-a` uses default WireGuard behavior for `10.201.10.1/32`.
  - `daemon-b` enables a peer-local AmneziaWG override matching daemon B's
    obfuscation parameters for `10.201.20.1/32`.
- `HTTP Client C to A` requests `http://10.201.10.1:18080/`.
- `HTTP Client C to B` requests `http://10.201.20.1:18080/`.

## Expected Behavior

- All three daemons restore their graph nodes.
- All WireGuard nodes reach the `up` state.
- All VTun nodes become ready with the configured names.
- Daemon C's HTTP client can access daemon A's plain WireGuard HTTP server.
- Daemon C's HTTP client can access daemon B's AmneziaWG-obfuscated WireGuard
  HTTP server.
- Both HTTP servers log a tunneled `GET /` request.
- A loopback packet capture exposes the standard WireGuard initiation, response,
  and transport signatures for daemon A.
- The same capture exposes no standard WireGuard packet signatures for the
  non-default AmneziaWG profile on daemon B.
