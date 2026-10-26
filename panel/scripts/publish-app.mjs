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
await mkdir(new URL("vendor/formular-menu/", target), { recursive: true });
await mkdir(new URL("vendor/litegraph/", target), { recursive: true });
await cp(new URL("logo-light.svg", repository), new URL("logo.svg", target));
await cp(new URL("style.css", panel), new URL("style.css", target));
await cp(new URL("vendor/formular-menu/formular-menu.js", panel), new URL("vendor/formular-menu/formular-menu.js", target));
await cp(new URL("vendor/litegraph/litegraph.css", panel), new URL("vendor/litegraph/litegraph.css", target));
await cp(new URL("vendor/litegraph/litegraph.js", panel), new URL("vendor/litegraph/litegraph.js", target));
// Bundle the module graph so the native package contains only code that the
// desktop entry point can reach. Formular stays separate because app.ts loads
// it from a versioned URL at runtime.
await build({
  bundle: true,
  entryPoints: [fileURLToPath(new URL("public/build/desktop.js", panel))],
  format: "esm",
  legalComments: "eof",
  minify: true,
  outfile: fileURLToPath(new URL("public/build/desktop.js", target)),
  target: "es2022",
});
await optimize(target);

const desktop = await readFile(new URL("public/build/desktop.js", target));
const version = createHash("sha256").update(desktop).digest("hex").slice(0, 16);
const sourceHTML = await readFile(new URL("index.html", panel), "utf8");
const appHTML = sourceHTML
  // Tauri supplies a restrictive response CSP and adds its randomized
  // isolation-frame origin to that policy. The web panel's meta policy has
  // `frame-src 'none'`, which would intersect with Tauri's policy and block
  // the isolation iframe, leaving all IPC requests queued forever.
  .replace(/^\s*<meta http-equiv="Content-Security-Policy"[^>]*>\s*$/m, "")
  .replace("./public/build/app.js", `./public/build/desktop.js?v=${version}`);
await writeFile(new URL("index.html", target), await minifyHTML(appHTML, {
  collapseWhitespace: true,
  conservativeCollapse: true,
  removeComments: true,
}));
