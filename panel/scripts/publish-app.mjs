import { createHash } from "node:crypto";
import { cp, mkdir, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { build, transform } from "esbuild";
import { minify as minifyHTML } from "html-minifier-terser";

const repository = new URL("../../", import.meta.url);
const panel = new URL("panel/", repository);
const target = new URL("app/frontend/", repository);

async function optimize(directory) {
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const file = new URL(entry.name, directory);
    if (entry.isDirectory()) {
      await optimize(new URL(`${entry.name}/`, directory));
      continue;
    }
    if (entry.name.endsWith(".d.ts")) {
      await rm(file);
      continue;
    }
    const extension = entry.name.slice(entry.name.lastIndexOf(".") + 1);
    if (extension !== "js" && extension !== "css") continue;
    const result = await transform(await readFile(file, "utf8"), {
      legalComments: "eof",
      loader: extension,
      minify: true,
      target: extension === "js" ? "es2022" : undefined,
    });
    await writeFile(file, result.code);
  }
}

await rm(target, { recursive: true, force: true });
await mkdir(new URL("public/build/", target), { recursive: true });
await mkdir(new URL("docs/", target), { recursive: true });
await mkdir(new URL("vendor/formular-menu/", target), { recursive: true });
await mkdir(new URL("vendor/litegraph/", target), { recursive: true });
await cp(new URL("logo-light.svg", repository), new URL("logo.svg", target));
await cp(new URL("logo-light.svg", repository), new URL("favicon.svg", target));
await cp(new URL("LUCIDE_LICENSE.txt", panel), new URL("LUCIDE_LICENSE.txt", target));
await cp(new URL("style.css", panel), new URL("style.css", target));
await cp(new URL("vendor/formular-menu/formular-menu.css", panel), new URL("vendor/formular-menu/formular-menu.css", target));
await cp(new URL("vendor/formular-menu/formular-menu.js", panel), new URL("vendor/formular-menu/formular-menu.js", target));
await cp(new URL("vendor/litegraph/litegraph.css", panel), new URL("vendor/litegraph/litegraph.css", target));
await cp(new URL("vendor/litegraph/litegraph.js", panel), new URL("vendor/litegraph/litegraph.js", target));
// Bundle the module graph so the native package contains only code that the
// desktop entry point can reach. Formular stays separate because app.ts loads
// it from a versioned URL at runtime.
await build({
  bundle: true,
  entryPoints: {
    desktop: fileURLToPath(new URL("public/build/desktop.js", panel)),
    "desktop-docs": fileURLToPath(new URL("public/build/desktop_docs.js", panel)),
  },
  format: "esm",
  legalComments: "eof",
  minify: true,
  outdir: fileURLToPath(new URL("public/build/", target)),
  target: "es2022",
});
await optimize(target);

const desktop = await readFile(new URL("public/build/desktop.js", target));
const version = createHash("sha256").update(desktop).digest("hex").slice(0, 16);
const desktopDocs = await readFile(new URL("public/build/desktop-docs.js", target));
const docsVersion = createHash("sha256").update(desktopDocs).digest("hex").slice(0, 16);
const sourceHTML = await readFile(new URL("index.html", panel), "utf8");
const appHTML = sourceHTML
  // Tauri supplies a restrictive response CSP and adds its randomized
  // isolation-frame origin to that policy. The web panel's meta policy has
  // `frame-src 'none'`, which would intersect with Tauri's policy and block
  // the isolation iframe, leaving all IPC requests queued forever.
  .replace(/^\s*<meta http-equiv="Content-Security-Policy"[^>]*>\s*$/m, "")
  // Warm the other page's module graph. Navigation can then reuse the parsed
  // HTTP cache when the webview does not retain a back-forward entry.
  .replace("</head>", `  <link rel="modulepreload" href="./public/build/desktop-docs.js?v=${docsVersion}">\n  </head>`)
  .replace("./public/build/app.js", `./public/build/desktop.js?v=${version}`);
await writeFile(new URL("index.html", target), (await minifyHTML(appHTML, {
  collapseWhitespace: true,
  conservativeCollapse: true,
  removeComments: true,
})).trim());

const sourceDocsHTML = await readFile(new URL("docs.html", panel), "utf8");
const docsHTML = sourceDocsHTML
  // Use Tauri's response CSP for the same isolation-frame reason as index.html.
  .replace(/^\s*<meta http-equiv="Content-Security-Policy"[^>]*>\s*$/m, "")
  .replace('content="/"', 'content="../"')
  .replaceAll('href="/favicon.svg"', 'href="../favicon.svg"')
  .replaceAll('href="/style.css"', 'href="../style.css"')
  .replaceAll('href="/vendor/', 'href="../vendor/')
  .replaceAll('src="/vendor/', 'src="../vendor/')
  .replaceAll('src="/logo.svg"', 'src="../logo.svg"')
  .replace('href="/"', 'href="../index.html"')
  .replace("</head>", `  <link rel="modulepreload" href="../public/build/desktop.js?v=${version}">\n  </head>`)
  .replace('src="/public/build/docs.js"', `src="../public/build/desktop-docs.js?v=${docsVersion}"`);
await writeFile(new URL("docs/index.html", target), (await minifyHTML(docsHTML, {
  collapseWhitespace: true,
  conservativeCollapse: true,
  removeComments: true,
})).trim());
