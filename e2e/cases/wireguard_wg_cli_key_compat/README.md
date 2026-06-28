# wireguard_wg_cli_key_compat

Checks WireGuard key format compatibility with the upstream `wg` CLI.

The case generates private keys and a preshared key with `wg genkey` and
`wg genpsk`, derives public keys with `wg pubkey`, then builds a local
WireGuard-over-SOCKS graph from those values.

It intentionally stores some keys without trailing `=` padding in `alm.hujson`
to verify Almagest accepts the common unpadded form and normalizes menu/config
values back to the padded `wg` form. The graph uses the generated preshared key
for a real WireGuard handshake and sends an HTTP request through the tunnel.
