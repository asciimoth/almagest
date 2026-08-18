Runs the TLS network nodes against real HTTP traffic.

The case starts local HTTPS and HTTP targets, then checks:
- a real `curl` HTTPS request intercepted by `TlsMitm` through `LocalProxy`;
- a real `curl` HTTPS request terminated to plaintext HTTP by `TlsTerminator`;
- an `HttpClient` request that reaches an HTTPS target through `TlsWrapper`.

The daemon built-in local CA signs the client-facing certificates. The setup
also creates a protected TLS policy that trusts the test origin CA for upstream
HTTPS verification.
