# default_tun_dns_provider

This privileged Linux case verifies both OutNetwork resolver guarantees while
DefaultTun controls system DNS:

- OutNetwork sends DNS requests to the original upstream that existed before
  DefaultTun started.
- OutNetwork uses `/etc/hosts` before it sends a DNS request.
- OutNetwork does not fall back to the managed system resolver after an
  upstream error.
- The final marked connection bypasses DefaultTun routing.

The test starts separate original and managed DNS servers. They return
different answers for the same names. A managed wildcard answer points to a
poison HTTP target. This makes use of the wrong resolver visible even when a
request can otherwise complete.

The setup also creates a routed public namespace. Direct traffic to that
namespace stops when DefaultTun is active. Requests through the graph's
OutNetwork must continue to work because its connection has the application
bypass mark.

The scenario covers resolution before and after DNS takeover, static aliases,
case and trailing-dot matching, IPv6 hosts data, hosts-file priority, live
hosts-file add and remove operations, `NXDOMAIN`, concurrent requests, DNS-link
detach and attach, and repeated DefaultTun creation.

Run with privileged Docker networking:

```sh
E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun" ./e2e/run.sh default_tun_dns_provider
```
