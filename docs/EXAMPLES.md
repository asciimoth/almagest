# Packaged examples

Almagest examples are read-only, unpacked snapshot sources. The daemon lists
them with regular snapshots and loads them through the same transactional
restore path.

## Layout

Each directory below the examples root has this layout:

```text
examples/<id>/
├── manifest.json
├── config.hujson
└── files/                 # optional
```

The identifier uses lowercase ASCII letters, digits, hyphens, and underscores.
It starts with a letter or digit and is not a reserved Windows device name.
`config.hujson` contains editable config only. `files/` contains only
file-storage data that the example needs. Do not add an empty bbolt database,
generated TLS authority, snapshots, config sources, or other unrelated daemon
state. The daemon creates an empty staged database when the example does not
supply one.

The manifest format is:

```json
{
  "format": 1,
  "name": "Hello Network",
  "comment": "Make one HTTP request through the host network.",
  "translations": {
    "ka": {
      "name": "...",
      "comment": "..."
    }
  }
}
```

`name` and `comment` are required fallback text. A translation can override one
or both fields. Locale names use the same canonical form as localized content.

## Installation

Install the directory at these platform defaults:

- `/usr/share/almagest/examples` on Unix;
- `%ProgramFiles%\Almagest\examples` on Windows;
- `/examples` inside the isolated Linux runtime.

`ALMAGEST_EXAMPLES_DIR` selects another absolute, clean path. The development
daemon commands set it to the checkout `examples/` directory. Linux isolation
opens the selected directory before runtime launch and exposes it through a
separate read-only, `nosuid`, `nodev`, and `noexec` mount. Landlock grants only
file and directory read access to that mount.

Example metadata is untrusted presentation text. The panel inserts it with DOM
text nodes. Example files are size-bounded, reject links and special files, and
are copied to private staging before the existing restore validation runs.
