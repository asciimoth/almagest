# Engine surface validation

This case exercises the complete daemon engine API against a real workspace.
It uses only userspace resources.

The case verifies:

- every registered node class exposes a class snapshot and long description;
- every form-backed Almagest node accepts validation and apply messages built
  from its current menu state;
- compatible default ports attach and detach across the live class instances;
- unsupported workspaces cannot mutate the main workspace;
- every engine API method rejects a payload of the wrong JSON type; and
- placeholder, popup, copy/paste, movement, history, and node-menu operations
  leave the daemon responsive and the workspace consistent.

After covering every left and right port, the case samples 100 additional
compatible link pairs by default. Set `ALMAGEST_E2E_LINK_MATRIX_ATTEMPTS` to a
different non-negative integer for a larger or smaller stress matrix.
