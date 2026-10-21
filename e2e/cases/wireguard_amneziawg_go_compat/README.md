# wireguard_amneziawg_go_compat

Checks compatibility between the embedded Almagest WireGuard node and upstream
`github.com/amnezia-vpn/amneziawg-go` with non-default AmneziaWG obfuscation.

The case starts `amneziawg-go` on a real Linux TUN device, configures it through
the upstream UAPI socket, then sends an Almagest HTTP client request through the
tunnel to a Python HTTP server bound to the upstream TUN address.

This case requires privileged Docker execution:

```sh
E2E_DOCKER_RUN_ARGS=--privileged ./e2e/run.sh wireguard_amneziawg_go_compat
```
