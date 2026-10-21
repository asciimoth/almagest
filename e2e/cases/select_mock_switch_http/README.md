# select_mock_switch_http

## Setup

The case runs inside the shared e2e Docker image. It mirrors the switch graph
from `local/alm.hujson`: two local SOCKS proxies, two isolated `MockNetwork`
instances, and two HTTP servers that both bind `127.0.0.1:8080`.

Both HTTP servers intentionally use the same address and port. They do not
conflict because each server is attached to a different `MockNetwork`.

## Initial Daemon Config

- `BoolConstant 1815`: starts as `false` and drives both selectors.
- `LocalProxy 1857`: listens as SOCKS on `127.0.0.1:1081`.
- `LocalProxy 1859`: listens as SOCKS on `127.0.0.1:1082`.
- `HttpServer 1867`: serves `Server A` in the first mock network.
- `HttpServer 1873`: serves `Server B` in the second mock network.

## Expected Behavior

Initially:

- `curl -g --socks5-hostname 127.0.0.1:1081 http://127.0.0.1:8080` returns
  `Server A`.
- `curl -g --socks5-hostname 127.0.0.1:1082 http://127.0.0.1:8080` returns
  `Server B`.

After updating `BoolConstant 1815` to `true` through the admin API:

- `curl -g --socks5-hostname 127.0.0.1:1081 http://127.0.0.1:8080` returns
  `Server B`.
- `curl -g --socks5-hostname 127.0.0.1:1082 http://127.0.0.1:8080` returns
  `Server A`.
