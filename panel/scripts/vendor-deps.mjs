import { cp, mkdir, readFile, writeFile } from "node:fs/promises";

await mkdir("vendor/litegraph", { recursive: true });
await mkdir("vendor/formular-menu", { recursive: true });

// The full build registers optional built-in nodes with Function() at startup.
// Use the core build so the panel's CSP can continue to prohibit unsafe-eval.
await cp("node_modules/litegraph.js/build/litegraph.core.js", "vendor/litegraph/litegraph.js");
await cp("node_modules/litegraph.js/css/litegraph.css", "vendor/litegraph/litegraph.css");
await cp("node_modules/litegraph.js/src/litegraph.d.ts", "vendor/litegraph/litegraph.d.ts");

await cp("node_modules/@asciimoth/formular-menu/src/formular-menu.js", "vendor/formular-menu/formular-menu.js");
await cp("node_modules/@asciimoth/formular-menu/types/formular-menu.d.ts", "vendor/formular-menu/formular-menu.d.ts");

// Formular includes its default theme in the JavaScript module so simple web
// clients need only one file. The desktop CSP intentionally blocks injected
// styles, so publish the same theme as a regular stylesheet.
const formularSource = await readFile("node_modules/@asciimoth/formular-menu/src/formular-menu.js", "utf8");
const formularTheme = formularSource.match(/const DEFAULT_THEME = `([\s\S]*?)`;/)?.[1];
if (!formularTheme || formularTheme.includes("${")) {
  throw new Error("could not extract the Formular default theme");
}
await writeFile("vendor/formular-menu/formular-menu.css", `${formularTheme.trim()}\n`);
