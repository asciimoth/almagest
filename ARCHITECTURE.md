# ARCHITECTURE.md
Almagest is a Go userspace network stack built around a Pasta node graph.
- `lib/` is the reusable module. It owns the engine, built-in node classes,
  workspace-facing APIs, and small subpackages for shared contracts.
- `lib/links/` owns shared Pasta link type identifiers, non-DNS link payloads,
  and output caches.
- `lib/nodekit/` owns node runtime interfaces, common node base helpers,
  workspace helpers, menu helpers, and node-facing aliases for shared
  contracts.
- `lib/dns/` owns DNS link payload contracts, DNS provider helpers, DNS node
  classes, and built-in DNS nodes.
- `lib/http/` owns HTTP client and server nodes.
- `lib/network/` owns network middleware nodes such as firewall, router,
  remapper, and spoofer. It also owns the mock network node.
- `lib/socks/` owns SOCKS client/server nodes and the local proxy node.
- `lib/tun/` owns TUN nodes, including TUN firewall middleware, and TUN
  debug-log nodes.
- `lib/wireguard/` owns WireGuard nodes.
- `lib/debug/` owns debug-only helper nodes.
- `lib/logging/` owns reusable logging interfaces and the in-memory debug
  logger.
- `lib/storage/` owns daemon-provided storage interfaces and error contracts,
  including DNS cache storage contracts.
- `lib/typecolor/` owns graph type palette data.
- `lib/testkit/` owns package-neutral test helpers and domain-specific E2E
  helper subpackages.
- `bin/` is the runnable module. It owns daemon startup, config files, platform
  integration, admin API plumbing, and the CLI placeholder.
- `panel/` is the TypeScript admin UI. `just build-web` compiles it into
  `bin/almagestd/static` for daemon embedding.

Runtime path:
1. `almagestd` reads HuJSON config.
2. It selects a `sysnet.System`.
3. It prepares the localhost admin listener.
4. It builds `lib.Engine`.
5. The engine creates the Pasta workspace named `main`.
6. Nodes exchange typed values; `almagest/network` carries `gonnect.Network`
   resources.
7. If web admin is enabled, `almagestd` serves embedded static panel files and
   `/api/ws`.

## Module Boundaries
`lib/` may depend on Pasta, Gonnect, Formular, Configer, and Bufpool. Root
package files keep only the engine API, engine worker API, nodekit runtime
bridge, built-in class registration, and node class list glue. Domain node
packages expose their own classes and public contracts. Their package names use
a `nodes` suffix, such as `dnsnodes`, to avoid collisions with standard library
or Gonnect package names in code that imports multiple domains. Shared link,
runtime, logging, storage, type-color, and test behavior belongs in the matching
`lib/links`, `lib/nodekit`, `lib/logging`, `lib/storage`, `lib/typecolor`, or
`lib/testkit` package. Production subpackages must not import root
`github.com/asciimoth/almagest/lib`. Library code should not know about daemon
config paths, process lifecycle, platform defaults, CLI commands, or admin
transports.

`bin/` imports `lib` through `bin/go.mod`. Keep daemon-only dependencies there.

## Key Files
Engine and workspace:
- `lib/engine.go`: `BuildEngine`, workspace API forwarding, notifications,
  persistence hooks, lifecycle.
- `lib/worker.go`: engine worker tracking, worker snapshots, and worker
  notifications.
- `lib/node_packages.go`: central registration for node subpackages.
- `lib/nodelist.go`: root built-in node class registration and filtering.
- `lib/nodekit_runtime.go`: adapter that makes the root engine implement
  `nodekit.Runtime`.
- `lib/links/`: shared link type identifiers, `almagest/network` and
  `almagest/tun` payload types, resource binding helpers, and output caches.
- `lib/nodekit/`: node runtime interface, node base, worker helpers, workspace
  helpers, menu helpers, TLS selector helpers, and node-facing aliases to
  `links` and `storage`.
- `lib/dns/`: DNS payload contracts, provider helpers, node classes, and
  built-in DNS nodes.
- `lib/http/`: HTTP client and server node classes.
- `lib/network/`: network firewall, router, remapper, and spoofer node classes.
- `lib/socks/`: SOCKS client/server and local proxy node classes.
- `lib/tun/`: TUN, default TUN, VTun, TUN copy/join/split/config-stripper,
  TUN firewall, and TUN debug-log node classes.
- `lib/wireguard/`: WireGuard node classes.
- `lib/debug/`: debug-only node classes.
- `lib/logging/`: logger interfaces and debug logger implementation.
- `lib/storage/`: KV, DNS cache, secret, and TLS config storage contracts.
- `lib/typecolor/`: graph type palette data.
- `lib/testkit/`: package-neutral assertions and E2E helper subpackages.

Built-in nodes:
- `lib/dns/`: all DNS-owned node implementations.
- `lib/debug/`: debug-only node implementations.
- `lib/http/`: HTTP client and server node implementations.
- `lib/network/`: network router, remapper, and spoofer node implementations.
- `lib/socks/`: SOCKS and local proxy node implementations.
- `lib/tun/`: TUN node implementations.
- `lib/wireguard/`: WireGuard node implementation.
- `lib/network/node_mock_net.go`: virtual loopback network node.

Daemon:
- `bin/almagestd/main.go`: startup sequence and shutdown context.
- `bin/almagestd/cli_paths.go`: CLI config path resolution.
- `bin/almagestd/config.go`: HuJSON parsing and config structs.
- `bin/almagestd/system.go`: debug/native system integration selection.
- `bin/almagestd/api.go`: localhost admin api listener.

Platform and CLI:
- `bin/almagestd/platform/`: build-tagged defaults and platform operations.
- `bin/almagest-cli/main.go`: CLI entry point.
- `bin/almagest-cli/commands.go`: CLI command registration and dispatch.

Admin panel:
- `panel/src/app.ts`: graph UI and WebSocket admin API client.
- `panel/src/pasta_model.ts`: snapshot/notification helpers.
- `panel/scripts/`: dependency vendoring and static publish scripts.

## Core Concepts
- Pasta provides the graph workspace, node classes, ports, links,
  notifications, menus, copy/paste, undo/redo, and standard link types.
- Gonnect provides network abstractions used by Almagest nodes.
- Formular provides node menu/form messages used by the HTTP nodes.
- Configer/HuJSON provide config parsing and future persistence.
- `sysnet.System` abstracts host/system network integration.

## Engine Notes
`lib.BuildEngine(ctx, cfg, logf, sys, kv, dns, secrets, tlsConfigs, extra,
pool, debug)` builds one workspace named `lib.MainWorkspace` (`"main"`). Most
public methods check the workspace ID and then forward to Pasta.

Engine API groups:
- graph mutation and inspection
- copy/paste, undo/redo
- inbox and Formular messages
- subscriptions and full snapshots
- worker snapshots and worker notifications
- admin type color map for graph rendering
- workspace config save under a caller-provided `nodes` config view
- accessors for buffer pool, KV storage, DNS cache manager, secret storage, and
  TLS config storage
- close of workspace and system integration

Notifications have two kinds:
- `workspace`: Pasta workspace notifications for `main`.
- `colormap`: type palette sent to subscribers before workspace events.

## Node Notes
`almagest/MockNetwork` creates a `gonnect.NewLoopbackNetwork()` network and emits
it on an `almagest/network` port.

`almagest/HttpServer` is a debug and tracing tool. It receives a network, host,
port, and response string. It listens with `network.Listen(ctx, "tcp", addr)`,
serves a debug response, and keeps recent worker logs for its Formular menu.

`almagest/HttpClient` is a debug and tracing tool. It receives a network, URL,
method, and body. It builds an HTTP transport that dials through
`gonnect.Network`, runs requests on a worker, and updates Formular result
fields.

Network payloads own closeable resources. Use `BindNetworkResource` when a node
receives or emits a network so Pasta cleanup can close it.

## Daemon Notes
Important flags and env:
- `--stdin`: read main config from stdin and make it read-only.
- `-c, --config`, `ALMAGEST_CONF`: main config path or directory.
- `-p, --protected`, `ALMAGEST_PROTECTED_CONF`: protected config path.
- `-l, --lvl`, `ALMAGEST_LOG_LVL`: logging level.
- `-w, --web`, `ALMAGEST_WEB_ADMIN`: localhost web admin port.
- `--web-filter-uid`, `ALMAGEST_WEB_ADMIN_FILTER_UIDS`: allowed web admin
  peer owner UIDs.
- `--web-filter-gid`, `ALMAGEST_WEB_ADMIN_FILTER_GIDS`: allowed web admin
  peer owner GIDs.
- `--web-filter-user`, `ALMAGEST_WEB_ADMIN_FILTER_USERS`: allowed web admin
  peer owner usernames.
- `--file-storage`, `ALMAGEST_FILE_STORAGE`: almagestd file storage directory.
- `--tmp-file-storage`, `ALMAGEST_TMP_FILE_STORAGE`: create random temporary
  file storage prefixed with `alm-store-` and remove it on exit.
- `ALMAGEST_TLS_FORCE_FALLBACK_ROOTS`: when set, use embedded NSS fallback
  TLS roots instead of OS certificate storage.

On Linux, persistent file storage defaults to
`~/.local/share/almagest/store` for non-root users and
`/var/lib/almagest/store` when running as root or when no current-user home is
available.

If `--config` points at a directory, use `alm.hujson` inside it. If protected
config is omitted, use `protected_alm.hujson` next to the main config. Protected
config currently includes read-only daemon settings such as `Admin.Web`,
`Admin.WebFilters`, and `System`; CLI args override admin listener settings in
memory. When `--stdin` is used, the stdin config may include those protected
settings under a top-level `Protected` key instead of using a separate protected
config file.

System integration is selected from `Protected.System.Type`:

- `debug`: `sysnetdebug.System{}`.
- `native` or empty: `platform.SystemIntegration`.

Use debug integration for local runnable experiments. Native platform
integration is currently a placeholder.

Before loading config, almagestd resolves path-valued CLI args to absolute
paths. After config parsing and before daemon resources are started or
privileges are dropped, it sets up file storage and changes the process working
directory to that storage directory. This keeps accidental relative file writes
inside the daemon storage area without breaking relative paths supplied at
startup.
