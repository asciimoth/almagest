# local_proxy_mock_http

## Setup

The case runs inside the shared e2e Docker image. `setup.sh` verifies the tools
used by the assertions are present: `curl`, `jq`, and `ip`.

No public network access is required. The daemon graph uses `MockNetwork`, so
the HTTP server and local SOCKS proxy communicate through an in-memory network
provided by Almagest nodes.

## Initial Daemon Config

`alm.hujson` starts six nodes:

- `HttpServer 1001`: listens on `127.0.0.1:8091` through its network port.
- `LocalProxy 1002`: listens as a SOCKS proxy on `127.0.0.1:1081`.
- `MockNetwork 1003`: shared virtual loopback network.
- `LogNetwork 1004`: debug network logger between the HTTP server and mock
  network.
- `LogNetwork 1005`: debug network logger between the local proxy and mock
  network.
- `HTTP Response`: string input that drives live HTTP response changes.

The harness starts the daemon with debug nodes enabled, debug logging, an
isolated Unix admin API socket rendered into `protected_alm.hujson`, and the web
admin API disabled.

## Expected Behavior

- The admin API answers `ping`.
- `almagest/LogNetwork` is available, proving debug nodes were enabled.
- The configured graph is restored with the expected nodes.
- The HTTP server logs that it is listening on `127.0.0.1:8091`.
- The local proxy logs that it is listening on `127.0.0.1:1081`.
- `curl --socks5-hostname 127.0.0.1:1081 http://127.0.0.1:8091/` returns
  `Debug http server response`.
- The successful proxied request is visible through the admin log API in:
  `LocalProxy`, `HttpServer`, and both `LogNetwork` debug nodes.
- A proxied request to an unused local port fails.
- The daemon process log shows that the engine finished building.
- Rapid port and response edits converge on the newest desired values, and an
  intermediate listener is unavailable after convergence.
- Detaching and restoring the server network starts a fresh listener whose first
  response uses edits made while detached.
- Replacing the debug logger's upstream wrapper also restores the server on the
  first request.
- Repeated response changes cannot let an old worker completion restore stale
  content.
- The final graph, port, and response work after a daemon restart.
