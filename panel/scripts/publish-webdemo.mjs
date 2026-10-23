import { cp, mkdir, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { transform } from "esbuild";
import { minify as minifyHTML } from "html-minifier-terser";

const outputArgument = process.argv[2] || "../build/webdemo";
const target = pathToFileURL(`${resolve(process.cwd(), outputArgument)}/`);
const repository = new URL("../../", import.meta.url);

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

function ordered(values) {
  return values.map((value, index) => ({ value, index })).sort((left, right) => {
    const a = left.value.order;
    const b = right.value.order;
    if (a === undefined && b === undefined) return left.index - right.index;
    if (a === undefined) return 1;
    if (b === undefined) return -1;
    return a - b || left.index - right.index;
  }).map(({ value }) => value);
}

function buildTopics(values, nodePages, pages, locale) {
  return ordered(values).map((value) => {
    const topic = {
      id: value.id,
      name: value.name,
      folded: value.folded ?? true,
      topics: buildTopics(value.topics ?? [], nodePages, pages, locale),
      pages: ordered(value.pages ?? []).map((page) => {
        pages[page.id] = `content/${locale}/pages/${page.file}`;
        return {
          id: page.id,
          name: page.name,
          folded: page.folded ?? true,
          ...(page.order === undefined ? {} : { order: page.order }),
          ...(page.default ? { default: true } : {}),
        };
      }),
      ...(value.order === undefined ? {} : { order: value.order }),
    };
    if (value.node_pages) topic.pages.push(...nodePages);
    return topic;
  });
}

async function buildDocsCatalog() {
  const localeRoot = new URL("locales/", repository);
  const entries = await readdir(localeRoot, { withFileTypes: true });
  const available = [];
  const locales = {};
  for (const entry of entries) {
    if (!entry.isDirectory()) continue;
    const source = new URL(`${entry.name}/`, localeRoot);
    const manifest = JSON.parse(await readFile(new URL("manifest.json", source), "utf8"));
    if (manifest.development_only) continue;
    available.push({ locale: manifest.locale, name: manifest.name });
    const index = JSON.parse(await readFile(new URL("docs/index.json", source), "utf8"));
    const names = JSON.parse(await readFile(new URL("nodes.json", source), "utf8"));
    const nodeRoot = new URL("docs/pages/nodes/", source);
    const nodePages = [];
    const pages = {};
    for (const namespace of await readdir(nodeRoot, { withFileTypes: true })) {
      if (!namespace.isDirectory()) continue;
      for (const file of await readdir(new URL(`${namespace.name}/`, nodeRoot), { withFileTypes: true })) {
        if (!file.isFile() || !file.name.endsWith(".md")) continue;
        const className = `${namespace.name}/${file.name.slice(0, -3)}`;
        const id = `node/${className}`;
        pages[id] = `content/${manifest.locale}/pages/nodes/${className}.md`;
        nodePages.push({ id, name: names[className] || className.split("/").at(-1), folded: true, available: true });
      }
    }
    nodePages.sort((left, right) => left.id.localeCompare(right.id));
    const tree = buildTopics(index.topics ?? [], nodePages, pages, manifest.locale);
    locales[manifest.locale] = { tree, pages };
    await cp(new URL("docs/pages/", source), new URL(`docs/content/${manifest.locale}/pages/`, target), { recursive: true });
  }
  available.sort((left, right) => left.locale.localeCompare(right.locale));
  await mkdir(new URL("docs/content/", target), { recursive: true });
  await writeFile(new URL("docs/content/catalog.json", target), JSON.stringify({ available, locales }));
}

async function minifiedHTML(source, replacements = []) {
  let value = await readFile(source, "utf8");
  for (const [from, to] of replacements) value = value.replaceAll(from, to);
  return await minifyHTML(value, { collapseWhitespace: true, conservativeCollapse: true, removeComments: true });
}

await rm(target, { recursive: true, force: true });
await mkdir(target, { recursive: true });
await cp(new URL("logo-light.svg", repository), new URL("logo.svg", target));
await cp(new URL("webdemo/main.css", repository), new URL("main.css", target));
await cp(new URL("panel/style.css", repository), new URL("style.css", target));
await cp(new URL("panel/public/", repository), new URL("public/", target), { recursive: true });
await cp(new URL("panel/vendor/", repository), new URL("vendor/", target), { recursive: true });
await writeFile(new URL("index.html", target), await minifiedHTML(new URL("webdemo/index.html", repository)));
await mkdir(new URL("demo/", target), { recursive: true });
await writeFile(new URL("demo/index.html", target), await minifiedHTML(new URL("panel/index.html", repository), [
  ["<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">", "<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\"><meta name=\"almagest-site-base\" content=\"../\">"],
  ["class=\"header-logo-link\" href=\"./\"", "class=\"header-logo-link\" href=\"../\""],
  ["href=\"./vendor/", "href=\"../vendor/"],
  ["href=\"./style.css\"", "href=\"../style.css\""],
  ["src=\"./vendor/", "src=\"../vendor/"],
  ["src=\"./logo.svg\"", "src=\"../logo.svg\""],
  ["src=\"./public/build/app.js\"", "src=\"../public/build/webdemo.js\""],
]));
await mkdir(new URL("docs/", target), { recursive: true });
await writeFile(new URL("docs/index.html", target), await minifiedHTML(new URL("panel/docs.html", repository), [
  ["content=\"/\"", "content=\"../../\""],
  ["href=\"/style.css\"", "href=\"../style.css\""],
  ["class=\"header-logo-link\" href=\"/\"", "class=\"header-logo-link\" href=\"../\""],
  ["src=\"/logo.svg\"", "src=\"../logo.svg\""],
  ["src=\"/public/build/docs.js\"", "src=\"../public/build/static_docs.js\""],
  ["href=\"/\" target=\"_blank\">Admin panel", "href=\"../demo/\" target=\"_blank\">Web demo"],
]));
await buildDocsCatalog();
await optimize(target);
