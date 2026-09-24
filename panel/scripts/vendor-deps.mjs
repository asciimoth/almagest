import { cp, mkdir } from "node:fs/promises";

await mkdir("vendor/litegraph", { recursive: true });
await mkdir("vendor/formular-menu", { recursive: true });

// The full build registers optional built-in nodes with Function() at startup.
// Use the core build so the panel's CSP can continue to prohibit unsafe-eval.
await cp("node_modules/litegraph.js/build/litegraph.core.js", "vendor/litegraph/litegraph.js");
await cp("node_modules/litegraph.js/css/litegraph.css", "vendor/litegraph/litegraph.css");
await cp("node_modules/litegraph.js/src/litegraph.d.ts", "vendor/litegraph/litegraph.d.ts");

await cp("node_modules/@asciimoth/formular-menu/src/formular-menu.js", "vendor/formular-menu/formular-menu.js");
await cp("node_modules/@asciimoth/formular-menu/types/formular-menu.d.ts", "vendor/formular-menu/formular-menu.d.ts");
