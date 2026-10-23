# Web demo

The tracked files in this directory define the placeholder landing page. The
web demo reuses the admin panel and documentation sources from `panel/` and
`locales/`.

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

The demo panel uses the shared admin transport interface. Its current transport
reports that the browser engine is not included. A later WASM transport can use
the same JSON admin API contract without changing the panel UI.
