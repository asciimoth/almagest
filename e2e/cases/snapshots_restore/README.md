# Snapshot restore

This case verifies the daemon snapshot API through `almagest-cli`. It saves and
restores a live graph that uses a protected secret. It exports, deletes,
imports, renames, and deletes the stored archive. It also restores a mutable
secret, default and plugin KV data, a DNS cache, the built-in TLS CA, and an
arbitrary future file-storage subtree. It verifies that protected config and
existing snapshot archives survive a restore. The storage subtree includes a
relative symlink chain, which must remain inside storage and survive restore.
Finally, it starts the daemon with `--snapshot` and verifies that config, bbolt
data, and file storage load before the engine starts.
