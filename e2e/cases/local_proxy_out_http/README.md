# local_proxy_out_http

## Setup

The case runs inside the shared e2e Docker image. It starts a tiny OS-level HTTP
target on `127.0.0.1:18091` with Python's standard library HTTP server.

No public internet access is required. The daemon graph uses `OutNetwork`, so
proxied traffic leaves Almagest's local SOCKS listener through the container's
ordinary network stack.

## Initial Daemon Config

`alm.hujson` starts three nodes:

- `LocalProxy 1101`: listens as a SOCKS proxy on `127.0.0.1:1081`.
- `LogNetwork 1102`: debug network logger between the local proxy and
  `OutNetwork`.
- `OutNetwork 1103`: provides the real container network to the proxy.

## Expected Behavior

- The admin API answers `ping`.
- The graph is restored with the expected nodes.
- The local proxy logs that it is listening on `127.0.0.1:1081`.
- A direct request to the OS-level HTTP target succeeds and does not appear in
  daemon node logs.
- A request to the same target through
  `curl --socks5-hostname 127.0.0.1:1081` succeeds.
- The proxied request is visible in `LocalProxy` and `LogNetwork` logs.
