# TLS OS Reload

Runs `almagestd` with `--isolation=off` and `--isolation=auto` against a
deterministic `SSL_CERT_FILE` and empty `SSL_CERT_DIR`. It then changes the
bundle and calls `almagest-cli tls reload-os-certs`. In automatic isolation
mode, the restricted runtime cannot read the bundle. It receives bounded literal
snapshots at startup and through the broker refresh operation.

The case verifies:

- OS CA entries are loaded from the process environment at startup.
- A root with a negative serial number is skipped at startup and refresh while
  valid roots remain available. The daemon logs the parse error.
- Direct and automatic isolation modes produce the same reload behavior.
- Isolated startup and refresh use the broker-owned platform TLS provider.
- Certcat warning roots are annotated and excluded from the default policy.
- Certcat narrow-scope roots are annotated and included with their DNS scope.
- Reload removes old OS roots, keeps unchanged roots, adds new roots, and
  rebuilds the default policy.
- `tls watch` receives certificate delete/upsert events and a default-policy
  update for the real daemon mutation.
