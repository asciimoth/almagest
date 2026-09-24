# Safe config startup

Starts `almagestd` without `--unsafe-config`, an explicit file-storage path, or
pre-existing config files. The case verifies secure config creation, the Linux
default persistent storage path, config writes, and restart persistence.
