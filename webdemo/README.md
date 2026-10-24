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
in-memory fallback.

Static documentation previews and the demo SPA type colors use the tracked
`node-classes.json` cache. The cache also keeps the daemon's node class display
order. Refresh it after a production node class, default port, or type color
changes:

```sh
just update-webdemo-node-classes
```

This command builds `almagestd`, runs its metadata-only dump mode, and replaces
the cache. A normal `build-webdemo` uses the cache and does not build or start
the daemon.
