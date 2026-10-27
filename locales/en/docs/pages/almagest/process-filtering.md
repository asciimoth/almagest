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
non-strict executable-tree exclusions. Read the
[DefaultTun](@docs/node/almagest/DefaultTun) page for the exact
rules on each platform.

## Filtering after capture

[TunSplitter](@docs/node/almagest/TunSplitter) can use process or socket-owner
rules after packets enter a native system TUN. The
[LocalProxy](@docs/node/almagest/LocalProxy) node and local server nodes can
also check the process that connects to their local listener.

These checks happen in userspace after the operating system created traffic.
They can race with short-lived sockets or process exit, use cached information,
or lack identity for some packets. Middleware between a native TUN and a
TunSplitter can also remove information needed for a match.

Treat post-capture process checks as best-effort routing and access control. Do
not use them as the only security boundary for untrusted local users.

## Comparison with other implementations

Projects use the term *per-app routing* for different mechanisms. Some projects
find the socket owner from a TCP or UDP 5-tuple after interception. Other
projects classify a process or socket before normal userspace forwarding. The
second mechanism can keep direct traffic out of the userspace packet path.

The following comparison covers application identity and the point at which each
project makes the routing decision. It does not compare protocol support or
general routing features.

- Almagest supports command name, executable, command line, PID, user, and group
  rules on Linux. Windows supports executable-tree rules before capture, and PID
  or executable-path rules after capture.
  [DefaultTun](@docs/node/almagest/DefaultTun) uses Linux eBPF process tracking
  and packet marks for Include, Exclude, and Strict routing. On Windows, it uses
  the Mullvad split-tunnel driver and Windows Filtering Platform for non-strict
  executable-tree exclusions. [TunSplitter](@docs/node/almagest/TunSplitter) and
  the [LocalProxy](@docs/node/almagest/LocalProxy) node can separately find an
  owner from a captured TCP or UDP 5-tuple on Linux and Windows.
- [Xray-core](https://xtls.github.io/en/config/routing.html#ruleobject) matches
  a process name, an absolute path, or a directory on Linux and Windows. An
  Android client can supply its own application finder. The routing core
  [finds a process from the connection 5-tuple](https://github.com/XTLS/Xray-core/blob/main/app/router/condition.go).
  This is comparable to Almagest's post-capture matching. Xray does not document
  an operating-system pre-filter for these rules.
- [sing-box](https://sing-box.sagernet.org/configuration/route/rule/#process_name)
  supports process-name and path rules on Linux, Windows, and macOS. Linux also
  supports user rules. Android uses package names. Normal route rules
  [find the process from the connection 5-tuple](https://github.com/SagerNet/sing-box/blob/testing/route/process_cache.go).
  Linux `auto_redirect` also has a
  [`bypass` pre-match action](https://sing-box.sagernet.org/configuration/route/rule_action/#bypass).
  It can keep matching direct connections out of sing-box userspace forwarding.
  This early path requires Linux, `auto_redirect`, and NFQUEUE. It provides
  direct bypass, not Almagest's early Include, Exclude, and Strict modes.
- [AmneziaVPN](https://docs.amnezia.org/documentation/instructions/vpn-split-tunneling/)
  can include or exclude selected application packages on Android. It can
  exclude selected executable paths on Windows. Application routing is not
  available on its Linux, macOS, or iOS clients. Android uses the operating
  system's per-app VPN selection. Windows uses a
  [Mullvad split-tunnel driver](https://github.com/amnezia-vpn/amnezia-client/blob/dev/client/platforms/windows/daemon/windowssplittunnel.cpp)
  to exclude applications before their traffic uses the VPN. AmneziaVPN does not
  offer Windows application Include mode or a separate 5-tuple rule layer.
- [Mullvad VPN](https://github.com/mullvad/mullvadvpn-app/blob/main/docs/split-tunneling.md)
  supports application exclusions on Windows, Linux, macOS, and Android.
  Identity and child-process behavior depend on the platform. Each platform
  excludes selected traffic before normal VPN forwarding. Windows uses the same
  [Mullvad split-tunnel driver](https://github.com/mullvad/win-split-tunnel)
  family that Almagest uses. Linux uses a dedicated cgroup for excluded
  processes. Mullvad VPN provides exclusion mode, not a separate 5-tuple routing
  layer.
- [dae](https://github.com/daeuniverse/dae/blob/main/docs/en/how-it-works.md#splitting-criteria)
  can match the process name of traffic from the local Linux host. eBPF cgroup
  hooks record the process name during local socket operations. A
  traffic-control eBPF program then sends matching traffic directly or to dae's
  transparent proxy. This is an early Linux traffic splitter, but it does not
  provide Almagest's other process, executable, user, or group identities.
- [Rebpf](https://github.com/one-d-wide/rebpf) can match Linux processes by
  executable pattern and can identify Flatpak applications. An eBPF
  `cgroup/sock_create` program
  [marks matching sockets](https://github.com/one-d-wide/rebpf#how), and Linux
  policy routing sends marked traffic to an interface. Rebpf can also scan
  existing sockets. It redirects traffic at the operating-system layer and does
  not provide a separate captured-flow routing graph. Its documentation states
  that only Flatpak matching comprehensively includes descendant processes.

Of these projects, only sing-box also combines 5-tuple owner lookup with early
bypass. Its early path provides direct bypass on Linux. Almagest provides early
routing on Linux and Windows, with the platform-specific modes shown above.

## Select the correct layer

- Use DefaultTun Include or Exclude for system-wide application selection.
- Use TunSplitter rules when already captured packets must take different graph
  paths and occasional unknown ownership has a safe result.
- Use [LocalProxy](@docs/node/almagest/LocalProxy) rules to limit which local
  applications can use that proxy.
- Use operating-system permissions, strict DefaultTun routing, and a
  [kill switch](@docs/almagest/killswitch) when bypass must fail closed.

Test rules with the exact process form that runs in service use. Launchers,
interpreters, child processes, containers, and updates can change the visible
executable path, command line, user, or process tree.
