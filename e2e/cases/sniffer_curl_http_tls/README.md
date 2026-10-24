# Sniffer HTTP/TLS/SSH routing

This case runs `almagestd` with a `LocalProxy -> Sniffer -> OutNetwork` graph.
The test starts local HTTP, HTTPS, and SSH-like target servers. It uses `curl`
and a raw SOCKS client through the proxy.

The Sniffer rules:

- intercept outgoing TCP dials
- route HTTP `/http-alpha` to the first HTTP target
- route HTTP `/http-beta` to the second HTTP target
- route HTTPS traffic with TLS ALPN `http/1.1` to the TLS target
- route an SSH client banner to the raw TCP target
- reject unmatched traffic

Assertions:

- `curl --socks5-hostname` gets the expected body for both HTTP URLs
- unmatched HTTP traffic fails
- `curl -k --http1.1` gets the expected HTTPS body
- target access logs show the expected HTTP paths and TLS ALPN
- the SSH target receives the client banner unchanged
- repeated live rule changes route through sparse dynamic slots
- linked obsolete slots stay present until unlink, then disappear immediately
- detaching an active backend blocks its routes, and reattaching it restores the
  existing proxy path
