# Localized content maintenance

This directory contains the text that `almagestd` shows in the admin panel, the
documentation site, node descriptions, and Formular menus. Localization is a
daemon concern. The reusable `lib` module supplies stable IDs and built-in
fallback text, but it does not read these files.

Use this document when you add a locale, add documentation, or change localized
text. For the system design and deployment details, see
[`docs/LOCALIZED_CONTENT.md`](../docs/LOCALIZED_CONTENT.md).

## Quick maintenance procedure

When you add or change localized content:

1. Update the English content first. English defines the complete catalog and
   documentation contract.
1. Keep stable keys, class IDs, documentation IDs, file paths, placeholders, and
   Formular addresses unchanged in translations.
1. Add both an English short description and an English node page when you add a
   production node class.
1. Run `just locales-check` from the repository root. Use `just check` when the
   change can affect more than localized content.

The daemon reloads JSON catalogs after their modification time or size changes.
It reads Markdown pages on demand. There is no file watcher or server push.
Refresh the locale, reconnect, or make a new lookup to see a change.

## Directory structure

Each locale is one complete bundle:

```text
locales/
  README.md
  en/
    manifest.json
    panel.json
    menus.json
    nodes.json
    docs/
      index.json
      pages/
        guides/
          welcome.md
        nodes/
          <namespace>/
            <Class>.md
  <locale>/
    ...same required files and directories...
```

The repository has these special bundles:

- `en` is the production English bundle and the primary fallback.
- `lorem` is a development-only test bundle. Its catalogs can be partial to
  exercise English and built-in fallback behavior.

Only `README.md` and locale directories are permitted directly below `locales/`.
All bundle files are required, including `docs/index.json` and the `docs/pages/`
directory.

## Add a locale

1. Copy `locales/en` to a directory with the canonical locale name.
1. Edit `manifest.json` and translate presentation values. Do not translate
   keys, IDs, file paths, class names, option values, or placeholders.
1. Keep untranslated entries or remove optional translation entries so that the
   normal fallback can supply them. Do not put an empty string in a catalog:
   empty values are invalid or do not override a fallback.
1. Run `just locales-check`.

Locale names use a restricted canonical BCP 47 form and have a maximum length of
35 characters. Examples are `en`, `ru`, `pt-BR`, and `zh-Hant`. Language subtags
are lowercase, two-letter region subtags are uppercase, and four-letter script
subtags use title case. The directory name and the `manifest.json` locale value
must be identical.

Almagest does not use a parent-locale content fallback. For example, `pt-BR`
falls back directly to `en`, not to `pt`. Browser-language selection can match
an available parent tag, but content lookup does not merge parent bundles.

## Bundle files

### `manifest.json`

The manifest makes a bundle available for selection:

```json
{
  "format": 1,
  "locale": "en",
  "name": "English",
  "development_only": false
}
```

- `format` must be `1`.
- `locale` must equal the directory name.
- `name` is the non-empty, user-visible locale name. Write it in that locale.
- `development_only` identifies a fixture that packages must exclude. The
  `lorem` bundle must set it to `true`.

### `panel.json`

This file maps stable UI keys to plain text:

```json
{
  "action.save": "Save",
  "app.status.failed": "Panel failed: {error}"
}
```

Values are not HTML. The panel inserts them as text or safe attribute values.
Preserve each named placeholder, such as `{error}`, `{count}`, or `{name}`.
Placeholders have the form `{key}` where `key` contains letters, digits,
periods, underscores, or hyphens. The formatter leaves an unknown placeholder
unchanged.

English keys must cover the `data-i18n` attributes in panel HTML and calls to
the localization functions in panel TypeScript. The validator checks this
coverage. Other locales, including `lorem`, can omit keys and use fallback text.

### `nodes.json`

This file maps a stable two-part node class ID to a short, single-line
description:

```json
{
  "almagest/Tun": "TUN interface"
}
```

Do not translate the `<namespace>/<Class>` key. The English catalog and an
English Markdown node page are required for each production node class. The
short description is also used as the node-page name in the documentation tree.

### `menus.json`

This file translates presentation fields in Formular messages. Its top-level
keys are node class IDs. A class can contain `blocks` and `dialogs`:

```json
{
  "example/Node": {
    "blocks": {
      "config": {
        "items": {
          "address": {
            "label": "Address",
            "help": "Address to use.",
            "placeholder": "example.test"
          }
        }
      }
    },
    "dialogs": {
      "confirm": {
        "title": "Confirm",
        "text": "Apply this change?",
        "yes_label": "Apply",
        "no_label": "Cancel",
        "options": { "safe": "Safe mode" },
        "resources": { "icon": "Confirmation icon" }
      }
    }
  }
}
```

Item presentation fields are `label`, `text`, `help`, and `placeholder`. Items
can also have `templates`; each template can have a `label` and nested `items`.
Dialog fields are `title`, `text`, `placeholder`, `yes_label`, `no_label`,
`submit_label`, `cancel_label`, `options`, and `resources`.

Keep all class, block, item, template, dialog, option, and resource addresses
unchanged. In particular, option map keys are runtime values and resource map
keys are resource IDs. Localization changes only presentation text. It does not
change conditions, IDs, values, logs, errors, or other runtime data.

The English menu can contain only known production classes plus
`almagest/Command`. Other locales, including `lorem`, can contain a partial menu
structure and use field-level fallback text.

### `docs/index.json`

This file defines the documentation navigation tree. It contains nested topics
and explicit pages:

```json
{
  "topics": [
    {
      "id": "guides",
      "name": "Guides",
      "order": 1,
      "folded": false,
      "pages": [
        {
          "id": "guide/welcome",
          "name": "Welcome",
          "file": "guides/welcome.md",
          "order": 1,
          "default": true
        }
      ],
      "topics": []
    },
    {
      "id": "nodes",
      "name": "Nodes",
      "order": 2,
      "node_pages": true
    }
  ]
}
```

A topic supports these fields:

- `id`: stable ID that is unique across all topics and explicit pages;
- `name`: localized navigation text;
- `folded`: optional initial state; omitted means folded;
- `order`: optional integer from 0 through 10,000; entries with an order come
  before entries without one, and equal entries keep file order;
- `topics`: optional child topics;
- `pages`: optional explicit pages;
- `node_pages`: optional Boolean. When true, the daemon inserts documented node
  classes at this position.

An explicit page supports `id`, `name`, `file`, `folded`, `order`, and
`default`. The `id` is the stable API and URL ID. The `file` is relative to
`docs/pages/`. At most one page in the complete bundle can set `default` to
`true`. The tree must have at least one topic.

For a translation, copy the English tree and translate only `name` values. Keep
topic IDs, page IDs, files, ordering, and node-page positions stable unless you
intentionally change the navigation contract. This also makes page fallback
work: the requested index selects a file, then the English bundle must have the
same file when the translation is absent.

### `docs/pages/**/*.md`

These files contain UTF-8 Markdown. An explicit page can use any valid path that
its index names. A node class page has a fixed path and ID:

```text
File: docs/pages/nodes/<namespace>/<Class>.md
ID:   node/<namespace>/<Class>
```

Do not add node pages to `index.json` individually. Put `node_pages: true` on
the required topic. The daemon discovers node-page files and generates the
entries. It includes documented classes that are not available in the current
daemon and marks them as unavailable.

## Fallback behavior

The selected locale belongs to one admin API session. Different clients can use
different locales. A locale change does not change graph state or rebuild nodes.

Lookup uses these levels where they apply:

1. the requested locale;
1. `en`;
1. compiled panel text or engine-provided node text;
1. missing content.

The details differ by content type:

- `panel.json` is merged per key over English. Compiled panel literals remain
  the final fallback.
- `nodes.json` falls back per class to English, then to the engine description.
- `menus.json` falls back per presentation field to English, then keeps the
  original Formular value.
- If the requested `docs/index.json` is missing or invalid, the complete English
  index is used. Indexes are not merged.
- When the requested index is valid, Markdown content falls back from that
  locale's selected file to the same English file. Node content can then use the
  engine-provided long description. If the requested index is invalid and the
  daemon uses the English index, it also reads English page content.
- An unavailable or invalid requested locale has the effective locale `en`.

There is no `pt-BR` to `pt` fallback and no cross-locale search other than
English. A missing locale file is not a daemon startup error. The daemon logs a
warning and uses the available fallback. If a changed JSON catalog is invalid,
the daemon keeps the last valid cached version.

## Supported Markdown

The panel uses a small safe renderer, not a full CommonMark implementation. Use
only the features in this section. Raw HTML is always text and is never parsed.

### Block features

- ATX headings from `#` through `######`;
- Setext level-one and level-two headings with `===` or `---` underlines;
- paragraphs with soft line breaks;
- hard line breaks from two trailing spaces or one trailing backslash;
- unordered list items that start with `-`;
- ordered list items that start with a number and `.` or `)`;
- fenced code blocks that use three backticks and an optional language name;
- GitHub-style alerts named `NOTE`, `TIP`, `IMPORTANT`, `WARNING`, or `CAUTION`.

Example alert:

```markdown
> [!WARNING]
> Keep the existing IDs when you translate this page.
>
> - The alert body can contain supported block elements.
```

Lists are flat. Indented continuation lines belong to the current list item, but
nested lists are not supported. Tables, task lists, thematic breaks, indented
code blocks, block quotes other than the named alerts, and reference links are
not supported.

In the standalone documentation site, headings get linkable IDs derived from
their text. Repeated headings get `-1`, `-2`, and later suffixes. In compact
node descriptions in the admin panel, headings render as bold paragraphs.

### Inline features

- bold text with `**text**`;
- italic text with `*text*` or `_text_`;
- inline code with `` `text` ``;
- inline links with `[label](destination "optional title")`;
- inline images with `![alternative](source "optional title")`.

Inline constructs are intentionally simple. Do not nest inline formatting. Links
allow only `http:`, `https:`, and `mailto:` destinations. Images allow only
`http:` and `https:` destinations. A relative destination resolves on the
current site and therefore uses its `http:` or `https:` URL. Use the special
documentation link below for internal pages because it uses stable page IDs.

### References to other documents

Use an `@docs/` destination with a documentation page ID:

```markdown
See [WireGuard](@docs/node/almagest/WireGuard).
See the [workspace guide](@docs/guide/workspaces "Workspace concepts").
```

The destination after `@docs/` must be a valid page ID. It can contain letters,
digits, `_`, `.`, `:`, `/`, and `-`; it cannot contain `..`. The standalone site
creates a relative documentation URL. A node description in the admin panel
opens the page in the documentation site.

If a custom reference is invalid or has an unknown `@type/`, the renderer shows
only its label and does not create a link.

### Node-port references

Use an `@port/` destination to name a port in a node page:

```markdown
Connect [DNS](@port/left/DNS) to [Network](@port/right/Network).
```

The syntax is `@port/<side>/<port-name>`. The side must be `left` or `right`.
`left` is an input port and `right` is an output port. The port name must match
the runtime port name exactly, including case.

The reference renders as bold text. On a node documentation page, pointer hover
can highlight the matching port in the node preview. An invalid port reference
renders as plain label text.

### Highlighted code syntaxes

All fenced code blocks render safely as text. These language names also get
syntax highlighting:

- `json`, `hjson`, and `hujson` for JSON-family configuration;
- `rulev1` for line-oriented routing and filtering rules;
- `spacetable` for whitespace-separated tables.

`rulev1` highlights the first non-whitespace token as an instruction and the
remaining tokens as arguments. Unsigned decimal arguments are numbers. A line
whose first non-whitespace character is `#` is a comment. Highlighting does not
validate whether an instruction is valid for a specific node.

````markdown
```rulev1
# Send private names to slot 2
ADDR_RE (^|.*\.)internal\.example$ 2
```
````

`spacetable` separates cells with spaces or tabs. It highlights the first cell
as a key and later cells as values. Signed integers and decimal numbers get
number highlighting. Double quotes group a cell that contains whitespace, and a
backslash escapes the next character inside a quoted cell. A line whose first
non-whitespace character is `#` is a comment. Highlighting does not validate the
table's node-specific schema.

````markdown
```spacetable
# name type value
app.example.test A 192.0.2.10
example.test MX 10 mail.example.test
example.test TXT "managed locally"
```
````

## Validation and limits

Run:

```sh
just locales-check
```

`just check` also runs this validation. The checker uses the runtime parser and
also checks repository-specific coverage:

- strict bundle structure and safe paths;
- valid canonical locale names and manifests;
- English panel-key coverage;
- valid but optionally partial non-English catalogs;
- known English menu classes;
- English short and long documentation for production node classes.

Catalog files are strict UTF-8 JSON. Duplicate keys, unknown struct fields,
trailing JSON data, symbolic links, non-regular files, unsafe paths, and
unexpected files are rejected. Markdown must be non-empty UTF-8 text and cannot
contain NUL bytes.

Current limits are:

- 128 locale bundles;
- 1 MiB per JSON file;
- 64 KiB per Markdown page;
- 4 KiB per localized value or visible name;
- 10,000 keys or counted menu entries per catalog;
- 10,000 explicit documentation pages;
- 256 characters per documentation ID, menu address, or custom reference
  destination.

File and ID segments must use letters, digits, `_`, `.`, or `-`, must start with
a letter or digit, and cannot traverse with `.` or `..`. Documentation IDs can
also contain `/` and `:`.

Localization does not cover CLI text, logs, configuration keys, protocol errors,
daemon diagnostics, machine-facing names and values, or arbitrary runtime text.
