# Yggdrasil reference compatibility

This privileged case runs one Almagest daemon and one reference `yggdrasil-go`
process. The reference revision matches the Ygg compatibility suite:
`be5daeba7ad6b9eb3a30a3fa84e58d3962322dbd`.

Almagest connects through OutNetwork to the reference TCP listener. Its
Yggdrasil node configures a VTun. An HTTP client sends an IPv6 request through
the mesh to a server on the reference TUN. The test checks the response,
restarts the reference process, and checks peer recovery with another request.

Run with `just test-total e2e:yggdrasil_go_compat`. Docker must permit TUN
creation. The case has no public peer dependency.
