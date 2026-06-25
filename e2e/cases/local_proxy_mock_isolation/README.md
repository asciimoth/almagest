# local_proxy_mock_isolation

## Setup

The case runs inside the shared e2e Docker image. It starts a tiny OS-level HTTP
target on `127.0.0.1:18092` with Python's standard library HTTP server.

No public internet access is required. The daemon graph uses `MockNetwork`, so
only services attached to that virtual network should be reachable through the
local SOCKS proxy.

## Initial Daemon Config

`alm.hujson` starts five nodes:

- `HttpServer 1201`: listens on `127.0.0.1:8091` through its network port.
- `LocalProxy 1202`: listens as a SOCKS proxy on `127.0.0.1:1081`.
- `MockNetwork 1203`: shared virtual loopback network.
- `LogNetwork 1204`: debug network logger between the HTTP server and mock
  network.
- `LogNetwork 1205`: debug network logger between the local proxy and mock
  network.

## Expected Behavior

- Direct container networking to `127.0.0.1:18092` works and is not visible in
  Almagest node logs.
- A proxied request to the HTTP server attached to `MockNetwork` succeeds.
- A proxied request to the OS-level target on `127.0.0.1:18092` fails because
  the proxy only has the isolated `MockNetwork`.
- The OS-level target never receives the blocked proxied request.
