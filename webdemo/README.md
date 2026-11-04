# Web demo

The tracked files in this directory define the landing page. The web demo reuses
the admin panel and documentation sources from `panel/` and `locales/`.

`index.html`, `main.css`, and `main.js` define the project main page. The page
uses the panel's Catppuccin Mocha colors. The screenshot carousel supports
touch, arrow buttons, and keyboard navigation. Installation tabs select platform
instructions. Without JavaScript, screenshots remain scrollable and all
installation instructions remain visible.

Landing-page copy uses `landing.*` keys in `locales/<locale>/panel.json`.
English text in `index.html` is the fallback when scripts or locale files are
unavailable. The build publishes panel catalogs for production locales only. The
page uses the same locale cookie and storage key as the panel and docs, and
shows a language selector when more than one production locale is available.

The generated `images/*.png` files are Chromium captures of the existing admin
panel. They use example graphs and fixed mock API data. They do not show live
network sessions. `images/orbit.svg` is the animated hero illustration. The
build renders missing PNG files before it copies `images/` to the published
tree. Existing PNG files are preserved.

## Render the interface images

Install the panel dependencies and make Chromium available on `PATH`, then run:

```sh
just render-webdemo-images
```

This compiles the panel and captures all seven scenes at 1500×720 CSS pixels
with a device scale factor of 1.5 (2250×1080 PNG files). It uses a loopback
server and a temporary browser profile. `--password-store=basic`, also used by
the graph image tool, prevents system secret-store prompts. Set `CHROMIUM_PATH`
or pass `--chromium /path/to/chromium` to select the browser. Node.js 22 or
later is required for its built-in WebSocket client.

Compare variations without replacing the generated images:

```sh
just render-webdemo-images --only graph,dns --output build/render-variants \
  --size 1500x720 --scale 2 --padding 56
```

Use `--zoom 0.9` to set the graph scale instead of automatic fitting. The script
fails if nodes extend outside the canvas. `--help` lists all options. After the
first compile, run `node panel/scripts/render-webdemo-images.mjs` directly for
faster iterations. `npm --prefix panel run webdemo:images -- --help` is also
available.

Edit `panel/scripts/webdemo-render/scenes.json`, or pass `--scenes FILE`, to
change the examples, positions, names, labels, palette, selected node, form
values, and saved snapshots. Example paths are relative to the repository.
Position, name, and label overrides use the original example node names.
`select` and `menus` use the displayed name. `nodeWidth` sets the minimum node
width (default: 180); this is the normal LiteGraph size property. Keep the image
width and height attributes in `index.html` in sync if the output ratio changes.

The capture entry point installs a mock admin transport, then loads the
production `app.js`. It reuses `panel/index.html`, `panel/style.css`, the graph
viewport fitter, LiteGraph, Formular, and the snapshot list. Node classes,
ports, and colors come from `node-classes.json`. Dynamic slots follow the
Sniffer and DnsFallback examples. Mock forms follow the HTTP client, Sniffer,
and TLS wrapper menu definitions in `lib/`; update the fixtures if these menus
change. The capture code is not included in production builds.

The script waits for API initialization, node forms, fonts, and canvas frames.
It rejects invalid ports, incompatible links, page errors, and unexpected image
sizes. It fixes the locale and timezone and hides the canvas FPS counter for
stable captures. Fonts and Chromium versions can still affect pixels. View each
new render before replacing the published set. The PNGs are ignored by Git.
GitHub CI installs Chrome for Testing and uses it for clean web demo builds.

## Build and serve

Build the complete static tree:

```sh
just build-webdemo
```

The default output is `build/webdemo/`. It is ignored by Git and can be copied
to any static HTTP server.

The build renders `social-preview.svg` as a 1280×640 PNG and publishes social
preview metadata for the landing, demo, and documentation pages. The metadata
uses `https://asciimoth.github.io/almagest/` by default. Set
`ALMAGEST_WEBDEMO_SITE_URL` to your site's full URL, ending in `/`, when you
publish the tree elsewhere.

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
