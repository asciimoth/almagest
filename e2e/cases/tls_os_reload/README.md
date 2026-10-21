# TLS OS Reload

Runs `almagestd` against a deterministic `SSL_CERT_FILE` and empty
`SSL_CERT_DIR`, then changes the bundle and calls
`almagest-cli tls reload-os-certs`.

The case verifies:

- OS CA entries are loaded from the process environment at startup.
- Certcat warning roots are annotated and excluded from the default policy.
- Certcat narrow-scope roots are annotated and included with their DNS scope.
- Reload removes old OS roots, keeps unchanged roots, adds new roots, and
  rebuilds the default policy.
- `tls watch` receives certificate delete/upsert events and a default-policy
  update for the real daemon mutation.
