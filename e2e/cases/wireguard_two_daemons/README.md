# wireguard_two_daemons

## Setup

The case runs two real `almagestd` processes inside the shared e2e Docker
container. It does not require privileged Docker networking.

Daemon A restores a WireGuard node, a VTun node with `10.198.10.1/24`, an
`OutNetwork`, and an HTTP client. Daemon B restores a WireGuard node, a VTun
node with `10.198.20.1/24`, an `OutNetwork`, and an HTTP server.

## Initial Daemon Config

- `WireGuard A` listens on UDP `127.0.0.1:51820` through daemon A's
  `OutNetwork`.
- `WireGuard B` listens on UDP `127.0.0.1:51821` through daemon B's
  `OutNetwork`.
- `WireGuard A` routes `10.198.20.1/32` to `WireGuard B`.
- `WireGuard B` routes `10.198.10.1/32` to `WireGuard A`.
- `HTTP Server B` listens on `10.198.20.1:18080`.
- `HTTP Client A` requests `http://10.198.20.1:18080/`.

## Expected Behavior

- Both daemons restore their graph nodes.
- Both WireGuard nodes reach the `up` state.
- Both VTun nodes become ready with the configured names.
- Sending the HTTP client request on daemon A returns `200 OK` with
  `Debug http server response`.
- The HTTP server on daemon B logs the tunneled `GET /` request.
