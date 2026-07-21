# default_tun_dns_provider

This privileged Linux case verifies that system name resolution through
`DefaultTun` uses the DNS provider connected to its DNS port.

The test starts two DNS servers inside the e2e container:

- provider DNS on `127.0.0.1:5354`, returning `192.0.2.41`
- bypass DNS on `127.0.0.1:5355`, returning `192.0.2.99`

`alm.hujson` links `DefaultTun DNS 1701` to a `DnsClient` that queries the
provider server. The protected config sets sysnet Linux DNS mode to `direct`
with the bypass server as its fallback. A successful `getent ahostsv4` result
must be `192.0.2.41`, and the bypass server log must not contain the query.

Run with privileged Docker networking:

```sh
E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun" ./e2e/run.sh default_tun_dns_provider
```
