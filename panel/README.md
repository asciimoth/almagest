# Admin panel

The panel is a TypeScript application that uses browser APIs without a UI
framework. `src/app.ts` owns navigation and API coordination. The smaller model
files own data transforms and DOM builders. `npm run build` writes compiled
modules to `public/build` and publishes the complete panel to the daemon static
directory.

## Data trust boundary

Treat all daemon, node, plugin, config, storage, and clipboard data as
untrusted. This rule applies even when the daemon listens only on localhost.

- Parse JSON as `unknown`.
- Validate WebSocket envelopes and known event payloads before use.
- Validate nested engine snapshots before graph data reaches LiteGraph. Use the
  decoders in `src/pasta_model.ts` for direct snapshots and notifications.
- Use `textContent`, `createTextNode`, form properties, and DOM construction for
  untrusted text. Do not use HTML parser APIs.
- Escape text before you pass it to LiteGraph menu APIs. LiteGraph parses some
  menu labels as HTML.
- Allow only `http:`, `https:`, and `mailto:` links in rendered documentation.
- Validate API-provided CSS with `safeCSSColor` before assignment.
- Use blob URLs for generated downloads. Sanitize API-provided file-name parts.
- Keep the Content Security Policy in `index.html` restrictive.
- Keep `LiteGraph.allow_scripts` set to `false`. LiteGraph contains optional
  script-node code that the panel does not use. The Content Security Policy
  must also continue to omit `unsafe-eval`.

The source-level XSS contract test scans every TypeScript source file. New
source files are included automatically. Add a DOM test when a new renderer
accepts untrusted text or URLs.

## Types

The compiler uses strict mode, exact optional properties, unchecked-index
checks, unreachable-code checks, and unused-label checks. Keep transport data
as `unknown` until a runtime decoder validates it. Put reusable decoders in
`src/event_model.ts` or the related domain model. Types alone do not validate
WebSocket data.

The LiteGraph browser global is the main exception. Its vendored declarations
do not include all private fields that the panel must patch. Keep uses of this
boundary local to graph integration code. Decode daemon data before it enters
this boundary. Do not let `any` values enter storage, API, or DOM model code.

## Tests

Run the panel checks with:

```sh
npm test
```

The command vendors browser dependencies, compiles with TypeScript, publishes
static files, and runs the Node test suite. The tests include runtime decoder
tests, hostile-text DOM tests, source scans for dangerous DOM and script sinks,
and Content Security Policy checks. From the repository root, use the required
whole-project checks:

```sh
just lint
just test-total
```

Do not edit `public/build` or `bin/almagestd/static` by hand. They are generated
by the build.
