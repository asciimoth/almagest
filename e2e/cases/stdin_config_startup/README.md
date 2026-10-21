# stdin_config_startup

## Setup

The case starts the real daemon with `--stdin`, `--tmp-unix-admin`, and
`--tmp-file-storage`. The standard-input document contains both the editable
graph and an embedded top-level `Protected` object. The protected object selects
the debug system, limits Unix admin access, and defines a read-only secret.

## Actions and Expected Behavior

- The daemon creates its temporary Unix admin socket below `XDG_RUNTIME_DIR`.
- The graph from standard input starts and is available through that socket.
- The embedded protected secret is available only to its configured node and is
  reported as read-only.
- Config snapshots are read-only and do not expose the embedded protected object
  or its secret value.
- Config updates are rejected because standard-input configuration is read-only,
  without changing the running graph.
- Stopping the daemon removes both the temporary admin socket and temporary file
  storage.
