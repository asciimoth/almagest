# Web demo backend status

The web demo has a minimal live Go/Wasm backend. It runs the same Almagest
engine used by the daemon and exposes it through the shared JSON `v1` admin API.

## Runtime and persistence

- The engine stays active for the lifetime of the Wasm program.
- The editable HuJSON config is stored under
  `almagest.webdemo.config.v1` in browser `localStorage`.
- A missing config starts with an HTTP client and server connected through a
  mock network. A saved empty config is read as `{}`.
- Config text is pretty-formatted HuJSON. Parsing and engine edits preserve
  comments and other HuJSON trivia.
- If `localStorage` is absent, disabled, or throws an exception, the current
  Wasm instance uses an in-memory fallback.
- Engine graph mutations export the current graph back to the stored config.
- KV and secrets use their existing `localStorage` stores. DNS and TLS runtime
  dependencies are in memory. The TLS runtime provides a `default` client
  config with an empty certificate pool. The TLS admin placeholder contains no
  certificates, policies, or local certificate authority.
- Engine, workspace, and node logs go to the browser console and to a bounded
  256-entry cache used by the log API.

The Wasm build includes these runtime node classes:

| Class | Purpose |
| --- | --- |
| `almagest/HttpClient` | Sends HTTP requests through an attached network. |
| `almagest/HttpServer` | Serves HTTP responses on an attached network. |
| `almagest/MockNetwork` | Provides the virtual network used by the demo. |

These classes can be added, configured through node forms, linked, copied,
pasted, renamed, moved, removed, undone, and redone through the engine API.

## API surfaces

The `admin/hello` catalog advertises only the surfaces supported by the demo:

- `admin`: hello, ping, and color map.
- `config`: live snapshot and update with revision conflict checks.
- `dns`: in-memory cache snapshots and record mutations.
- `engine`: live snapshots, graph mutations, node menus, forms, workers, and
  engine notifications.
- `kv`: persistent store, subtree, and value reads and mutations.
- `log`: cached browser log snapshots and live entries.
- `secrets`: persistent secret reads and mutations with node validation.
- `snapshots`: an empty list with no packaged examples. Snapshot mutations are
  rejected, and export reports that the selected snapshot does not exist.
- `tls`: empty snapshot and subscription responses. There are no certificates,
  policies, or local CA, and TLS mutations are rejected.

Locale, documentation, and system administration are not advertised. Requests
for these surfaces or for locale methods return `invalid_method`. The web-demo
panel uses its built-in English text when the locale API is absent.

## Transport limits

Requests and responses use the synchronous `almagestWebDemoRequest` JavaScript
function. The backend queues engine events, and `almagestWebDemoEvents` drains
them. The panel drains this queue after requests and every 50 ms. This supports
engine snapshots, graph updates, node menus, and worker changes without a
WebSocket.

DNS, KV, secrets, and log subscribe methods return real initial snapshots.
Changes made through these admin surfaces are queued as live events. DNS, KV,
and secrets writes made by running nodes also update the API snapshots and
subscription event stream.

There is no authentication because the backend has no listener and all state is
restricted to the current browser origin.

## Source map

- `bin/cmd/almagest-webdemo/main.go`: Wasm startup, browser logging, and the
  JavaScript request/event functions.
- `bin/cmd/almagest-webdemo/local_storage.go`: `localStorage` with in-memory
  fallback.
- `bin/webdemo/live_backend.go`: admin, config, engine, KV, log, and secrets
  API handlers.
- `bin/webdemo/dns_api.go`: in-memory DNS administration.
- `bin/webdemo/config.go`: persistent HuJSON config and revision tokens.
- `bin/webdemo/placeholder_api.go`: empty, read-only snapshots and TLS admin
  placeholders.
- `bin/webdemo/logging.go`: browser logging and the bounded API cache.
- `bin/webdemo/storage.go`: Wasm-compatible KV, DNS, secrets, and TLS runtime
  storage.
- `panel/src/wasm_transport.ts`: synchronous responses and queued engine-event
  delivery.
