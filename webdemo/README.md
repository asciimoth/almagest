# Web demo

The tracked files in this directory define the landing page. The web demo reuses
the admin panel and documentation sources from `panel/` and `locales/`.

Build the complete static tree:

```sh
just build-webdemo
```

The default output is `build/webdemo/`. It is ignored by Git and can be copied
to any static HTTP server.

Serve it at the origin root:

```sh
just serve-webdemo
```

Serve it below a path, for example `http://127.0.0.1:8000/almagest/`:

```sh
just serve-webdemo base_path=almagest
```

The shorter positional form `just serve-webdemo almagest` is equivalent.

The build includes a Go Wasm backend and its matching Go runtime. The panel
loads the backend in the browser and uses the shared JSON admin API contract.
The backend runs a live engine with the Wasm-supported HTTP client, HTTP server,
and mock-network nodes. Graph mutations and node forms work through the shared
admin API. Config, KV, and secrets persist in browser `localStorage`; DNS is
in-memory. If browser storage is unavailable, the current Wasm instance uses an
in-memory fallback. If there is no saved config, the demo starts with an HTTP
client and server connected through a mock network. The graph is fitted and
centered after each full web demo load.

Static documentation previews and the demo SPA type colors use the tracked
`node-classes.json` cache. The cache contains production daemon node metadata
and keeps the daemon's node class display order. Green documentation marks use
the separate `available-node-classes.json` list of classes registered by the
Wasm build. Nodes outside that list do not get a mark. Update this list when
`lib/node_packages_wasm.go` changes.

Refresh the preview cache after a production node class, default port, or type
color changes:

```sh
just update-webdemo-node-classes
```

This command builds `almagestd`, runs its metadata-only dump mode, and replaces
the cache. A normal `build-webdemo` uses the cache and does not build or start
the daemon.
