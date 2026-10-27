import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { access, readFile, readdir, stat } from "node:fs/promises";
import { test } from "node:test";
import { promisify } from "node:util";
import { JSDOM } from "jsdom";

const output = (path) => new URL(`../../build/webdemo/${path}`, import.meta.url);
const defaultWebDemoNodeCount = 15;
const webDemoNodeClasses = JSON.parse(await readFile(new URL("../../webdemo/available-node-classes.json", import.meta.url), "utf8"));
await promisify(execFile)(process.execPath, ["scripts/publish-webdemo.mjs", "../build/webdemo"], {
  cwd: new URL("..", import.meta.url),
});

test("web demo publication is a complete relative static tree", async () => {
  for (const path of [
    "index.html",
    "LUCIDE_LICENSE.txt",
    "demo/index.html",
    "docs/index.html",
    "docs/content/catalog.json",
    "docs/content/node-classes.json",
    "docs/content/en/pages/almagest/start.md",
    "public/build/webdemo.js",
    "public/build/static_docs.js",
    "almagest.wasm",
    "wasm_exec.js",
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
  const demoPolicy = demo.querySelector("meta[http-equiv='Content-Security-Policy']")?.getAttribute("content") ?? "";
  assert.match(demoPolicy, /script-src 'self' 'wasm-unsafe-eval'/);
  assert.doesNotMatch(demoPolicy, /script-src[^;]*'unsafe-eval'/);
  assert.equal(docs.querySelector("meta[name='almagest-site-base']")?.getAttribute("content"), "../../");
  assert.equal(docs.querySelector("script[type='module']")?.getAttribute("src"), "../public/build/static_docs.js");
  assert.equal(docs.querySelector("#admin-panel-link")?.getAttribute("href"), "../demo/");
  assert.equal(docs.querySelector("#admin-panel-link")?.textContent, "Web demo");
  assert.equal(demo.querySelector("script[type='module']")?.src, "https://example.test/almagest/public/build/webdemo.js");
  assert.equal(docs.querySelector("link[href='../style.css']")?.href, "https://example.test/almagest/style.css");
  assert.equal(docs.querySelector("link[href='../vendor/litegraph/litegraph.css']")?.href,
    "https://example.test/almagest/vendor/litegraph/litegraph.css");
  assert.equal(docs.querySelector("script[src='../vendor/litegraph/litegraph.js']")?.src,
    "https://example.test/almagest/vendor/litegraph/litegraph.js");
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
  assert.equal(catalog.locales.en.pages["almagest/start"], "content/en/pages/almagest/start.md");
  assert.equal(catalog.locales.en.pages["node/almagest/MeshNames"], "content/en/pages/nodes/almagest/MeshNames.md");
  const nodeTopic = catalog.locales.en.tree.find((topic) => topic.id === "nodes");
  const availablePage = nodeTopic.pages.find((page) => page.id === "node/almagest/MeshNames");
  assert.equal(availablePage.available, true);
  assert.deepEqual(availablePage.marks, ["available"]);
  const unavailablePage = nodeTopic.pages.find((page) => page.id === "node/almagest/Tun");
  assert.equal(unavailablePage.available, false);
  assert.deepEqual(unavailablePage.marks, []);
  for (const page of nodeTopic.pages) {
    assert.deepEqual(page.marks, page.available ? ["available"] : []);
  }
  const publishedClasses = nodeTopic.pages.map((page) => page.id.slice("node/".length));
  const cache = JSON.parse(await readFile(output("docs/content/node-classes.json"), "utf8"));
  assert.deepEqual(publishedClasses, cache.order.filter((className) => publishedClasses.includes(className)));
  const source = await readFile(output("docs/content/en/pages/nodes/almagest/DnsInject.md"), "utf8");
  assert.match(source, /\[MeshNames\]\(@docs\/node\/almagest\/MeshNames\)/);
});

test("the web demo publishes production node preview metadata", async () => {
  const cache = JSON.parse(await readFile(output("docs/content/node-classes.json"), "utf8"));
  assert.equal(cache.version, 1);
  assert.deepEqual(new Set(cache.order), new Set(Object.keys(cache.classes)));
  assert.ok(Object.keys(cache.classes).length > 50);
  assert.equal(cache.classes["almagest/Command"].primary_type, "pasta/trigger");
  assert.equal(cache.classes["almagest/NetworkFirewall"].initial_ports[0].name, "Network");
  assert.equal(Object.values(cache.classes).some((entry) => Object.hasOwn(entry, "short_description")), false);
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

test("web demo contains a valid Go Wasm backend and its matching runtime", async () => {
  const wasm = await readFile(output("almagest.wasm"));
  assert.deepEqual([...wasm.subarray(0, 4)], [0x00, 0x61, 0x73, 0x6d]);
  const runtime = await readFile(output("wasm_exec.js"), "utf8");
  assert.match(runtime, /globalThis\.Go = class/);
  const entrypoint = await readFile(output("public/build/wasm_transport.js"), "utf8");
  assert.match(entrypoint, /almagest\.wasm/);
  assert.match(entrypoint, /wasm_exec\.js/);
});

test("published Go Wasm starts and exposes its node class list API", async () => {
  const { stdout } = await promisify(execFile)(process.execPath, [
    new URL("helpers/run_webdemo_wasm.mjs", import.meta.url).pathname,
    output("").pathname,
  ]);
  const { logs, snapshots, tls, workspace: response } = JSON.parse(stdout);
  assert.equal(logs.error, undefined);
  assert.ok(logs.result.subscription_id > 0);
  assert.ok(logs.result.cache_size > 0);
  assert.ok(logs.result.entries.some((entry) => entry.node_class === "almagest/HttpServer"));
  assert.equal(snapshots.error, undefined);
  assert.deepEqual(snapshots.result.snapshots, []);
  assert.equal(tls.error, undefined);
  assert.deepEqual(tls.result.certs, []);
  assert.deepEqual(tls.result.policies, []);
  assert.equal(tls.result.builtin_ca, undefined);
  assert.equal(response.id, "smoke");
  assert.equal(response.error, undefined);
  assert.deepEqual(new Set(response.result.snapshot.classes.map((entry) => entry.class)), new Set(webDemoNodeClasses));
  assert.equal(Object.keys(response.result.snapshot.nodes).length, defaultWebDemoNodeCount);
  assert.ok(Object.keys(response.result.snapshot.ports).length > 0);
  assert.equal(Object.keys(response.result.snapshot.links).length, 13);
});

test("published Go Wasm runs and persists all web-demo node classes", async () => {
  const { stdout } = await promisify(execFile)(process.execPath, [
    new URL("helpers/run_webdemo_wasm_mutation.mjs", import.meta.url).pathname,
    output("").pathname,
  ]);
  const result = JSON.parse(stdout);
  assert.equal(result.subscribed.error, undefined);
  assert.deepEqual(result.classes, webDemoNodeClasses);
  assert.equal(result.added.every((response) => response.error === undefined), true);
  assert.equal(Object.keys(result.snapshot.result.snapshot.nodes).length, defaultWebDemoNodeCount + webDemoNodeClasses.length);
  assert.equal(result.events.some((event) => event.surface === "engine" && event.method === "notification"), true);
  for (const className of webDemoNodeClasses) assert.match(result.config, new RegExp(className.replace("/", "\\/")));
  assert.equal(result.persistedConfig, result.config);
});

test("published Go Wasm uses in-memory storage when localStorage is absent", async () => {
  const { stdout } = await promisify(execFile)(process.execPath, [
    new URL("helpers/run_webdemo_wasm_mutation.mjs", import.meta.url).pathname,
    output("").pathname,
    "no-storage",
  ]);
  const result = JSON.parse(stdout);
  assert.equal(result.added.every((response) => response.error === undefined), true);
  assert.equal(Object.keys(result.snapshot.result.snapshot.nodes).length, defaultWebDemoNodeCount + webDemoNodeClasses.length);
  for (const className of webDemoNodeClasses) assert.match(result.config, new RegExp(className.replace("/", "\\/")));
  assert.equal(result.persistedConfig, null);
});
