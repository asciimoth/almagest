# socks_chain_nested_mock_http

## Setup

The case runs inside the shared e2e Docker image. It is based on the SOCKS chain
from `local/alm.hujson`.

The graph exposes four local SOCKS listeners:

- `LocalProxy 2013`: listens on `127.0.0.1:1081` and uses a nested SOCKS client
  path.
- `LocalProxy 2020`: listens on `127.0.0.1:1082` and chains through
  `127.0.0.1:1083`.
- `LocalProxy 2027`: listens on `127.0.0.1:1083` and reaches the host
  `OutNetwork`.
- `LocalProxy 2094`: listens on `127.0.0.1:1084` and reaches the isolated
  `MockNetwork`.

The HTTP server binds `127.0.0.1:8080` inside the `MockNetwork`, so it is not
reachable through ordinary host networking.

## Expected Behavior

The public SOCKS listener can reach the mock HTTP server through the nested and
chained SOCKS path:

```sh
curl -g --socks5-hostname 127.0.0.1:1081 http://127.0.0.1:8080
```

The response body is `Debug http server response`.

The test also changes the live chain:

- A malformed nested proxy URL closes the old output and reports an error; a
  valid URL restores the first request.
- Rapid proxy URL changes converge on the last value without a stale wrapper
  replacing it.
- Removing and restoring the immediate and next-level upstream links propagates
  failure and recovery through the full chain.
- The rebuilt chain works on its first request after daemon restart.
