import { createHash } from "node:crypto";
import { cp, mkdir, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { promisify } from "node:util";
import { constants, gzip as gzipCallback } from "node:zlib";
import { transform } from "esbuild";
import { minify as minifyHTML } from "html-minifier-terser";

const target = new URL("../../bin/almagestd/static/", import.meta.url);
const gzip = promisify(gzipCallback);

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
    if (extension !== "js" && extension !== "css") {
      continue;
    }
    const source = await readFile(file, "utf8");
    const result = await transform(source, {
      legalComments: "eof",
      loader: extension,
      minify: true,
      target: extension === "js" ? "es2022" : undefined,
    });
    await writeFile(file, result.code);
  }
}

async function compress(directory) {
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const file = new URL(entry.name, directory);
    if (entry.isDirectory()) {
      await compress(new URL(`${entry.name}/`, directory));
      continue;
    }
    const source = await readFile(file);
    await writeFile(new URL(`${entry.name}.gz`, directory), await gzip(source, {
      level: constants.Z_BEST_COMPRESSION,
    }));
    await rm(file);
  }
}

await rm(target, { recursive: true, force: true });
await mkdir(target, { recursive: true });
await cp(new URL("../../logo.svg", import.meta.url), new URL("logo.svg", target));
await cp(new URL("../style.css", import.meta.url), new URL("style.css", target));
await cp(new URL("../public", import.meta.url), new URL("public", target), { recursive: true });
await cp(new URL("../vendor", import.meta.url), new URL("vendor", target), { recursive: true });
await optimize(target);

const app = await readFile(new URL("public/build/app.js", target));
const docs = await readFile(new URL("public/build/docs.js", target));
const formularMenu = await readFile(new URL("vendor/formular-menu/formular-menu.js", target));
const version = createHash("sha256").update(app).update(docs).update(formularMenu).digest("hex").slice(0, 16);
const indexURL = new URL("index.html", target);
const index = await readFile(new URL("../index.html", import.meta.url), "utf8");
const versionedIndex = index.replace("./public/build/app.js", `./public/build/app.js?v=${version}`);
await writeFile(indexURL, await minifyHTML(versionedIndex, {
  collapseWhitespace: true,
  conservativeCollapse: true,
  removeComments: true,
}));
await mkdir(new URL("docs/", target), { recursive: true });
const docsIndex = (await readFile(new URL("../docs.html", import.meta.url), "utf8"))
  .replace("/public/build/docs.js", `/public/build/docs.js?v=${version}`);
await writeFile(new URL("docs/index.html", target), await minifyHTML(docsIndex, {
  collapseWhitespace: true,
  conservativeCollapse: true,
  removeComments: true,
}));
await compress(target);
