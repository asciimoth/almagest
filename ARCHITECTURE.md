# ARCHITECTURE.md

Almagest is a Go userspace network stack built around a Pasta node graph.

- `lib/` is the reusable module. It owns the engine, built-in node classes,
  workspace-facing APIs, and small subpackages for shared contracts.
- `lib/links/` owns shared Pasta link type identifiers, non-DNS link payloads,
  and output caches.
- `lib/nodekit/` owns node runtime interfaces, common node base helpers,
  workspace helpers, menu helpers, and node-facing aliases for shared contracts.
- `lib/amnesia/` owns the Amnesia and AmnesiaCtrl nodes. Amnesia owns an
  ephemeral WireGuard device and publishes its profile DNS through that device's
  TUN path. AmnesiaCtrl manages one tracked peer on a shared WireGuard device.
  Both import profiles through `wgo/amnesia`.
- `lib/dns/` owns DNS link payload contracts, DNS provider helpers, DNS node
  classes, built-in DNS nodes, and the UDP DNS adapter for an existing TUN data
  path.
- `lib/http/` owns HTTP client and server nodes.
- `lib/network/` owns network middleware nodes such as firewall, router,
  remapper, and spoofer. It also owns the mock network node.
- `lib/socks/` owns SOCKS client/server nodes and the local proxy node.
- `lib/tailscale/` owns the Tailscale control-plane node, its persistent
  identity cache, MagicDNS output, and ACL-derived firewall output.
- `lib/tun/` owns TUN nodes, including TUN firewall middleware, and TUN
  debug-log nodes.
- `lib/wireguard/` owns WireGuard nodes.
- `lib/debug/` owns debug-only helper nodes.
- `lib/tor/` owns the stable TorDriver node. The daemon registers it in direct
  and isolated runtime modes. It can launch only protected `gobox` programs.
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
  `bin/almagestd/static` for daemon embedding and `app/frontend` for Tauri.

Runtime path:

1. `almagestd` selects isolated or direct execution before it reads config.
1. Packaged Linux/amd64 replaces itself with a trusted network broker. The
   broker reads protected launch authority and starts restricted runtime,
   config-writer, and command-helper roles. Windows currently uses direct mode.
1. The selected network service provides the runtime with a `sysnet.System`
   implementation: local objects in direct mode or typed broker clients in
   isolated mode.
1. In isolated mode, the runtime wraps durable DNS cache storage with a shared
   10 MiB in-memory LRU. Cache hits stay in the runtime networking component;
   misses and all writes continue to use bbolt. Direct mode uses bbolt without
   this extra layer.
1. The runtime builds `lib.Engine` and creates the Pasta workspace named `main`.
1. Nodes exchange typed values; `almagest/network` carries `gonnect.Network`
   resources.
1. If web admin is enabled, the runtime serves embedded panel files and
   `/api/ws`. In isolated mode, the broker owns the public listener and relays
   accepted streams to the runtime.

See [`docs/DAEMON_ISOLATION.md`](docs/DAEMON_ISOLATION.md) for role ownership,
IPC, platform restrictions, packaging, and security-test scope. See
[`docs/SUBPROGRAM_SANDBOX.md`](docs/SUBPROGRAM_SANDBOX.md) for the subprogram
security model, effective profiles, protected policy, and current platform
limits.

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

## Nix Dependency Hashes

`flake.nix` pins the fetched dependency trees for Nix packaging:

- `almagest.vendorHash` covers the Go dependencies used by the `bin/` module,
  including local-module dependencies from `lib/`.
- `almagestApp.cargoHash` covers the Rust dependencies in `app/src-tauri/`.

Update the applicable hash when a change to `go.mod`, `go.sum`, `Cargo.toml`,
`Cargo.lock`, or a related local module changes that dependency tree. Do not
update these hashes for source-only changes. Run `just test-packaging-nix`; if
Nix reports a fixed-output hash mismatch, replace the applicable hash with the
reported `got` value. One mismatch can hide another, so rerun the command until
the complete packaging gate passes.

## Test Harness

With no selectors, `just check` runs the static checks, lints, the complete test
gate, and then all current-platform Go fuzz targets. `just fuzz` runs the fuzz
targets with a shared five-minute fuzzing budget. Set `FUZZ_TIME` to a Go
duration such as `10m` to change that budget.

`just test-total` runs the complete test gate without the static checks. With no
arguments, both commands keep the normal full-test workflow.

On Linux, tests use a private executable tmpfs directory when available. An
explicit `TMPDIR` overrides host scratch selection. Large Go build files, VM
disks, final coverage reports, and retained failure evidence stay on disk.
Docker cases use bounded tmpfs mounts for `/tmp` (256 MiB) and
`/run/almagest-e2e` (512 MiB). Case storage survives daemon restarts within the
same container. Shared service volumes use a 128 MiB tmpfs. Docker raw coverage
and the test-type scheduler's resource samples are staged in scratch space and
copied to disk at completion or failure; they do not survive a host reboot
during the run. Set `ALMAGEST_TEST_TMPFS=0` to test with disk-backed scratch and
container state.

For a change with local scope, pass one or more exact test selectors. Use the
narrow set of tests that the change adds, changes, or can affect:

```sh
just check go:lib/http:TestHTTPClientServerE2EMockNetwork e2e:static_dns_records
just check rust:endpoint::tests::rejects_invalid_endpoints
just test-total winvm:m8::daemon-user-named-pipe-cli-access winvm:m6::xtor-restricted-token
just test-total js:panel/tests/api_model.test.mjs
just test-total python:scripts.tests.test_cycles.CycleTests.test_go_leaf_and_package_failures
ALMAGEST_PACKAGE_VERSION=1.2.3 just test-total winpkg:integration
```

The selector forms are:

- `go:MODULE[/PACKAGE]:TEST` for one exact Go test, fuzz target, or subtest in
  `certcat`, `lib`, or `bin`.
- `rust:TEST` for one exact Rust test in the desktop app, including its module
  path.
- `e2e:CASE` for one directory name under `e2e/cases/`.
- `winvm:CASE-ID` for one Go case ID in `dev/winvm/test-suites.json`.
- `js:panel/tests/FILE.mjs` for one panel test file.
- `python:DOTTED-UNITTEST` for one unittest under `scripts/tests/` or
  `dev/winvm/tests/`.
- `winpkg:{build,smoke,lifecycle,integration}` for one Windows MSI build or
  installed-package WinVM gate. `ALMAGEST_PACKAGE_VERSION` selects its MSI
  `x.y.z` version and defaults to `0.0.0` for a local snapshot.

The harness validates selector syntax and referenced packages, cases, and files
before preparation starts. It also fails if an exact selected Go test does not
run. Selected Go tests still repair the Go workspace, generate the certificate
catalog, and build the panel assets. Selected Docker E2E cases still build or
reuse their required image. Selected WinVM cases build or reuse the base image
and stage the test payload. Multiple WinVM selectors use one VM invocation; the
harness runs only the required privilege phase when all selected cases use the
same phase. A selected run does not add unselected test suites or produce the
full aggregate coverage report.

When selectors are passed to `just check`, its lint and static-check
dependencies still run in full, but the full fuzz suite does not run. An exact
top-level `Fuzz...` Go selector runs only that fuzz target for `FUZZ_TIME`,
which defaults to five minutes. Use the complete `just check` without selectors
for significant changes that affect the whole project or large parts of it. The
narrow gate is for feedback on changes with local scope; it does not replace the
complete project gate when the affected area is broad.

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
- `lib/amnesia/`: Amnesia service and profile import, persistent client state,
  an ephemeral managed WireGuard runtime, tunnel DNS publication, and WireGuard
  controller integration.
- `lib/dns/`: DNS payload contracts, provider helpers, node classes, and
  built-in DNS nodes, including the raw UDP tunnel DNS adapter.
- `lib/http/`: HTTP client and server node classes.
- `lib/network/`: network firewall, router, remapper, and spoofer node classes.
- `lib/socks/`: SOCKS client/server and local proxy node classes.
- `lib/tailscale/`: Tailscale control-plane integration for a shared WireGuard
  device, including MagicDNS and firewall object outputs.
- `lib/tun/`: TUN, default TUN, VTun, TUN copy/join/manual-split/
  automatic-split/config-stripper, TUN firewall, and TUN debug-log node classes.
- `lib/wireguard/`: WireGuard node classes.
- `lib/debug/`: debug-only node classes.
- `lib/tor/`: TorDriver sandbox integration, isolated client Networks, and v3
  onion-service Networks.
- `lib/logging/`: logger interfaces and debug logger implementation.
- `lib/storage/`: KV, DNS cache, secret, and TLS config storage contracts.
- `lib/typecolor/`: graph type palette data.
- `lib/testkit/`: package-neutral assertions and E2E helper subpackages.

Built-in nodes:

- `lib/amnesia/`: Amnesia and AmnesiaCtrl implementations.
- `lib/dns/`: all DNS-owned node implementations.
- `lib/debug/`: debug-only node implementations.
- `lib/http/`: HTTP client and server node implementations.
- `lib/network/`: network router, remapper, and spoofer node implementations.
- `lib/socks/`: SOCKS and local proxy node implementations.
- `lib/tailscale/`: the Tailscale control-plane node and its DNS and firewall
  outputs.
- `lib/tor/`: the stable daemon-registered TorDriver node.
- `lib/tun/`: TUN node implementations.
- `lib/wireguard/`: WireGuard node implementation.
- `lib/network/node_mock_net.go`: virtual loopback network node.

Daemon:

- `bin/almagestd/main.go`: startup sequence and shutdown context.
- `bin/almagestd/cli_paths.go`: CLI config path resolution.
- `bin/almagestd/config.go`: HuJSON parsing and config structs.
- `bin/almagestd/daemonservices/localnetwork/`: direct debug/native host-network
  service selection and construction. It returns the original sysnet data-path
  objects without an IPC or proxy wrapper.
- `bin/almagestd/api.go`: localhost admin API listener and surface dispatch.
- `bin/almagestd/api_config.go`: editable HuJSON config API. It never exposes
  protected config and uses revision tokens for conflict detection.
- `bin/almagestd/snapshots.go`: snapshot archive creation, validation,
  file-storage restore, graph rebuild, and rollback.
- `bin/almagestd/examples.go`: read-only unpacked example discovery,
  localization, bounded staging, and snapshot-restore integration.
- `bin/almagestd/snapshot_bbolt.go`: consistent bbolt copies and transactional
  replacement of live database buckets.
- `bin/internal/isolation/`: trusted bootstrap, closed IPC contracts, network
  broker and runtime adapters, admin/config/command boundaries, role profiles,
  supervision, and recovery. Detailed wire contracts stay beside the code.
- `bin/native/linux-role/` and `bin/internal/isolation/linuxprofile/`: native
  Linux role restrictions, held-descriptor launch contracts, cgroup placement,
  and final Go thread synchronization. Packaged Linux/amd64 uses these
  components for isolated startup by default.
- `bin/internal/isolation/windowsprofile/`: Windows Server 2022/amd64 component
  restrictions. These include suspended launches, restricted tokens, distinct
  AppContainers, held trusted paths, exact pipe inheritance, mitigations, and
  bounded jobs. Complete Windows daemon isolation is not released.
- `bin/internal/isolation/supervision/`: bounded lifecycle coordination for an
  isolated process set and the broker-private, identity-driven host recovery
  journal.

Platform and CLI:

- `bin/almagestd/platform/`: build-tagged defaults and platform operations.
- `bin/almagest-cli/main.go`: CLI entry point.
- `bin/almagest-cli/commands.go`: CLI command registration and dispatch.

Admin panel:

- `panel/src/app.ts`: graph UI and transport-neutral admin API client.
- `panel/src/admin_transport.ts`: daemon WebSocket transport and the transport
  selection boundary.
- `panel/src/desktop_admin_transport.ts`: capability-restricted Tauri admin
  channel adapter.
- `panel/src/wasm_transport.ts`: in-process web demo transport.
- `panel/src/docs.ts`: documentation surface shared by daemon, static, and
  desktop clients.
- `panel/src/embedded_docs_client.ts` and `embedded_docs_protocol.ts`: desktop
  documentation bridge that keeps the panel document and its native API session
  alive while docs are visible.
- `panel/src/pasta_model.ts`: snapshot/notification helpers.
- `panel/scripts/`: dependency vendoring and static publish scripts.

Localized documentation:

- `locales/<locale>/nodes.json`: localized node class short descriptions.
- `locales/<locale>/docs/index.json`: localized nested topic and page tree.
- `locales/<locale>/docs/pages/`: localized Markdown pages. Runtime node pages
  are below `nodes/<namespace>/<class>.md` and use `node/<namespace>/<class>` as
  their stable API ID.

Editable config flow:

1. A client reads the `config.snapshot` API result and keeps its revision.
1. The client submits a complete HuJSON document to `config.update` with that
   revision. A top-level `Protected` member is rejected.
1. The engine compares saved node-owned config and semantic links. It removes
   obsolete links first, reconstructs only changed nodes, adds new links, and
   keeps unrelated records live.
1. The daemon exports the resulting workspace into the submitted HuJSON, writes
   it immediately, and returns the new canonical text and revision.

The API is read-only when the daemon config source is read-only. Protected
config is loaded once at startup and is not part of this flow.

Snapshot flow:

1. `snapshots.create` writes `file-storage/snapshots/<uuid>.zip`. The archive
   contains the regular config, a consistent bbolt image, and all file-storage
   content except paths in the daemon exclusion list. The `snapshots` subtree
   itself is currently the only hard-coded exclusion.
1. The daemon removes every top-level `Protected` member from the archived
   regular config. It also excludes the active regular and protected config
   source files if they are inside file storage. Restore keeps their parent
   directories when the archive does not contain those directories.
1. `snapshots.restore` validates and stages the complete archive, saves an
   internal recovery archive, removes the current graph, mirrors storage, and
   replaces all bbolt buckets in one transaction. It then reapplies protected
   and host-derived state and reconstructs the graph from the restored config.
1. Engine subscribers keep their previous graph view during restore. After the
   restored graph or rollback state is stable, the engine drains notifications
   that were already queued and publishes one authoritative workspace snapshot.
   It also publishes the matching full worker snapshot because worker state is a
   separate engine notification type. A blocked client does not block the
   restore request; its final publication continues after that callback returns.
1. If restore fails, the daemon uses the recovery archive to restore the prior
   in-memory and persistent state. A later config-file write failure does not
   roll back a successful restore; the restored config stays active in memory.
1. `snapshots.export` returns the stored ZIP and `snapshots.import` validates a
   ZIP before publishing it without replacing an existing UUID. Import can
   preserve the archived display name or replace it with an optional name.
1. Snapshot create, delete, import, rename, and restore operations exclude
   concurrent admin API state operations. Listing and export remain read-only.
   The CLI uses `almagest-cli snapshots`; the panel provides the same archive
   and snapshot-store operations. Panel downloads use `<uuid>.zip` names.
1. `almagestd --snapshot PATH` validates and restores an archive before it opens
   bbolt or builds the engine. `--snapshot -` reads the archive from stdin.
   Protected config and the snapshot-store subtree remain host-local. The daemon
   first copies the closed storage state and restores that copy if startup
   restore changes some files and then fails.
1. A compressed archive can be at most 32 MiB. Its extracted storage entries can
   be at most 256 MiB in total. The manifest can be at most 64 KiB, the regular
   config can be at most 8 MiB, and a display name can be at most 256 UTF-8
   bytes. Stream and WebSocket transports also have a finite JSON message limit
   that includes base64 expansion and envelope data.
1. A stored symlink must use a relative target. Its complete target chain must
   resolve inside the staged storage tree and outside all excluded paths.
   Dangling symlinks, absolute targets, and links that escape storage make the
   archive invalid. The daemon applies the same rule during create and import.

## Core Concepts

- Pasta provides the graph workspace, node classes, ports, links, notifications,
  menus, copy/paste, undo/redo, and standard link types.
- Gonnect provides network abstractions used by Almagest nodes.
- Formular provides node menu/form messages used by the HTTP nodes.
- Configer/HuJSON provide config parsing and future persistence.
- `sysnet.System` abstracts host/system network integration.

## Engine Notes

The `lib.BuildEngine` function builds one workspace named `lib.MainWorkspace`
(`"main"`). Most public methods check the workspace ID and then forward to
Pasta.

Engine API groups:

- graph mutation and inspection
- copy/paste, undo/redo
- inbox and Formular messages
- subscriptions and full snapshots
- worker snapshots and worker notifications
- admin type color map for graph rendering
- workspace config save under a caller-provided `nodes` config view
- partial live reconciliation from a complete `nodes` config view; unchanged
  node and link records keep their IDs and runtime resources
- accessors for buffer pool, KV storage, DNS cache manager, secret storage, and
  TLS config storage
- close of workspace and system integration

Notifications have two kinds:

- `workspace`: Pasta workspace notifications for `main`.
- `colormap`: type palette sent to subscribers before workspace events.

## Node Notes

`almagest/MockNetwork` creates a `gonnect.NewLoopbackNetwork()` network and
emits it on an `almagest/network` port.

`almagest/HttpServer` is a debug and tracing tool. It receives a network, host,
port, and response string. It listens with `network.Listen(ctx, "tcp", addr)`,
serves a debug response, and keeps recent worker logs for its Formular menu.

`almagest/HttpClient` is a debug and tracing tool. It receives a network, URL,
method, and body. It builds an HTTP transport that dials through
`gonnect.Network`, runs requests on a worker, and updates Formular result
fields.

`almagest/FetchAddr` queries a selected public connection-information service
through a received network. It normalizes the public IP address, country code,
city, latitude, and longitude, and emits success or failure triggers after its
data outputs are updated.

Network payloads own closeable resources. Use `BindNetworkResource` when a node
receives or emits a network so Pasta cleanup can close it.

TUN providers use `TunOutputCache` to keep one detached read root for each
native source. Per-link outputs are children of that root. A ready root is
reused, but a failed root or a new native source gets a new root. DefaultTun
captures the sysnet source generation before each rebuild because its stable
public wrapper can change native source during the build call. TunSplitter and
TunJoiner observe terminal input errors and down events. They use link and
attachment generations when they request replacements. TunJoiner tracks its
default input and each secondary input separately.

## Daemon Notes

Important flags and env:

- `--stdin`: read main config from stdin and make it read-only.
- `--snapshot`, `ALMAGEST_SNAPSHOT`: restore startup state from an archive path.
  Use `--snapshot -` for stdin; this cannot be combined with `--stdin`.
- `-c, --config`, `ALMAGEST_CONF`: main config path or directory.
- `-p, --protected`, `ALMAGEST_PROTECTED_CONF`: protected config path.
- `-l, --lvl`, `ALMAGEST_LOG_LVL`: logging level.
- `--log-workers`: log engine worker lifecycle events.
- `--no-isolation`: run the direct single-process daemon. Linux and Windows use
  direct mode by default. The Linux default is temporary because the current
  isolation implementation has performance issues. Packaged Linux services
  continue to select isolation explicitly. Other unsupported profiles fail with
  guidance to select the flag explicitly.
- `--experimental`: enable experimental features and node classes. They are
  disabled by default and require direct mode. On platforms where isolation is
  selected, use `--no-isolation` to select direct mode.
- `--isolation-memory-bytes`, `--isolation-tasks`, `--isolation-cpu-quota`,
  `--isolation-descriptors`, and `--isolation-file-bytes`: Linux isolation
  startup ceilings. Each has a matching `ALMAGEST_ISOLATION_*` environment
  variable; CLI values take precedence. Defaults are 8 GiB memory, 4096 tasks,
  64 CPUs of quota per role, 65536 descriptors per process, and 1 TiB per file.
  See `docs/DAEMON_ISOLATION.md` for units, ranges, and service configuration.
- `-w, --web`, `ALMAGEST_WEB_ADMIN`: localhost web admin port.
- `--web-filter-uid`, `ALMAGEST_WEB_ADMIN_FILTER_UIDS`: allowed web admin peer
  owner UIDs.
- `--web-filter-gid`, `ALMAGEST_WEB_ADMIN_FILTER_GIDS`: allowed web admin peer
  owner GIDs.
- `--web-filter-user`, `ALMAGEST_WEB_ADMIN_FILTER_USERS`: allowed web admin peer
  owner usernames.
- `--web-allow-cross-origin`, `ALMAGEST_WEB_ADMIN_ALLOW_CROSS_ORIGIN`: allow any
  browser origin to access or embed the web admin. This is unsafe and is
  disabled by default.
- `--file-storage`, `ALMAGEST_FILE_STORAGE`: almagestd file storage directory.
- `--tmp-file-storage`, `ALMAGEST_TMP_FILE_STORAGE`: create random temporary
  file storage prefixed with `alm-store-` and remove it on exit.
- `ALMAGEST_TLS_FORCE_FALLBACK_ROOTS`: when set, use embedded NSS fallback TLS
  roots instead of OS certificate storage.

On Linux, persistent file storage defaults to `~/.local/share/almagest/store`
for non-root users and `/var/lib/almagest/store` when running as root or when no
current-user home is available.

If `--config` points at a directory, use `alm.hujson` inside it. If protected
config is omitted, use `protected_alm.hujson` next to the main config. Protected
config currently includes read-only daemon settings such as `Admin.Web`,
`Admin.WebFilters`, and `System`; CLI args override admin listener settings in
memory. When `--stdin` is used, the stdin config may include those protected
settings under a top-level `Protected` key instead of using a separate protected
config file.

The web admin rejects requests with a different browser origin by default. It
also sends browser headers that prevent other origins from embedding the panel
or loading its resources. Origin-less clients, including `almagest-cli`, are not
affected. Set protected `Admin.AllowCrossOrigin` or use
`--web-allow-cross-origin` to disable these browser protections. This setting
allows every origin and is not an authentication mechanism.

System integration is selected from `Protected.System.Type`:

- `debug`: `sysnetdebug.System{}`.
- `native` or empty: `platform.SystemIntegration`.

Use debug integration for local runnable experiments. On Windows, native
integration uses `sysnet-windows`. Other platforms currently use the platform
implementation selected by their build tags.

### Windows integration

Windows host integration stays in `bin/` and is selected with build tags:

- `platform_windows.go` owns known-folder paths, private file handling, and
  construction of the native `sysnet-windows` system.
- `runtime_dependencies_windows.go` loads Wintun only from the executable
  directory or `System32`. Provisioning tools, not the daemon, install Wintun
  and the split-tunnel driver service.
- `api_pipe_windows.go` creates the SID-scoped interactive administration pipe
  or the Local System service pipe. The machine pipe grants the package-created
  `Almagest Administrators` local group. The CLI verifies the expected pipe
  owner.
- `api_web_filter_windows.go` implements the optional best-effort username/SID
  filter for the loopback web listener. It is not authentication.
- `tls_authorities_windows.go` reads public certificates from the current
  process identity and machine Root stores. TLS policies must explicitly select
  catalog entries before they become trust anchors or constraints.
- `command/process_windows.go` and `platform/lifecycle_windows.go` own Job
  Object cleanup and console/Service Control Manager lifecycle handling.

`contracts/windows/support-matrix.json` is the feature and platform support
contract. `contracts/windows/test-manifest.json` defines the evidence required
to qualify each claim. The reusable graph and node code remains in `lib/` and
does not import the Windows backend.

`packaging/windows/` defines the unsigned Windows Server 2022/amd64 MSI.
`scripts/package-windows.sh` runs its locked WinVM build and adds the MSI,
checksum, and build evidence to `dist/`. `just test-windows-packaging` joins the
reproducibility, lifecycle, and installed-package integration gates. These
package gates do not change entries in the Windows support contract.

Before loading config, almagestd resolves path-valued CLI args to absolute
paths. After config parsing and before daemon resources are started, it sets up
file storage and changes the process working directory to that storage
directory. This keeps accidental relative file writes inside the daemon storage
area without breaking relative paths supplied at startup.
