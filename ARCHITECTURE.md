# ARCHITECTURE.md
Almagest is a Go userspace network stack built around a Pasta node graph.
- `lib/` is the reusable module. It owns the engine, built-in node classes,
  Almagest link types, and workspace-facing APIs.
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
`lib/` may depend on Pasta, Gonnect, Formular, Configer, and Bufpool. It should
not know about daemon config paths, process lifecycle, platform defaults, CLI
commands, or admin transports.

`bin/` imports `lib` through `bin/go.mod`. Keep daemon-only dependencies there.

## Key Files
Engine and workspace:
- `lib/engine.go`: `BuildEngine`, workspace API forwarding, notifications,
  persistence hooks, lifecycle.
- `lib/nodelist.go`: built-in node class registration.
- `lib/type_network.go`: `almagest/network` payload type and resource binding.

Built-in nodes:
- `lib/node_mock_net.go`: virtual loopback network node.
- `lib/node_http_server.go`: debug HTTP server over a received network.
- `lib/node_http_client.go`: HTTP client over a received network.

Daemon:
- `bin/almagestd/main.go`: startup sequence and shutdown context.
- `bin/almagestd/agent.go`: config path resolution, reading, fallback behavior.
- `bin/almagestd/config.go`: HuJSON parsing and config structs.
- `bin/almagestd/system.go`: debug/native system integration selection.
- `bin/almagestd/api.go`: localhost admin api listener.

Platform and CLI:
- `bin/almagestd/platform/`: build-tagged defaults and platform operations.
- `bin/almagest-cli/cli.go`: CLI placeholder.

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
`lib.BuildEngine(ctx, cfg, logf, sys, kv, dns, secrets, extra, pool)` builds one
workspace named `main`. Most public methods check the workspace ID and then
forward to Pasta.

Engine API groups:
- graph mutation and inspection
- copy/paste, undo/redo
- inbox and Formular messages
- subscriptions and full snapshots
- admin type color map for graph rendering
- workspace config save under a caller-provided `nodes` config view
- close of workspace and system integration

Notifications have two kinds:
- `workspace`: Pasta workspace notifications for `main`.
- `colormap`: type palette sent to subscribers before workspace events.

## Node Notes
`almagest/MockNetwork` creates a `gonnect.NewLoopbackNetwok()` network and emits
it on an `almagest/network` port.

`almagest/HttpServer` receives a network, host, port, and response string. It
listens with `network.Listen(ctx, "tcp", addr)`, serves a debug response, and
keeps recent worker logs for its Formular menu.

`almagest/HttpClient` receives a network, URL, method, and body. It builds an
HTTP transport that dials through `gonnect.Network`, runs requests on a worker,
and updates Formular result fields.

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
