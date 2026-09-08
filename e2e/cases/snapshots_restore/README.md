# Snapshot restore

This case verifies the daemon snapshot API through `almagest-cli`. It saves and
restores a live graph that uses a protected secret. It also restores a mutable
secret, default and plugin KV data, a DNS cache, the built-in TLS CA, and an
arbitrary future file-storage subtree. It verifies that protected config and
existing snapshot archives survive a restore.
