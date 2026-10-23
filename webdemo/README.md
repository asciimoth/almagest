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

The build includes a Go Wasm backend and its matching Go runtime. The panel loads
the backend in the browser and uses the shared JSON admin API contract. The
backend exposes the Wasm-supported node class list. Other reads return empty
data, and mutations fail because the backend is read-only.
