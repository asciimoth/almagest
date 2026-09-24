# Protected built-in TLS CA

Starts the daemon with immutable built-in CA bundles that use RSA, ECDSA, and
Ed25519 private keys. It verifies protected CA publication and mutation
rejection. It also verifies startup rejection for incomplete, duplicated,
unsupported, mismatched, and trailing-data PEM bundles.
