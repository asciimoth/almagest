# TLS Admin CLI

Runs `almagestd` with persistent e2e file storage and a deterministic one-root
OS trust bundle.

The case verifies:

- Generated built-in CA metadata is exposed without leaking the private key.
- Built-in CA files are stored in persistent daemon storage with private file
  modes.
- Protected TLS certificates and policies from protected config are read-only.
- Mutable certificates and policies can be set, renamed, deleted, and survive a
  daemon restart. Policy mutations publish the corresponding TLS watch events.
- Policy rename rejects read-only and conflicting targets, accepts a same-name
  no-op, and policy delete is idempotent.
- Policy certificate references are rewritten after certificate rename/delete.
- Built-in CA option updates regenerate the CA, autosave config, and survive a
  restart without regenerating again.
- Reserved certificate names and policies that reference missing certs fail.
