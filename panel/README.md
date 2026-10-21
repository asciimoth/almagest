# Admin panel

The panel is a TypeScript application that uses browser APIs without a UI
framework. `src/app.ts` owns navigation and API coordination. The smaller model
files own data transforms and DOM builders. `npm run build` writes compiled
modules to `public/build` and publishes the complete panel to the daemon static
directory. The publication step minifies HTML, CSS, and JavaScript. It also
excludes TypeScript declaration files because the daemon does not use them.
The embedded tree contains only gzip-compressed assets; the daemon serves them
with their original URLs and content types.

Use `src/notification_center.ts` for transient action feedback. Do not add
tab-specific copied, saved, or deleted message renderers.

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
- Allow only `http:`, `https:`, and `mailto:` destinations for regular links in
  rendered documentation. Internal documentation links use `@docs/<page-id>`
  destinations and are resolved by the renderer for its current panel or
  documentation context.
  Node-port references use `@port/<left|right>/<port-name>` and render as bold
  text. Their hover state can highlight the matching port in a node preview.
- Validate API-provided CSS with `safeCSSColor` before assignment.
- Use blob URLs for generated downloads. Sanitize API-provided file-name parts.
- Keep the Content Security Policy in `index.html` restrictive.
- Keep `LiteGraph.allow_scripts` set to `false`. LiteGraph contains optional
  script-node code that the panel does not use. The Content Security Policy
  must also continue to omit `unsafe-eval`.
- Vendor the LiteGraph core build. The full build registers optional built-in
  nodes with `Function()` during startup and is not compatible with the panel's
  Content Security Policy.

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
just test-check
```

Do not edit `public/build` or `bin/almagestd/static` by hand. They are generated
by the build.
