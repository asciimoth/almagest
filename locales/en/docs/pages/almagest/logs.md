The panel shows only a small recent part of the service log. The service keeps a
separate bounded in-memory cache for debug, info, warning, error, and fatal
entries. New clients receive only the entries still in those caches, then they
can subscribe to new messages.

Do not use the panel as the full log archive. For complete service history, use
the platform log destination:

- Packaged Linux services write standard output and standard error to the
  systemd journal. Use `journalctl --unit almagestd`. The upstream
  [journalctl manual](https://www.freedesktop.org/software/systemd/man/latest/journalctl.html)
  describes time, boot, follow, and output filters.
- The packaged Windows service writes a private rotating log at
  `%ProgramData%\Almagest\logs\almagestd.log`.
- A foreground `almagestd` process writes to its process output unless a service
  manager redirects that output.

[System administration](@docs/almagest/administration) lists the standard log
and service paths for each supported package.

The project is still adjusting which events belong at each log level. A version
change can make a message more or less verbose. Do not build monitoring that
depends only on the current level or exact message text.

## In-memory cache configuration

The regular configuration can set the number of cached messages per level:

```hujson
{
  "Logs": {
    "Cache": {
      "Debug": 32,
      "Info": 64,
      "Warn": 128,
      "Error": 256,
      "Fatal": 64,
    },
  },
}
```

A value of `0` disables the cache for that level. Larger values use more memory
and still do not replace platform log retention. CLI flags can override each
cache size at startup. Read
[Configuration, KV, and secrets](@docs/almagest/data) for the regular
configuration model.

## Diagnose a graph

Start at the first node that does not show its expected ready state. Then check
its providers, node menu errors, and recent log source. A visible link can stay
in the graph while its live resource is unavailable. Read
[Nodes, ports, and links](@docs/almagest/graph) for node status, popups, and
live link state.

For intermittent failures, keep the platform log and note the exact time of the
panel action. Service log timestamps include subsecond precision. Worker and
graph notification views can show activity, but they do not prove that external
traffic completed successfully.

Logs can contain hostnames, addresses, node names, error details, and program
output. Protect exported logs as operational data.
