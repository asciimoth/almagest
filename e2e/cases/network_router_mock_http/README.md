# network_router_mock_http

## Setup

The case runs inside the shared e2e Docker image. It uses the graph from
`local/alm.hujson`: one local SOCKS proxy, one `NetworkRouter`, three isolated
`MockNetwork` slots, and one HTTP server in each slot.

The HTTP servers all bind `127.0.0.1:80`. This is intentional: each
`MockNetwork` has its own loopback network, so the servers do not conflict and
the test does not depend on container DNS or host networking.

## Expected Behavior

- `curl -g --socks5-hostname 127.0.0.1:1081 http://host1/` returns `Server 1`.
- `curl -g --socks5-hostname 127.0.0.1:1081 http://host2/` returns `Server 2`.
- Any other hostname through the same proxy uses the router default slot and
  returns `Server 3`.
- Repeated live rule changes immediately switch the selected backends while the
  existing proxy output stays usable.
- Invalid rule text does not replace the last valid routing program.
- Rules can add a dynamic slot. Linked obsolete ports stay available, then
  disappear immediately when their last link is removed.
- Only rule-owned slot ports return after a daemon restart.
- Removing one active backend only breaks routes for that backend. Reattaching
  it restores the first request.
- Removing and restoring the proxy output link replaces its network wrapper.
- Live rule and link changes persist across a daemon restart.
