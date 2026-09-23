# Command Node Lifecycle

This hermetic case exercises the daemon-only protected command node against the
real command runner. Fixed test commands append their rendered slot values to a
runtime file.

Assertions cover these situations:

- only commands with `AllowNodes` appear in the node menu;
- restored commands create stable, ordered string-slot input ports;
- trigger events and direct node triggers dispatch the selected command;
- removing a required input prevents execution, and relinking requests the
  current string value;
- field validation and hidden command selections report a form error without
  replacing the active command;
- switching commands removes obsolete slot ports and links;
- clearing and restoring the command rebuilds its ports without retaining stale
  slot values; and
- daemon restart preserves the selected command, current links, and command
  execution behavior.

The protected commands use only fixed test programs. Runtime values are passed
as positional arguments and must match restrictive slot patterns. The case uses
the debug system and does not require privileged Docker networking.
