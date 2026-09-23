# Sniffer curl HTTP/TLS routing

This case runs `almagestd` with a `LocalProxy -> Sniffer -> OutNetwork` graph.
The test starts local HTTP and HTTPS target servers, then uses `curl` through
the SOCKS proxy.

The Sniffer rules:

- intercept outgoing TCP dials
- route HTTP `/http-alpha` to the first HTTP target
- route HTTP `/http-beta` to the second HTTP target
- route HTTPS traffic with TLS ALPN `http/1.1` to the TLS target
- reject unmatched traffic

Assertions:

- `curl --socks5-hostname` gets the expected body for both HTTP URLs
- unmatched HTTP traffic fails
- `curl -k --http1.1` gets the expected HTTPS body
- target access logs show the expected HTTP paths and TLS ALPN
- repeated live rule changes route through sparse dynamic slots
- linked obsolete slots stay present until unlink, then disappear immediately
- detaching an active backend blocks its routes, and reattaching it restores
  the existing proxy path
