# VTun and Spoofer IPv6 fallback

This case verifies the dual-stack TCP fallback path through this graph:

`HttpClient -> VTun -> TunJoiner -> Spoofer -> OutNetwork`

The test DNS server returns one reachable IPv4 address and one IPv6 address.
The IPv6 address is in a separate network namespace. An nftables rule silently
drops TCP SYN packets for the test port. Thus, the IPv6 connect stays pending
instead of returning an immediate error.

The test first verifies that the IPv6 target is a real black hole. It then
sends five new HTTP requests. Each request must reach the IPv4 server in less
than five seconds. The test also checks that the IPv6 drop counter increased,
so a success that did not try IPv6 cannot pass.

Run this case with:

```sh
E2E_DOCKER_RUN_ARGS="--privileged" ./e2e/run.sh vtun_spoofer_ipv6_fallback
```
