import { cp, mkdir, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { execFile } from "node:child_process";
import { join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { promisify } from "node:util";
import { Resvg } from "@resvg/resvg-js";
import { transform } from "esbuild";
import { minify as minifyHTML } from "html-minifier-terser";
import { copyEntryModules } from "./copy-entry-modules.mjs";

const outputArgument = process.argv[2] || "../build/webdemo";
const target = pathToFileURL(`${resolve(process.cwd(), outputArgument)}/`);
const repository = new URL("../../", import.meta.url);
const execute = promisify(execFile);
const siteURL = new URL(process.env.ALMAGEST_WEBDEMO_SITE_URL || "https://asciimoth.github.io/almagest/");
if (!["http:", "https:"].includes(siteURL.protocol) || !siteURL.pathname.endsWith("/")
  || siteURL.search || siteURL.hash) {
  throw new Error("ALMAGEST_WEBDEMO_SITE_URL must be an HTTP(S) site URL ending in /");
}

const socialPages = {
  landing: {
    path: "",
    title: "Almagest",
    description: "Almagest is a network toolkit with a visual graph editor. Connect VPNs, proxies, DNS, routing, and traffic filters in one local service.",
  },
  demo: {
    path: "demo/",
    title: "Almagest Web Demo",
    description: "Try the Almagest visual graph editor in your browser with a live WebAssembly demo.",
  },
  docs: {
    path: "docs/",
    title: "Almagest Documentation",
    description: "Explore Almagest nodes, links, and configuration in the documentation.",
  },
};

function htmlAttribute(value) {
  return value.replaceAll("&", "&amp;").replaceAll('"', "&quot;").replaceAll("<", "&lt;");
}

function socialMetadata({ path, title, description }) {
  const pageURL = new URL(path, siteURL).href;
  const imageURL = new URL("social-preview.png", siteURL).href;
  const imageAlt = "Almagest logo beside three connected graph nodes";
  const meta = (key, value, property = false) => `<meta ${property ? "property" : "name"}="${key}" content="${htmlAttribute(value)}">`;
  return [
    `<link rel="canonical" href="${htmlAttribute(pageURL)}">`,
    meta("description", description),
    meta("og:type", "website", true),
    meta("og:site_name", "Almagest", true),
    meta("og:url", pageURL, true),
    meta("og:title", title, true),
    meta("og:description", description, true),
    meta("og:image", imageURL, true),
    meta("og:image:type", "image/png", true),
    meta("og:image:width", "1280", true),
    meta("og:image:height", "640", true),
    meta("og:image:alt", imageAlt, true),
    meta("twitter:card", "summary_large_image"),
    meta("twitter:title", title),
    meta("twitter:description", description),
    meta("twitter:image", imageURL),
    meta("twitter:image:alt", imageAlt),
  ].join("\n    ");
}

async function buildWASM() {
  const bin = fileURLToPath(new URL("bin/", repository));
  const wasm = fileURLToPath(new URL("almagest.wasm", target));
  const env = { ...process.env, GOOS: "js", GOARCH: "wasm", CGO_ENABLED: "0" };
  await execute("go", ["-C", bin, "build", "-trimpath", "-o", wasm, "./cmd/almagest-webdemo"], { env });
  const { stdout } = await execute("go", ["env", "GOROOT"]);
  await cp(join(stdout.trim(), "lib", "wasm", "wasm_exec.js"), new URL("wasm_exec.js", target));
}

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

async function buildDocsCatalog(nodeClassCache, availableNodeClasses) {
  const nodeClassOrder = new Map(nodeClassCache.order.map((className, index) => [className, index]));
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
        const available = availableNodeClasses.has(className);
        pages[id] = `content/${manifest.locale}/pages/nodes/${className}.md`;
        nodePages.push({
          id,
          name: names[className] || className.split("/").at(-1),
          folded: true,
          available,
          marks: available ? ["available"] : [],
        });
      }
    }
    // The dump order comes from the daemon's canonical node-class comparator.
    // Unknown documented classes sort last, in a deterministic fallback order.
    nodePages.sort((left, right) => {
      const leftOrder = nodeClassOrder.get(left.id.slice("node/".length));
      const rightOrder = nodeClassOrder.get(right.id.slice("node/".length));
      if (leftOrder === undefined && rightOrder === undefined) return left.id.localeCompare(right.id);
      if (leftOrder === undefined) return 1;
      if (rightOrder === undefined) return -1;
      return leftOrder - rightOrder;
    });
    const tree = buildTopics(index.topics ?? [], nodePages, pages, manifest.locale);
    locales[manifest.locale] = { tree, pages };
    await mkdir(new URL(`locales/${manifest.locale}/`, target), { recursive: true });
    await cp(new URL("panel.json", source), new URL(`locales/${manifest.locale}/panel.json`, target));
    await cp(new URL("docs/pages/", source), new URL(`docs/content/${manifest.locale}/pages/`, target), { recursive: true });
  }
  available.sort((left, right) => left.locale.localeCompare(right.locale));
  await mkdir(new URL("docs/content/", target), { recursive: true });
  await writeFile(new URL("docs/content/catalog.json", target), JSON.stringify({ available, locales }));
}

async function publishNodeClassCache() {
  const source = new URL("webdemo/node-classes.json", repository);
  const cache = JSON.parse(await readFile(source, "utf8"));
  if (cache?.version !== 1 || !cache.classes || typeof cache.classes !== "object" || Array.isArray(cache.classes)
    || !Array.isArray(cache.order) || cache.order.length !== Object.keys(cache.classes).length
    || new Set(cache.order).size !== cache.order.length
    || cache.order.some((className) => typeof className !== "string" || !Object.hasOwn(cache.classes, className))) {
    throw new Error("webdemo/node-classes.json is not a supported node class cache");
  }
  await mkdir(new URL("docs/content/", target), { recursive: true });
  await writeFile(new URL("docs/content/node-classes.json", target), JSON.stringify(cache));
  return cache;
}

async function webDemoAvailableNodeClasses(nodeClassCache) {
  const source = new URL("webdemo/available-node-classes.json", repository);
  const classes = JSON.parse(await readFile(source, "utf8"));
  if (!Array.isArray(classes) || classes.length === 0
    || classes.some((className) => typeof className !== "string" || className === "")
    || new Set(classes).size !== classes.length
    || classes.some((className) => !Object.hasOwn(nodeClassCache.classes, className))) {
    throw new Error("webdemo/available-node-classes.json is not a supported node class list");
  }
  return new Set(classes);
}

async function minifiedHTML(source, replacements = [], page) {
  let value = await readFile(source, "utf8");
  for (const [from, to] of replacements) value = value.replaceAll(from, to);
  value = value.replace("</head>", `    ${socialMetadata(page)}\n  </head>`);
  return await minifyHTML(value, { collapseWhitespace: true, conservativeCollapse: true, removeComments: true });
}

await rm(target, { recursive: true, force: true });
await mkdir(target, { recursive: true });
await cp(new URL("logo-light.svg", repository), new URL("logo.svg", target));
await cp(new URL("logo-light.svg", repository), new URL("favicon.svg", target));
const preview = new Resvg(await readFile(new URL("social-preview.svg", repository)), {
  fitTo: { mode: "width", value: 1280 },
}).render();
if (preview.width !== 1280 || preview.height !== 640) {
  throw new Error("social-preview.svg must render at a 2:1 ratio");
}
await writeFile(new URL("social-preview.png", target), preview.asPng());
await cp(new URL("panel/LUCIDE_LICENSE.txt", repository), new URL("LUCIDE_LICENSE.txt", target));
await cp(new URL("webdemo/main.css", repository), new URL("main.css", target));
await cp(new URL("webdemo/main.js", repository), new URL("main.js", target));
await cp(new URL("webdemo/images/", repository), new URL("images/", target), { recursive: true });
await cp(new URL("panel/style.css", repository), new URL("style.css", target));
await copyEntryModules(["webdemo.js", "static_docs.js"], target);
await cp(new URL("panel/vendor/", repository), new URL("vendor/", target), { recursive: true });
await writeFile(new URL("index.html", target), await minifiedHTML(new URL("webdemo/index.html", repository), [], socialPages.landing));
await mkdir(new URL("demo/", target), { recursive: true });
await writeFile(new URL("demo/index.html", target), await minifiedHTML(new URL("panel/index.html", repository), [
  ["</head>", "<meta name=\"almagest-site-base\" content=\"../\"></head>"],
  ["script-src 'self'", "script-src 'self' 'wasm-unsafe-eval'"],
  ["class=\"header-logo-link\" href=\"./\"", "class=\"header-logo-link\" href=\"../\""],
  ["href=\"./favicon.svg\"", "href=\"../favicon.svg\""],
  ["href=\"./vendor/", "href=\"../vendor/"],
  ["href=\"./style.css\"", "href=\"../style.css\""],
  ["src=\"./vendor/", "src=\"../vendor/"],
  ["src=\"./logo.svg\"", "src=\"../logo.svg\""],
  ["src=\"./public/build/admin.js\"", "src=\"../public/build/webdemo.js\""],
], socialPages.demo));
await mkdir(new URL("docs/", target), { recursive: true });
await writeFile(new URL("docs/index.html", target), await minifiedHTML(new URL("panel/docs.html", repository), [
  ["content=\"/\"", "content=\"../../\""],
  ["href=\"/favicon.svg\"", "href=\"../favicon.svg\""],
  ["href=\"/style.css\"", "href=\"../style.css\""],
  ["href=\"/vendor/", "href=\"../vendor/"],
  ["class=\"header-logo-link\" href=\"/\"", "class=\"header-logo-link\" href=\"../\""],
  ["src=\"/logo.svg\"", "src=\"../logo.svg\""],
  ["src=\"/vendor/", "src=\"../vendor/"],
  ["src=\"/public/build/daemon_docs.js\"", "src=\"../public/build/static_docs.js\""],
  ["href=\"/\" target=\"_blank\">Admin panel", "href=\"../demo/\" target=\"_blank\">Web demo"],
], socialPages.docs));
const nodeClassCache = await publishNodeClassCache();
const availableNodeClasses = await webDemoAvailableNodeClasses(nodeClassCache);
await buildDocsCatalog(nodeClassCache, availableNodeClasses);
await optimize(target);
await buildWASM();
