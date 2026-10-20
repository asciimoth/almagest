# Localized content bundles

## Status

This document specifies the proposed documentation and localization system for
`almagestd` and its admin panel. It is an implementation plan. The system does
not exist yet.

The term **localized content bundle** is used in this document. A bundle
contains node documentation, panel strings, and node-menu presentation text.
This term is more accurate than `docs`, because some content is a short UI
label, and more accurate than `localization`, because some content is a long
Markdown document.

## Scope

The first version has these functions:

- Move built-in node short and long documentation to editable locale files.
- Keep the current class descriptions as small built-in fallbacks.
- Localize panel-owned user interface text.
- Localize the static labels, help text, and placeholders in Formular node
  menus.
- Let each admin API session select its locale and change it while connected.
- Use English when selected-locale content is absent.
- Use the built-in string when both locale files are absent.
- Let development recipes use files from the checkout without a rebuild.

The first version does not localize:

- `almagest-cli`, command output, config files, logs, protocol errors, or daemon
  diagnostics;
- node names, class identifiers, port names, values, secret names, paths, or
  other machine-facing data;
- arbitrary runtime text, such as a remote service error or a node log line;
- the reusable `lib` API for callers that do not use `almagestd`.

Only `en` and `lorem` are initially present. `en` is complete and is installed
by packages. `lorem` is a development fixture and is not installed.

## Ownership and module boundaries

Localized content is an `almagestd` presentation concern. It must stay in
`bin/` and the panel. The reusable engine and node packages must not read the
host filesystem and must not depend on daemon locale policy.

The responsibilities are:

| Component | Responsibility |
| --- | --- |
| Node classes in `lib/` and `bin/almagestd` | Supply stable class, block, and item IDs and concise English fallback text. |
| Localized-content reader in `bin/almagestd` | Read and validate bundle files and apply the locale fallback order. |
| Admin API session | Store the requested locale and project localized views of engine data for one client. |
| Panel | Select a locale, request the panel catalog, and render text with safe DOM operations. |
| Isolation bootstrap | Give the runtime read-only access to one fixed bundle root. It does not parse or render content. |

The daemon API boundary is the correct translation boundary. The engine can
continue to produce one language-neutral state plus built-in fallback strings.
Different connected panels can then use different locales without rebuilding
nodes or changing graph state.

## Directory layout

The source tree uses this layout:

```text
locales/
  en/
    manifest.json
    panel.json
    menus.json
    nodes/
      almagest/
        Tun.short.txt
        Tun.md
        WireGuard.short.txt
        WireGuard.md
      pasta/
        PopUp.short.txt
        PopUp.md
  lorem/
    manifest.json
    panel.json
    menus.json
    nodes/
      almagest/
        Tun.short.txt
```

The recommended installed root is:

```text
/usr/share/almagest/locales
```

`ALMAGEST_LOCALES_DIR` overrides that root. The value must be an absolute,
clean directory path. A set but invalid override is a startup error. An absent
default directory is not a startup error: the daemon logs one bounded warning
and uses built-in text.

The repository directory is `locales/`, not `docs/locales/`. Files in `docs/`
describe the project and its design. Files in `locales/` are runtime package
data. This distinction also makes it easy for packaging rules to include
`locales/en` and exclude `locales/lorem`.

Each file must be UTF-8. Text files use LF line endings in the repository. A
short description is plain text and must not contain a line break. A long node
description uses the small Markdown subset that the panel already renders
safely. JSON files use strict JSON, not HuJSON, so tools can validate them in
the same way on all platforms.

`manifest.json` initially has this shape:

```json
{
  "format": 1,
  "locale": "en",
  "name": "English",
  "development_only": false
}
```

`format` permits an explicit future migration. `locale` must equal the
directory name. `name` is the safe display name in the language picker.
Packages must reject a bundle with `development_only: true`.

### Locale names

Locale names use a restricted BCP 47 form, such as `en`, `ru`, or `pt-BR`.
They contain only ASCII letters, digits, and `-`, are at most 35 bytes, and
cannot contain empty segments. The reader compares canonical locale names.

The first version does not implicitly try a parent language. For example, the
lookup order for `pt-BR` is `pt-BR`, then `en`, then the built-in value. A
future version can add `pt` between the first two entries after that behavior
is part of the API contract.

### Node files

A class name maps to two files. For example, `almagest/Tun` maps to:

```text
nodes/almagest/Tun.short.txt
nodes/almagest/Tun.md
```

The reader accepts exactly two safe class-name path segments. Each segment
must match `[A-Za-z0-9][A-Za-z0-9_.-]*`. It rejects all other names for file
lookup and uses the built-in class descriptions. It must never pass an
unchecked class name to `os.Open`.

All reads are relative to an already-open locale-root directory. The reader
rejects symlinks and non-regular final files. On Linux, use `openat2` with
`RESOLVE_BENEATH`, `RESOLVE_NO_MAGICLINKS`, and `RESOLVE_NO_SYMLINKS`, with a
descriptor-walk fallback where `openat2` is unavailable. The portable path
must apply equivalent checks to every component. A catalog file must never
escape into daemon state or another host directory, even when the bundle root
is writable by the development user.

If Pasta later permits class IDs outside this grammar, add a manifest mapping
or a reversible safe encoding. Do not relax path validation.

Node files are separate because long documentation changes often, benefits
from normal Markdown editor support, and should not require parsing a large
catalog. The files also give node documentation clear review diffs.

### Panel catalog

`panel.json` is a flat map from stable key to text:

```json
{
  "app.status.connecting": "Connecting admin API",
  "node.name.label": "Name",
  "node.name.rename": "Rename",
  "node.description.empty": "No long description is registered for this node class."
}
```

Keys describe meaning, not the current English text and not the DOM location.
Renaming an English label must not rename its key. Values are plain text. The
first version does not permit HTML values. Parameterized text uses named
placeholders with a small panel-owned formatter; parameters are always
inserted as text, never as markup.

The compiled panel keeps its current English constants as fallbacks. This is
necessary when the runtime data directory is absent or an installed catalog
is incomplete. `en/panel.json` is nevertheless complete and is the normal
source at runtime.

### Formular menu catalog

`menus.json` is keyed by node class and stable Formular IDs. A representative
fragment is:

```json
{
  "almagest/WireGuard": {
    "blocks": {
      "config": {
        "items": {
          "private-key": {
            "label": "Private key",
            "help": "Select the secret that contains the WireGuard private key."
          }
        }
      }
    }
  }
}
```

The translator uses block IDs, item IDs, array-template names, nested item IDs,
dialog IDs, and dialog-option values as addresses. It can replace only these
presentation fields:

- item `label`, static `text`, `help`, and `placeholder`;
- array-template `label` and its nested item presentation fields;
- dialog `title`, static `text`, button labels, placeholder, resource
  alternative text, and option labels.

It must not replace IDs, generations, field kinds, conditions, option values,
field values, progress, log lines, validation results, autocomplete results,
copyable data, or resource bytes. Dynamic `statusText` is also not translated
in the first version because it can contain runtime error details.

This address scheme requires node code to treat Formular IDs as stable API.
The IDs are already used by backend message handling. A change to an ID must
update `en/menus.json` in the same change.

## Lookup rules

Lookup is per value, not per file. For each requested value, the reader uses:

1. the requested locale directory, when the session specified a locale;
2. the `en` directory;
3. the built-in constant, when one exists;
4. an empty value only when the API field already permits no text.

This rule permits an intentionally incomplete `lorem` bundle to test fallback
behavior. A missing key in `lorem/menus.json`, for example, can still resolve
from `en/menus.json` even when another key exists in `lorem/menus.json`.

An empty file or empty JSON string is a present value only for fields that
explicitly permit an empty value. Node short and long descriptions, required
panel keys, locale display names, and Formular labels treat empty text as
missing. This prevents a damaged translation from silently removing controls.

The reader returns the source of a resolved value (`requested`, `en`,
`builtin`, or `missing`) internally. Normal API responses do not need this
metadata. Tests and a development diagnostic can use it to verify fallback.

## Reading and live reload

Create a small daemon-owned `localizedcontent` package under `bin/`. Its main
interface should provide:

```text
AvailableLocales()
Panel(locale)
NodeShort(locale, class, builtin)
NodeLong(locale, class, builtin)
TranslateFormular(locale, class, message)
```

The exact Go signatures can use typed results and errors. The reader must have
bounded input sizes, reject unknown manifest formats, and return copies of
mutable Formular messages.

Development edits must become visible without a daemon or panel rebuild. The
reader therefore checks file identity, modification time, and size on lookup
and reparses a changed JSON file. Node text files can be read on demand. It can
cache unchanged valid content, but it must not require a filesystem watcher.
Filesystem watchers have different behavior across the supported platforms
and can lose events.

If a file becomes invalid after startup, keep the last valid parsed version
for that file and emit a rate-limited warning. If there is no last valid
version, continue through the fallback order. Use an internal content revision
that changes after a valid reload. The panel can get new content when it
changes locale, reconnects, or explicitly refreshes. Automatic push reload is
not required in the first version.

Set conservative limits before decoding. Initial limits should include:

- 64 KiB for one node document;
- 1 MiB for one JSON catalog;
- 4 KiB for one localized value;
- 128 locales and 10,000 keys per catalog.

These values are policy limits, not allocation targets. Tests must cover the
limits before release.

## Isolation design

The runtime role, R, must do locale lookup because locale is per API session
and R owns the admin protocol and panel. The network broker, N, must not become
a text lookup service. That would add a broad file-content protocol to the
trusted process and put presentation parsing in the wrong role.

Direct mode opens the resolved bundle root directly.

For Linux isolated `auto` and `strict` profiles, bootstrap does this work:

1. Resolve the default path or `ALMAGEST_LOCALES_DIR` before R starts.
2. Open the directory without following an unsafe replacement and keep the
   directory descriptor.
3. Pass the descriptor as a new optional runtime launch input.
4. Mount that held directory at `/locales` in R's private mount namespace.
5. Remount it read-only, `nosuid`, `nodev`, and `noexec`.
6. Add read-file and read-directory rights for `/locales` to R's Landlock
   rules. Do not give this access to C or H.

The mount must be non-recursive so a submount below the configured root does
not add host filesystem authority. R uses the fixed path `/locales`; it does
not receive the host path. A missing default root means that no descriptor or
mount is passed. Built-in fallback remains available.

This design also supports live development edits. A bind-mounted held
directory sees ordinary file replacement below that directory. The isolated
`just daemon-sudo` recipe can therefore use the checkout bundle without
copying it into R's private state. The content is untrusted and read-only from
R's view. Safe rendering and bounded reads remain mandatory.

The locale root does not change isolation selection. `auto` must not reduce
its profile because content is absent, and `strict` must not require optional
localized content. If an operator explicitly sets an invalid override, startup
fails in all modes so a configuration error is visible.

Windows currently uses direct mode and opens the root directly. The packaged
default is `%ProgramFiles%\Almagest\locales`, resolved with the Windows known
folder API instead of assuming that the system drive is `C:`. The package
installer grants ordinary users read and execute access and grants modification
only to administrators and the installer service. `ALMAGEST_LOCALES_DIR`
overrides this location for the daemon process.

A future isolated Windows profile must pass an already-open directory handle
with read-only rights or stage a read-only package directory by the same
authority rules. It must not give R a general host path capability. This future
work is separate from `just daemon-winvm`, which currently starts the daemon as
SYSTEM in explicit direct mode.

### Windows VM development

The current WinVM harness archives and transfers one source-tree snapshot
before it starts the manual daemon. Therefore, using
`$SourceDir\locales` alone would not satisfy live reload: edits made in the host
checkout after VM startup would not reach the guest.

`just daemon-winvm` must use a host-to-guest development sync:

1. The recipe sets `WINVM_LOCALES_DIR` to the absolute host checkout
   `locales` directory.
2. `dev/winvm/run.sh` validates this host directory and passes it to
   `supervise.py`. The value is harness input and is never passed as a Windows
   path.
3. Before daemon startup, the supervisor copies the directory to the fixed
   guest path `$RunDir\manual\locales` over the existing authenticated SSH
   transport.
4. `manual-daemon.ps1` starts `almagestd.exe` with a process-local
   `ALMAGEST_LOCALES_DIR` value that names that guest directory. Because the
   current launcher uses `Win32_Process.Create`, put the environment assignment
   in the `cmd.exe` command line; do not set a persistent machine environment
   variable.
5. While the manual session runs, the supervisor polls a bounded content hash
   of the host locale tree. On a change, it uploads a new archive to a temporary
   guest directory and asks a PowerShell helper to validate and replace changed
   regular files below the fixed locale root.

The sync must reject host symlinks, Windows reparse points, unsafe archive
paths, non-regular files, and inputs above the normal bundle limits. Copy each
new file to a temporary file in its destination directory and then replace the
target atomically. Apply manifests last. Remove guest files that are absent
from the new validated snapshot, but never remove the fixed locale root. A
failed sync keeps the last good guest files, records a clear harness warning,
and does not stop the daemon.

The sync loop is a development transport, not a second localization reader.
`almagestd.exe` observes the changed guest files through the same cache and
reload logic as every other direct-mode installation. It does not need a VM
share, SMB server, QEMU filesystem device, daemon restart, or panel rebuild.
Both `en` and `lorem` are copied for this development command. Locale files are
not part of the persisted `local/winvm` daemon state and are not copied back
when the session stops.

## Admin API

Locale is connection state in `apiSession`. It is not graph state, regular
config, protected config, or a process-global daemon setting. Two panels can
select different locales at the same time. A reconnect starts in `en` until
the client selects another locale.

Add these methods to the `admin` surface:

- `locale_snapshot`: return the requested locale, effective locale, available
  locales, the resolved panel catalog, and content revision;
- `set_locale`: validate and set the requested locale for this session, then
  return the same response shape as `locale_snapshot`.

The panel starts with `en`. It can store the user's last selection in browser
local storage and call `set_locale` after `hello`. Failure to restore that
preference leaves the session in `en`.

An unavailable but syntactically valid requested locale resolves to `en` and
the response reports that effective locale. The normal picker lists only
valid discovered manifests, so this case primarily supports API compatibility
and removal of a bundle during a session.

All class DTO creation must use the session reader:

- `workspace_snapshot`;
- `node_class_snapshot`;
- class-added and full-snapshot engine notifications;
- `node_class_long_description`.

`node_class_long_description` first requests the localized node file and then
uses `Engine.NodeClassLongDescription` as its built-in fallback. Class snapshot
conversion uses `NodeClassSnapshot.ShortDescription` as its built-in fallback.
The public API types can keep their current fields.

When locale changes, the panel must:

1. replace its panel catalog and re-render panel-owned text;
2. request a full engine snapshot so all short class descriptions are
   replaced;
3. destroy and resubscribe the selected node menu so it gets a complete
   localized Formular snapshot;
4. refetch the visible long node description.

No graph node is rebuilt. A locale change does not need a mutation lease and
does not schedule config autosave.

The session notification callback can run at the same time as `set_locale`.
Protect the session locale with a small lock or an immutable atomic session
view. Each outgoing message uses one locale snapshot for its complete
conversion, so one message cannot contain a mix of two locales.

## Formular translation decision

Translate Formular messages in `almagestd`, after they leave the engine and
before they are encoded for one admin session.

Doing this only in the panel is not recommended. It would require TypeScript
code to duplicate the full recursive Formular schema, determine the node class
for every message, and keep backend semantic fields separate from display
fields. It would also make every non-panel admin client implement the same
rules.

The daemon translator operates on known Go Formular types and deep copies the
message before replacement. The engine notification remains unchanged for
other sessions. For a node-menu notification, the daemon uses workspace ID and
node ID to obtain the node class. If the node no longer exists or the message
type is unknown, it passes the message through unchanged.

Locale changes do not modify Formular generation numbers. A fresh panel menu
instance and a new subscription avoid stale renderer state when labels change
but backend generations do not.

## Panel integration and XSS safety

Add a small `panel/src/localization.ts` module. It owns the current resolved
catalog and a `text(key, builtin, parameters?)` function. Existing English UI
strings move behind stable keys in manageable groups. The English literal
stays close enough to the call site to be a reviewable compiled fallback.

Add a language picker to the panel settings or application header. Populate
it only from the validated `available` list in `locale_snapshot`. The initial
selection is English. Selection changes call `admin.set_locale` on the current
connection.

Localized files are untrusted input. Existing protections remain required:

- use `textContent`, form `value`, `title`, and explicit DOM attributes;
- do not assign localized text to `innerHTML` or an HTML template;
- continue to escape any string passed to LiteGraph APIs that parse markup;
- continue to render node Markdown with the existing safe, limited renderer;
- keep Formular's DOM-only text and limited Markdown renderer;
- do not permit URLs, CSS, script, event-handler attributes, or arbitrary HTML
  in catalogs;
- apply length limits before a value reaches layout or rendering code.

Localization does not make node text trusted. English package files, local
development files, third-party node fallbacks, and translated files all use
the same safe path.

## Packaging and development commands

Package rules install only:

```text
locales/en -> /usr/share/almagest/locales/en
```

They must not install `locales/lorem`. The package manifest validates the
locale manifest and rejects `development_only: true`. The Linux packaging
guide and isolated payload build must include the runtime data directory as a
separate package asset; it is not embedded in `almagestd`.

The `just daemon` and `just daemon-sudo` paths set:

```text
ALMAGEST_LOCALES_DIR=<absolute-checkout-path>/locales
```

The direct runner passes it to `almagestd`. The isolated runner passes it only
to N as launch input; N opens the directory and gives R the read-only mount
described above. `just` test recipes also set the same value when a test starts
the development daemon. Tests that verify missing-content fallback explicitly
clear or replace it.

`just daemon-winvm` instead sets `WINVM_LOCALES_DIR` because the checkout path
is a host POSIX path that the Windows process cannot open. The WinVM supervisor
copies and synchronizes it to the guest path, and `manual-daemon.ps1` sets
`ALMAGEST_LOCALES_DIR` to that guest path for `almagestd.exe` only.

The normal panel build must not copy locale files into
`bin/almagestd/static`. They remain daemon data and can change independently of
the compiled panel.

## Validation and tests

Add one repository validation command and include it in `just check`. It must:

- validate manifests and locale names;
- validate strict JSON, UTF-8, file names, safe relative paths, and size limits;
- require all `en` panel keys used by the panel;
- require English node files for all production node classes registered by
  the daemon;
- check that `menus.json` refers to known production class and stable menu
  IDs where static inspection is possible;
- reject unknown catalog fields and duplicate JSON keys;
- confirm that `lorem` is marked development-only and intentionally permit it
  to be incomplete;
- confirm that package file lists exclude development-only locales.

Unit tests must cover:

- requested-locale, English, built-in, and missing fallback for each content
  type;
- fallback per key when a locale JSON file is incomplete;
- invalid locale names and traversal-shaped class names;
- symlinks, directory replacement, truncated files, invalid UTF-8, duplicate
  keys, oversized inputs, and reload after an atomic save;
- retention of the last valid catalog after a bad development edit;
- two concurrent API sessions with different locales;
- locale change during an engine notification;
- localized workspace snapshots, class notifications, and long descriptions;
- recursive Formular translation without mutation of the engine message;
- pass-through of values, IDs, logs, errors, and unknown Formular messages;
- panel locale selection, reconnect preference, rerender, and missing-key
  fallback;
- XSS payloads in panel text, node Markdown, menu labels, help, placeholders,
  and dialog text.

WinVM harness tests must cover initial locale transfer, a host edit while the
manual daemon stays running, deletion fallback, rejection of a source symlink
or guest reparse point, interrupted upload, atomic replacement, and exclusion
of locale files from exported manual state. A harness unit test must also prove
that the host path is not placed in the Windows daemon command line.

Linux native-profile tests must prove that R can read the held locale root and
cannot write it, execute from it, follow it to the host root, or read an
unrelated host file. C and H must not receive locale access. Add an isolated
daemon test that changes a locale file atomically and observes the new text
without rebuilding or restarting the daemon.

## Implementation plan

Implement the system in these stages:

1. Add `locales/en`, `locales/lorem`, manifests, validators, and file-size
   policy. Copy the exact current production node descriptions to English
   files. Replace long built-in descriptions with concise one- or two-sentence
   fallbacks and keep the current short class descriptions as fallbacks.
2. Add the daemon `localizedcontent` reader with per-value fallback and
   reload-aware caches. Add focused reader and security tests.
3. Add direct-mode path resolution and `ALMAGEST_LOCALES_DIR`. Update `just`
   daemon and test launch paths.
4. Extend Linux bootstrap and the native launcher with the optional held
   locale directory. Add mount, Landlock, denial, and isolated live-edit tests.
5. Add per-session admin locale methods. Localize node-class snapshots,
   notifications, and long-description responses at the API boundary.
6. Extract panel-owned text to stable keys, add the panel catalog client and
   language picker, and keep compiled English fallbacks. Add DOM and XSS tests.
7. Add typed Formular message translation in the daemon. Populate English
   menu entries and an incomplete Lorem fixture. Remount the selected menu on
   locale change.
8. Update Linux and Windows package builders to install only production
   locales. Add package-content tests and update the packaging guide.
9. Add the bounded WinVM locale sync and process-local guest environment
   override. Add harness tests and a manual-daemon live-edit integration case.
10. Run focused Go, panel, WinVM harness, and isolation tests during each
   stage. Run the full
   `just check` after all API, panel, packaging, and isolation changes are
   integrated.

## Design consequences

This design keeps localization out of engine state and keeps host file access
out of reusable node packages. It supports multiple locales at the same time,
preserves built-in behavior when package data is absent, and permits live
development edits. It adds one narrow read-only directory capability to the
isolated runtime, but it does not add arbitrary path access or a new broker
content protocol.

The main maintenance cost is stable text keys and stable Formular IDs. That
cost is useful: it makes presentation contracts explicit and lets validation
find missing English content before packaging.
