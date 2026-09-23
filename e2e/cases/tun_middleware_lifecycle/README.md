# TUN middleware lifecycle

This hermetic case connects two userspace VTun networks through a `TunCopy`:

`VTun -> TunConfigStripper -> LogTun -> TunFirewall -> TunCopy`

The same middleware chain is present on both sides. An HTTP client on one VTun
contacts an HTTP server on the other VTun.

The case verifies these situations:

- traffic crosses both middleware chains after startup;
- removing and restoring the `TunFirewall` output replaces the detached wrapper;
- `TunFirewall` detects an indirectly closed input and closes its output;
- removing and restoring the `TunConfigStripper` output replaces all wrappers
  downstream from it;
- removing and restoring the stripper input invalidates the complete old chain;
- a live VTun address change rebuilds its TUN and propagates the replacement
  through the existing graph; and
- traffic works on the first request after each recovery and after daemon
  restart.

A separate `VTun -> LogTun -> Spoofer -> MockNetwork` chain verifies that
`Spoofer` stops its netstack after the indirectly closed input and starts a new
runtime after recovery.

For indirect input removal, the case verifies each lifecycle layer: the logger
must observe the wrapper close, `TunFirewall` must change to `waiting for TUN`,
and `TunCopy` must change to `waiting for TunA` before the graph edge is
restored.

The case uses only userspace networks and does not need privileged Docker
networking.
