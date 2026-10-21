# TLS Nodes HTTPS

This hermetic case runs all three TLS network nodes against real HTTP traffic.
It combines the graph runtime, Formular configuration, TLS storage
notifications, link lifecycle, persistence, and the HTTP and SOCKS consumers.

The case starts local HTTPS and HTTP targets, then checks:

- a real `curl` HTTPS request intercepted by `TlsMitm` through `LocalProxy`;
- a real `curl` HTTPS request terminated to plaintext HTTP by `TlsTerminator`;
- an `HttpClient` request that reaches an HTTPS target through `TlsWrapper`;
- live valid Formular changes rebuild all three network wrappers without a
  daemon restart;
- invalid MITM rules, wrapper server names, and terminator remaps make only the
  affected branch unavailable, and later valid changes recover it;
- changing a selected TLS policy to the wrong CA breaks upstream verification in
  both consumers and restoring the policy recovers them;
- rotating the built-in local CA invalidates its old client trust, publishes new
  wrappers, and lets both client-facing TLS branches use the new CA;
- input link removal and reattachment close and rebuild each independent wrapper
  branch; and
- daemon restart restores the live node configuration, mutable TLS policy,
  rotated built-in CA, and all three data paths.

The setup creates a protected TLS policy that trusts the test origin CA. The
test also creates a mutable policy and a wrong CA through the admin API.
