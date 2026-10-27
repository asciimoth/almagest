This page describes installed service state and privileged settings. Exact paths
can change when an installation uses CLI overrides, environment variables,
service isolation, or a non-system account. Read
[Configuration, KV, and secrets](@docs/almagest/data) for regular and protected
configuration roles. Read [Isolation and sandboxing](@docs/almagest/security)
for isolation modes.

## Linux package layout

The standard Linux package uses these paths:

- `/etc/almagest/alm.hujson`: regular configuration in direct service mode;
- `/etc/almagest/protected_alm.hujson`: protected configuration;
- `/var/lib/almagest`: persistent service data;
- `/run/almagest/admin.sock`: local administration socket;
- `/usr/share/almagest/locales`: localized interface text and documentation;
- `/usr/share/almagest/examples`: read-only packaged examples; and
- `/usr/lib/systemd/system/almagestd.service`: system service unit.

The package creates the `almagest` administrator group. Membership permits full
control through the default administration socket. Treat this group as
equivalent to Almagest administrator access.

In the complete isolated Linux layout, role-owned state is split further:

- `/var/lib/almagest/config/alm.hujson`: regular configuration;
- `/var/lib/almagest/runtime`: graph runtime storage;
- `/var/lib/almagest/broker`: privileged network recovery state; and
- `/var/lib/almagest/helper`: approved command-helper state.

The system service prepares this isolated layout before it starts. If it finds
data from direct mode in `/var/lib/almagest/store`, it migrates that data and
keeps the original directory as `/var/lib/almagest/store.pre-isolation`.

Do not move or change the ownership of these directories manually. An
interrupted migration contains state that the storage tool uses to continue
safely. Keep the service stopped and run this command:

```sh
almagest-isolation-storage recover
```

To return the storage to the direct layout, first make sure that Almagest has
recovered all host network changes. Then stop the service and run:

```sh
almagest-isolation-storage rollback
```

The systemd unit runs the service with elevated privileges and restarts it after
failure. Use a service drop-in for startup flags or environment overrides. The
upstream
[systemctl edit documentation](https://www.freedesktop.org/software/systemd/man/latest/systemctl.html)
explains how systemd creates these drop-ins. After a unit change, reload systemd
and restart the service. Read [Logs and diagnostics](@docs/almagest/logs) for
retention and log-level limits. Read the service log with:

```sh
journalctl --unit almagestd
```

## Windows package layout

The standard Windows package uses:

- `%ProgramData%\Almagest\alm.hujson`: regular configuration;
- `%ProgramData%\Almagest\protected_alm.hujson`: protected configuration;
- `%ProgramData%\Almagest\store`: persistent service data;
- `%ProgramData%\Almagest\logs\almagestd.log`: rotating service log;
- `%ProgramFiles%\Almagest\locales`: localized content; and
- `%ProgramFiles%\Almagest\examples`: packaged examples.

The machine administration pipe permits Local System, Administrators, and the
**Almagest Administrators** local group. The current-user pipe is identity
scoped. Protect group membership because the API can change graphs, routes,
secrets, and stored state.

## Configuration ownership

Keep protected configuration writable only by system administrators. It can
grant access to host networking, administration endpoints, secrets, TLS private
keys, command profiles, and sandbox executables.

Regular configuration can be writable through the administration API. The
service checks file safety unless `--unsafe-config` disables that check. Do not
use that option for a privileged service.

## Administration endpoints

Prefer the local Unix socket or Windows named pipe. The web listener binds to
loopback, but any local browser user can access an unfiltered listener. It has
no general browser authentication. Peer filters and filesystem or pipe access
rules are the primary authorization controls.

Do not enable cross-origin web access unless the host environment supplies a
separate trusted boundary. The option lets any browser origin use or embed the
administration interface.

### Administration endpoint options

Protected `Admin` contains these options:

- `Web` sets the loopback web port. `0` disables the web listener.
- `WebFilters` limits web clients by the owner of their local connection. This
  check is best effort.
- `WebPProf` enables the diagnostic `/debug/pprof/` handlers on the web
  listener.
- `AllowCrossOrigin` lets pages from any browser origin use or embed the web
  interface.
- `Unix` sets the Unix administration socket path. An empty value disables it.
- `UnixGroup` sets the group that owns the Unix socket.
- `UnixFilters` limits Unix-socket clients by operating-system peer credentials
  where supported.
- `Pipe` sets the Windows named-pipe path. An empty value disables it.

Both filter objects use the same fields. `UIDs` lists permitted user IDs, `GIDs`
lists permitted group IDs, and `Usernames` lists permitted account names. Web
filtering cannot be a security boundary. Prefer the Unix socket, the Windows
pipe, or the desktop app for trusted administration.

## Protected command allowlist

`CommandRunner.Commands` maps a stable name to each command that Almagest can
start for an approved integration. A graph or callback selects the name. It
cannot supply a new executable or an unrestricted argument list.

Each command entry has these options:

- `Command` contains the fixed executable and argument list. Arguments can
  contain declared `${slot_name}` placeholders.
- `Env` adds fixed environment variables to the child process.
- `UID` and `GID` select the Unix user and group for the child process.
- `Sync` runs the command in order with other synchronous commands.
- `Timeout` sets the synchronous-command timeout in nanoseconds. `0` selects the
  default.
- `Slots` defines and validates the values that callers can insert into
  arguments.
- `AllowNodes` makes the command available to `Command` nodes.

Read the [Command node](@docs/node/almagest/Command) page for the graph-side
settings.

Each `Slots` entry must set `MaxLength`. `Pattern` can require a complete
regular-expression match. `Required` requires a non-empty value. `AllowEmpty`
lets an optional slot contain an empty value. Do not use a shell to reinterpret
slot values.

## Host integration options

Protected `System.Type` selects `native` host integration or the `debug`
integration. `System.Linux`, `System.Windows`, and `System.Debug` contain the
settings for those integrations.

### Linux

`System.Linux.Features` requests individual host functions. If you specify the
`Features` object, omitted switches are false. Omit the complete object to use
the platform defaults.

- `Tun` creates regular system TUN interfaces for [Tun](@docs/node/almagest/Tun)
  nodes.
- `DefaultTun` creates a TUN that receives selected host traffic for
  [DefaultTun](@docs/node/almagest/DefaultTun) nodes.
- `DynTun` changes regular TUN settings while the interface exists.
- `DynDefaultTun` changes default-TUN settings while the interface exists.
- `TunNames` applies requested names to regular TUN interfaces.
- `DefaultTunNames` applies requested names to default-TUN interfaces.
- `StrictMode` supports strict default-TUN routing.
- `TunRules` supports source-route rules for default TUNs.
- `MatcherRules` supports the matchers described in
  [Application and process filtering](@docs/almagest/process-filtering).
- `DNSControl` changes host DNS settings for a default TUN.
- `Routing` changes host routes and routing rules.
- `Pmark` enables process-mark integration.
- `Killswitch` enables the supported
  [kill-switch integration](@docs/almagest/killswitch).

The other `System.Linux` options control these functions:

- `DNS` configures the host DNS provider.
- `KillswitchPath` sets a non-default path for the supported kill-switch
  endpoint.
- `Pmark` configures process-mark data.
- `Callbacks` selects protected commands to run after successful host changes.
- `AppBypassMark` sets the firewall mark used by Almagest sockets that bypass
  tunnel routing.
- `AppBypassMask` selects the significant bits of `AppBypassMark`.
- `UserMark` sets the firewall mark used for default-TUN include and exclude
  rules.
- `UserMarkMask` selects the significant bits of `UserMark`.
- `KillswitchAllowExclude` lets default-TUN exclude rules become kill-switch
  allow rules.

In `System.Linux.DNS`, `Mode` selects automatic detection, disables host DNS
changes, or selects `direct`, `openresolv`, `debian-resolvconf`,
`systemd-resolved`, or `resolved`. `ResolvconfInterface` sets the record name
for a resolvconf provider. `ResolvedInterfaceIndex` selects the interface used
with systemd-resolved. `FallbackServers` lists upstream addresses to use when
the original resolver is unavailable. Read [DNS](@docs/almagest/dns) for graph
resolver paths and DNS privacy.

In `System.Linux.Pmark`, `PinPath` sets the persistent kernel-object directory;
an empty value disables this integration. `TombCollectionEvents` sets the
cleanup interval as an event count. `TombTTL` sets the cleanup age in
nanoseconds. `Priority` sets the routing priority. A zero value for the last
three fields selects the platform default.

`System.Linux.Callbacks` can define `TunCreated`, `TunConfigured`,
`DefaultTunCreated`, `DefaultTunConfigured`, `DefaultTunClosed`,
`RoutingApplied`, `DNSConfigured`, and `KillswitchUpdated`. Each callback uses
`Command` to select a `CommandRunner.Commands` entry. Its `Slots` object maps
command slot names to templates made from that callback's data.

### Windows

Protected `System.Windows` has these options:

- `AdapterNamePrefix` sets the prefix for generated TUN adapter names.
- `StableGUID` assigns one stable adapter GUID. Concurrent TUN requests can
  collide when they use it.
- `UnderlaySelector` prefers an outbound interface name or GUID but does not
  require it.
- `Features` disables selected native functions.
- `OperationTimeout` sets the host-operation timeout with a duration such as
  `30s`.
- `Recovery` uses `inspect` to report abandoned state or `owned` to recover
  state after ownership is proved.

`System.Windows.Features.DisableIPv4` and `DisableIPv6` disable the applicable
address family. `DisableExclusions` disables executable exclusion rules.
`DisableMatchers` disables process-owner matching rules.

### Debug integration

The debug integration is for tests and development. Its switches simulate
missing platform features:

- `NativeOutNetwork` lets [OutNetwork](@docs/node/almagest/OutNetwork) use the
  operating-system network.
- `DisableTun` and `DisableDefaultTun` reject regular and default TUN creation.
- `DisableDynTun` and `DisableDynDefaultTun` reject live TUN changes.
- `DisableTunNames` and `DisableDefaultTunNames` reject requested TUN names.
- `DisableStrictMode` reports strict default-TUN routing as unavailable.
- `DisableDefaultTunSourceRoutes` reports default-TUN source routes as
  unavailable.

`System.Debug.DefaultTun` controls its test default TUN. `Builder` selects
`vtun-socks` or `pipe`. For `vtun-socks`, `SocksHost` and `SocksPort` set the
local SOCKS endpoint. `Name` sets the virtual TUN name. `NoLoopbackAddr`
prevents loopback addresses. `EndpointCh` and `EventCh` set the internal
endpoint and event queue sizes.

## Backups and recovery

An Almagest snapshot contains regular configuration and file storage. It does
not contain protected configuration, platform service settings, or every
external side effect. Back up those administrator files separately.

Before a routing, isolation, or sandbox-policy change:

1. Create and export a snapshot.
1. Back up regular and protected configuration with their permissions.
1. Keep a local administrator session that does not depend on the graph.
1. Apply the change and check service startup logs.
1. Test DNS, IPv4, IPv6, and failure recovery before you close the session.

A snapshot can contain secrets, private TLS material, DNS history, and other
operational data. Store it with access controls suitable for credentials. Read
[Snapshots and examples](@docs/almagest/snapshots) for the full snapshot scope
and restore procedure.
