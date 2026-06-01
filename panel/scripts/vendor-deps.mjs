import { cp, mkdir } from "node:fs/promises";

await mkdir("vendor/litegraph", { recursive: true });
await mkdir("vendor/formular-menu", { recursive: true });

await cp("node_modules/litegraph.js/build/litegraph.js", "vendor/litegraph/litegraph.js");
await cp("node_modules/litegraph.js/css/litegraph.css", "vendor/litegraph/litegraph.css");
await cp("node_modules/litegraph.js/src/litegraph.d.ts", "vendor/litegraph/litegraph.d.ts");

await cp("node_modules/@asciimoth/formular-menu/src/formular-menu.js", "vendor/formular-menu/formular-menu.js");
await cp("node_modules/@asciimoth/formular-menu/types/formular-menu.d.ts", "vendor/formular-menu/formular-menu.d.ts");
