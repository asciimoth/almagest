Almagest can select or reject traffic by process, executable, user, group, or
other platform identity. Available rule types depend on the operating system and
active host integration. The node menu shows the rule types that the service can
use.

There are two different places to apply these rules. They have different
accuracy and security properties.

## DefaultTun routing

[DefaultTun](@docs/node/almagest/DefaultTun) can include or exclude traffic
before it enters the Almagest TUN. On supported Linux systems, this uses kernel
process tracking and packet marks. On supported Windows systems, executable tree
exclusions use a system driver.

This pre-filtering is the preferred choice for host-wide application routing. It
runs before the traffic enters the userspace graph and keeps the direct and
captured routes separate.

- **Exclude** sends matching traffic outside the TUN in normal mode.
- **Include** sends only matching traffic into the TUN.
- **Strict routing** blocks the traffic that normal mode would let bypass the
  TUN.

Include and Exclude cannot be active together. Windows currently supports only
non-strict executable-tree exclusions. Read the DefaultTun page for the exact
rules on each platform.

## Filtering after capture

[TunSplitter](@docs/node/almagest/TunSplitter) can use process or socket-owner
rules after packets enter a native system TUN. Local proxy and server nodes can
also check the process that connects to their local listener.

These checks happen in userspace after the operating system created traffic.
They can race with short-lived sockets or process exit, use cached information,
or lack identity for some packets. Middleware between a native TUN and a
TunSplitter can also remove information needed for a match.

Treat post-capture process checks as best-effort routing and access control. Do
not use them as the only security boundary for untrusted local users.

## Select the correct layer

- Use DefaultTun Include or Exclude for system-wide application selection.
- Use TunSplitter rules when already captured packets must take different graph
  paths and occasional unknown ownership has a safe result.
- Use local proxy rules to limit which local applications can use that proxy.
- Use operating-system permissions, strict DefaultTun routing, and a
  [kill switch](@docs/almagest/killswitch) when bypass must fail closed.

Test rules with the exact process form that runs in service use. Launchers,
interpreters, child processes, containers, and updates can change the visible
executable path, command line, user, or process tree.
