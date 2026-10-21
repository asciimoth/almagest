# Linux kernel WireGuard compatibility

This privileged case ports the vanilla track from the WGO Docker compatibility
suite to the real `almagestd` WireGuard node.

The test creates a kernel WireGuard interface with `ip` and configures it with
the `wg` CLI. The Almagest side uses a WireGuard node, a userspace VTun node, an
OutNetwork node, and an HTTP client node. It verifies:

- ICMP traffic from kernel WireGuard to the Almagest userspace tunnel;
- HTTP traffic from Almagest to a service on the kernel tunnel address;
- rotation to a new preshared key through the daemon secrets API;
- failure while only one peer has the new key and recovery after both peers do;
- failure after kernel peer removal and recovery after peer restoration.

Run it with kernel networking privileges:

```sh
E2E_DOCKER_RUN_ARGS="--privileged --device /dev/net/tun" \
  ./e2e/run.sh wireguard_kernel_compat
```
