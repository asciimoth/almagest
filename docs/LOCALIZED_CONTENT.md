# Localized content

Almagest has a file-based localization system for daemon-owned presentation
text. It is implemented for the admin panel, the documentation page, node
class descriptions, and Formular menus.

Localization is an `almagestd` concern. The reusable `lib` module supplies
stable IDs and English fallback text, but it does not read locale files.

## Bundles

Bundles are in `locales/<locale>/`:

```text
locales/<locale>/
  manifest.json
  panel.json
  menus.json
  nodes.json
  docs/
    index.json
    pages/
```

The repository contains:

- `en`: the complete English content;
- `lorem`: a development-only test locale. Its panel and menu catalogs are
  complete, but its node documentation is intentionally incomplete.

`manifest.json` has this format:

```json
{
  "format": 1,
  "locale": "en",
  "name": "English",
  "development_only": false
}
```

Locale names use a restricted, canonical BCP 47 form, for example `en`, `ru`,
or `pt-BR`. Almagest does not use parent-locale fallback: `pt-BR` falls back
directly to `en`.

## Content files

- `panel.json` maps stable UI keys to plain text. Named placeholders use the
  panel formatter. Catalog values cannot contain HTML.
- `nodes.json` maps a two-part class ID such as `almagest/Tun` to its short
  description.
- `menus.json` maps class, block, item, template, and dialog IDs to Formular
  presentation text. It can replace labels, help, placeholders, static text,
  dialog button and option labels, and resource alternative text. It does not
  replace IDs, values, conditions, logs, errors, or other runtime data.
- `docs/index.json` defines the nested documentation tree. A topic can set
  `node_pages: true` to insert pages for documented node classes. Topics and
  pages support `order`; pages support one bundle-wide `default`; topics and
  pages are folded by default.
- `docs/pages/**/*.md` contains documentation. A node class page is
  `docs/pages/nodes/<namespace>/<class>.md` and has the API ID
  `node/<namespace>/<class>`.

Catalog files are strict UTF-8 JSON. Markdown files are UTF-8 text. The reader
rejects duplicate JSON keys, unknown fields, unsafe paths, symbolic links,
non-regular files, invalid identifiers, and oversized input. Current limits
are 1 MiB per JSON catalog, 64 KiB per Markdown page, 4 KiB per value, 10,000
keys or documentation pages, and 128 locales.

## Runtime behavior

The locale belongs to one admin API session. Concurrent clients can use
different locales, and a locale change does not change graph state or rebuild
nodes.

Text lookup uses this order where the content type has a built-in value:

1. the requested locale;
2. English;
3. the compiled or engine-provided fallback.

Panel catalogs are merged per key. Node and menu values also fall back
independently. A missing or invalid requested documentation index uses the
English index; page content then falls back to the matching English file.
Missing locale data is not a startup error: the daemon logs a warning and uses
built-in text. An invalid `ALMAGEST_LOCALES_DIR` path is a startup error.

JSON catalogs reload after their file modification time or size changes. If a
new version is invalid, the reader keeps the last valid version. Markdown is
read on demand. Changes become visible after a new lookup, locale refresh, or
reconnect; there is no filesystem watcher or server push for locale changes.

The admin API provides `locale_snapshot` and `set_locale`. A snapshot contains
the requested and effective locale, available bundles, the resolved panel
catalog, and the content revision. The docs API provides `tree` and `page` on
its separate surface. The daemon localizes class snapshots, class
notifications, long descriptions, documentation responses, and copied
Formular messages at the API boundary.

The panel stores the selection in browser storage and a cookie, matches browser
languages when no selection exists, and keeps English literals as fallbacks.
Localized text is inserted through text and attribute APIs. Markdown uses the
existing limited renderer; locale content is never trusted as HTML.

## Paths and isolation

`ALMAGEST_LOCALES_DIR` overrides the bundle root and must be an absolute,
clean path. The defaults are:

- `/usr/share/almagest/locales` on Unix;
- `%ProgramFiles%\Almagest\locales` on Windows;
- `/locales` inside the isolated Linux runtime when that mount exists.

The development daemon scripts set the override to the checkout `locales/`
directory. In Linux production isolation, the network role opens the locale
root and gives the runtime role a held directory. The launcher mounts it
read-only at `/locales` with `nosuid`, `nodev`, and `noexec`, and grants only
read access through Landlock. Other isolated roles do not receive it.

Locale files are not copied into the panel static assets. No current package
rule installs them. A future package must install production bundles and must
exclude bundles with `development_only: true`, including `lorem`.

## Maintenance

Treat panel keys and Formular IDs as stable presentation API. When you add or
change localized UI:

1. update the English bundle and its compiled fallback;
2. keep the `lorem` panel and menu fixtures complete;
3. add English short and long documentation for production node classes;
4. run `just locales-check`.

`just check` runs this validation automatically. The validator checks bundle
structure and limits, English panel-key coverage, production node content,
known English menu classes, and `lorem` fixture coverage.

Localization does not cover the CLI, logs, configuration, protocol errors,
daemon diagnostics, machine-facing names and values, or arbitrary runtime
text.
