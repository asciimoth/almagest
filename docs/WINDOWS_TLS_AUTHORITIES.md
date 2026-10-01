# Windows certificate authority discovery

On Windows, Almagest reads public certificates from these logical stores:

- `CurrentUser\Root` for the identity that runs `almagestd`;
- `LocalMachine\Root` for the computer.

Thus, console and service processes can see different current-user
certificates. A service does not read the store of the interactive user. Store
enumeration copies only public certificate DER. It does not open or export a
private key.

Discovered certificates appear as read-only `os-ca:` entries. Discovery does
not trust a certificate. A TLS policy must explicitly select an entry as a
trust anchor or constraint. Almagest does not claim that this catalog reproduces
Windows chain building, distrust, usage restrictions, group policy, automatic
root updates, or application-specific exceptions.

Almagest reads both stores at startup. The `tls reload-os-certs` CLI command and
the corresponding panel action replace the in-memory OS catalog. They show
certificate additions and removals without restarting the daemon. A store open,
enumeration, or close failure fails startup or reload and leaves an existing
runtime catalog unchanged. The error identifies the logical store.

If both selected stores are empty, Almagest uses its compiled NSS fallback
catalog. Set `ALMAGEST_TLS_FORCE_FALLBACK_ROOTS` to a nonempty value to skip
Windows store access and use that fallback explicitly. The fallback is also a
candidate catalog: it does not create implicit trust.
