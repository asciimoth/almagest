# wireguard_socks_local

## Setup

The case runs inside the shared e2e Docker image and is based on the
WireGuard-over-SOCKS graph from `local/alm.hujson`.

It does not require privileged Docker networking. The graph uses userspace
`VTun` nodes, embedded WireGuard devices, a local SOCKS server, and ordinary
loopback UDP sockets inside the container.

## Initial Daemon Config

- `WireGuard Client` listens on UDP `127.0.0.1:51820` through `SocksClient 56`.
- `WireGuard Server` listens on UDP `127.0.0.1:51821` through `SocksClient 86`.
- Both SOCKS clients use `socks5://127.0.0.1:1081?gost` so the WireGuard peer
  endpoint ports remain stable through the SOCKS transport.
- `SocksServer 70` listens on `127.0.0.1:1081`.
- `VTun Client` owns `10.198.10.1/24`; `VTun Server` owns `10.198.20.1/24`.
- `HTTP Server` listens on `10.198.20.1:18080`.
- `HTTP Client` requests `http://10.198.20.1:18080/`.

## Expected Behavior

- The daemon restores the local graph with the expected nodes.
- The SOCKS server starts listening on `127.0.0.1:1081`.
- Both WireGuard nodes reach the `up` state.
- Both VTun nodes become ready with the configured names.
- Sending the HTTP client request through the admin menu returns `200 OK` with
  `Debug http server response`.
- The HTTP server logs the proxied `GET /` request.
