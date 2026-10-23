import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { access, readFile, readdir, stat } from "node:fs/promises";
import { test } from "node:test";
import { promisify } from "node:util";
import { JSDOM } from "jsdom";

const output = (path) => new URL(`../../build/webdemo/${path}`, import.meta.url);
await promisify(execFile)(process.execPath, ["scripts/publish-webdemo.mjs", "../build/webdemo"], {
  cwd: new URL("..", import.meta.url),
});

test("web demo publication is a complete relative static tree", async () => {
  for (const path of [
    "index.html",
    "demo/index.html",
    "docs/index.html",
    "docs/content/catalog.json",
    "docs/content/en/pages/guides/welcome.md",
    "public/build/webdemo.js",
    "public/build/static_docs.js",
  ]) await access(output(path));

  const landing = new JSDOM(await readFile(output("index.html"), "utf8"), {
    url: "https://example.test/almagest/",
  }).window.document;
  const demo = new JSDOM(await readFile(output("demo/index.html"), "utf8"), {
    url: "https://example.test/almagest/demo/",
  }).window.document;
  const docs = new JSDOM(await readFile(output("docs/index.html"), "utf8"), {
    url: "https://example.test/almagest/docs/",
  }).window.document;
  assert.equal(landing.querySelector("a[href='./docs/']") !== null, true);
  assert.equal(landing.querySelector("a[href='./demo/']") !== null, true);
  assert.equal(demo.querySelector("meta[name='almagest-site-base']")?.getAttribute("content"), "../");
  assert.equal(demo.querySelector(".header-logo-link")?.getAttribute("href"), "../");
  assert.equal(docs.querySelector(".header-logo-link")?.getAttribute("href"), "../");
  assert.equal(demo.querySelector(".header-logo-link")?.href, "https://example.test/almagest/");
  assert.equal(docs.querySelector(".header-logo-link")?.href, "https://example.test/almagest/");
  assert.equal(demo.querySelector("script[type='module']")?.getAttribute("src"), "../public/build/webdemo.js");
  assert.equal(docs.querySelector("meta[name='almagest-site-base']")?.getAttribute("content"), "../../");
  assert.equal(docs.querySelector("script[type='module']")?.getAttribute("src"), "../public/build/static_docs.js");
  assert.equal(docs.querySelector("#admin-panel-link")?.getAttribute("href"), "../demo/");
  assert.equal(docs.querySelector("#admin-panel-link")?.textContent, "Web demo");
  assert.equal(demo.querySelector("script[type='module']")?.src, "https://example.test/almagest/public/build/webdemo.js");
  assert.equal(docs.querySelector("link[rel='stylesheet']")?.href, "https://example.test/almagest/style.css");
});

test("web demo pages contain no root-relative local asset or navigation URLs", async () => {
  for (const [path, url] of [
    ["index.html", "https://example.test/almagest/"],
    ["demo/index.html", "https://example.test/almagest/demo/"],
    ["docs/index.html", "https://example.test/almagest/docs/"],
  ]) {
    const document = new JSDOM(await readFile(output(path), "utf8"), { url }).window.document;
    for (const element of document.querySelectorAll("[href], [src]")) {
      for (const attribute of ["href", "src"]) {
        const value = element.getAttribute(attribute);
        if (value === null || /^(?:data:|https?:|#)/.test(value)) continue;
        assert.equal(value.startsWith("/"), false, `${path}: ${attribute}=${value}`);
        const resolved = new URL(value, url);
        assert.equal(resolved.pathname.startsWith("/almagest/"), true, `${path}: ${resolved.href}`);
      }
    }
  }
});

test("published web demo retains the complete existing panel document contract", async () => {
  const source = new JSDOM(await readFile(new URL("../index.html", import.meta.url), "utf8")).window.document;
  const demo = new JSDOM(await readFile(output("demo/index.html"), "utf8")).window.document;
  const sourceIDs = [...source.querySelectorAll("[id]")].map((element) => element.id).sort();
  const demoIDs = [...demo.querySelectorAll("[id]")].map((element) => element.id).sort();

  assert.deepEqual(demoIDs, sourceIDs);
  assert.equal(demo.querySelectorAll("[data-tab]").length, source.querySelectorAll("[data-tab]").length);
  assert.equal(demo.querySelectorAll("[data-panel]").length, source.querySelectorAll("[data-panel]").length);
  assert.equal(demo.querySelectorAll("input, select, textarea, button").length, source.querySelectorAll("input, select, textarea, button").length);
});

test("the static documentation catalog includes raw and custom-link pages", async () => {
  const catalog = JSON.parse(await readFile(output("docs/content/catalog.json"), "utf8"));
  assert.deepEqual(catalog.available, [{ locale: "en", name: "English" }]);
  assert.equal(catalog.locales.en.pages["guide/welcome"], "content/en/pages/guides/welcome.md");
  assert.equal(catalog.locales.en.pages["node/almagest/MeshNames"], "content/en/pages/nodes/almagest/MeshNames.md");
  const source = await readFile(output("docs/content/en/pages/nodes/almagest/DnsInject.md"), "utf8");
  assert.match(source, /\[MeshNames\]\(@docs\/node\/almagest\/MeshNames\)/);
});

test("every catalog page maps to a published Markdown file", async () => {
  const catalog = JSON.parse(await readFile(output("docs/content/catalog.json"), "utf8"));
  assert.equal(Object.hasOwn(catalog.locales, "lorem"), false, "development-only locale was published");

  for (const [locale, content] of Object.entries(catalog.locales)) {
    assert.ok(Object.keys(content.pages).length > 0, `${locale} has no documentation pages`);
    for (const [id, path] of Object.entries(content.pages)) {
      assert.match(path, /^content\/[a-zA-Z0-9_-]+\/pages\/.+\.md$/);
      await access(output(`docs/${path}`), undefined, `${locale}:${id}`);
    }
  }
});

test("web demo output is optimized and contains no TypeScript declarations", async () => {
  async function files(directory, prefix = "") {
    const result = [];
    for (const entry of await readdir(directory, { withFileTypes: true })) {
      const path = `${prefix}${entry.name}`;
      if (entry.isDirectory()) result.push(...await files(new URL(`${entry.name}/`, directory), `${path}/`));
      else result.push(path);
    }
    return result;
  }

  const allFiles = await files(output(""));
  assert.equal(allFiles.some((path) => path.endsWith(".d.ts")), false);
  const sourceSize = (await stat(new URL("../public/build/webdemo.js", import.meta.url))).size;
  const publishedSize = (await stat(output("public/build/webdemo.js"))).size;
  assert.ok(publishedSize < sourceSize, "web demo JavaScript was not minified");
  const html = await readFile(output("index.html"), "utf8");
  assert.doesNotMatch(html, /\n\s{2,}</);
});
