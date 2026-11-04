import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { access, readFile, readdir, stat } from "node:fs/promises";
import { test } from "node:test";
import { promisify } from "node:util";
import { JSDOM } from "jsdom";

const output = (path) => new URL(`../../build/webdemo/${path}`, import.meta.url);
const webDemoNodeClasses = JSON.parse(await readFile(new URL("../../webdemo/available-node-classes.json", import.meta.url), "utf8"));
await promisify(execFile)(process.execPath, ["scripts/publish-webdemo.mjs", "../build/webdemo"], {
  cwd: new URL("..", import.meta.url),
});

test("web demo publication is a complete relative static tree", async () => {
  for (const path of [
    "index.html",
    "main.css",
    "main.js",
    "locales/en/panel.json",
    "images/orbit.svg",
    "images/graph.png",
    "images/settings.png",
    "images/workspace.png",
    "images/routing.png",
    "images/dns.png",
    "images/tls.png",
    "images/local-proxy.png",
    "favicon.svg",
    "social-preview.png",
    "LUCIDE_LICENSE.txt",
    "demo/index.html",
    "docs/index.html",
    "docs/content/catalog.json",
    "docs/content/node-classes.json",
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
  assert.equal(landing.querySelectorAll(".brand img[src='./logo.svg']").length, 2);
  assert.equal(landing.querySelector(".orbit img")?.getAttribute("src"), "./images/orbit.svg");
  const captures = [...landing.querySelectorAll(".screenshot-image img, .feature-image img")];
  assert.equal(captures.length, 9);
  assert.equal(landing.querySelector(".placeholder-label, img[src*='-placeholder']"), null);
  for (const capture of captures) {
    assert.match(capture.getAttribute("src"), /\.png$/);
    assert.doesNotMatch(capture.getAttribute("alt"), /placeholder/i);
    const png = await readFile(output(capture.getAttribute("src")));
    assert.equal(png.readUInt32BE(16), Number(capture.getAttribute("width")));
    assert.equal(png.readUInt32BE(20), Number(capture.getAttribute("height")));
  }
  assert.equal(landing.querySelector("link[rel='icon']")?.getAttribute("href"), "./favicon.svg");
  assert.equal(demo.querySelector("link[rel='icon']")?.getAttribute("href"), "../favicon.svg");
  assert.equal(docs.querySelector("link[rel='icon']")?.getAttribute("href"), "../favicon.svg");
  assert.equal(demo.querySelector("meta[name='almagest-site-base']")?.getAttribute("content"), "../");
  assert.match(demo.querySelector("meta[name='viewport']")?.content ?? "", /viewport-fit=cover/);
  assert.equal(demo.querySelectorAll("meta[name='almagest-site-base']").length, 1);
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

test("landing page publishes the English catalog and excludes development locales", async () => {
  assert.deepEqual(await readFile(output("locales/en/panel.json")),
    await readFile(new URL("../../locales/en/panel.json", import.meta.url)));
  await assert.rejects(access(output("locales/lorem/panel.json")));
});

test("landing page applies a second locale and preserves inline links and code", async () => {
  const dom = new JSDOM(await readFile(output("index.html"), "utf8"), {
    url: "https://example.test/almagest/?locale=zz",
    runScripts: "outside-only",
  });
  const { window } = dom;
  window.matchMedia = () => ({ matches: false });
  window.fetch = async (path) => ({
    ok: true,
    json: async () => path === "./docs/content/catalog.json"
      ? { available: [{ locale: "en", name: "English" }, { locale: "zz", name: "Test" }] }
      : path === "./locales/zz/panel.json"
        ? {
          "landing.hero.build-your-whole-network-item-item": "Red {1} {0}",
          "landing.hero.in-one-place": "en un lugar",
          "landing.hero.item-many-possible-paths": "Rutas: {0}.",
          "landing.hero.90-node-classes": "90+ tipos de nodos",
          "locale.label": "Idioma",
        }
        : {},
  });
  window.eval(await readFile(output("main.js"), "utf8"));
  const localeSelect = window.document.querySelector(".locale-switch select");
  for (let tries = 0; tries < 20 && localeSelect.value !== "zz"; tries++) {
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  assert.equal(localeSelect.value, "zz");
  assert.equal(window.document.documentElement.lang, "zz");
  assert.equal(window.document.querySelector("#hero-title").textContent, "Red en un lugar ");
  assert.equal(window.document.querySelector(".orbit-label").textContent, "Rutas: 90+ tipos de nodos.");
  assert.equal(window.document.querySelector(".orbit-label a").getAttribute("href"),
    "./docs/?page=node%2Falmagest%2FDns2Network");
  assert.match(window.document.querySelector("#install-nix code").textContent, /nix profile add/);
  assert.equal(window.document.querySelector(".locale-switch").hidden, false);
  localeSelect.value = "en";
  localeSelect.dispatchEvent(new window.Event("change"));
  for (let tries = 0; tries < 20 && window.document.documentElement.lang !== "en"; tries++) {
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  assert.equal(window.document.querySelector("#hero-title").textContent, "Build your whole networkin one place.");
  assert.equal(window.document.querySelector(".orbit-label").textContent, "90+ node classes. Many possible paths.");
  dom.window.close();
});

test("web demo favicon is generated from the light logo", async () => {
  assert.deepEqual(
    await readFile(output("favicon.svg")),
    await readFile(new URL("../../logo-light.svg", import.meta.url)),
  );
});

test("web demo renders a 1280 by 640 social preview and publishes page metadata", async () => {
  const png = await readFile(output("social-preview.png"));
  assert.deepEqual([...png.subarray(0, 8)], [137, 80, 78, 71, 13, 10, 26, 10]);
  assert.equal(png.readUInt32BE(16), 1280);
  assert.equal(png.readUInt32BE(20), 640);

  const base = "https://asciimoth.github.io/almagest/";
  for (const [path, pageURL, title] of [
    ["index.html", base, "Almagest"],
    ["demo/index.html", `${base}demo/`, "Almagest Web Demo"],
    ["docs/index.html", `${base}docs/`, "Almagest Documentation"],
  ]) {
    const document = new JSDOM(await readFile(output(path), "utf8")).window.document;
    const property = (name) => document.querySelector(`meta[property='${name}']`)?.content;
    const named = (name) => document.querySelector(`meta[name='${name}']`)?.content;
    assert.equal(document.querySelector("link[rel='canonical']")?.href, pageURL);
    assert.ok(named("description"));
    assert.equal(property("og:type"), "website");
    assert.equal(property("og:site_name"), "Almagest");
    assert.equal(property("og:url"), pageURL);
    assert.equal(property("og:title"), title);
    assert.equal(property("og:description"), named("description"));
    assert.equal(property("og:image"), `${base}social-preview.png`);
    assert.equal(property("og:image:type"), "image/png");
    assert.equal(property("og:image:width"), "1280");
    assert.equal(property("og:image:height"), "640");
    assert.ok(property("og:image:alt"));
    assert.equal(named("twitter:card"), "summary_large_image");
    assert.equal(named("twitter:title"), title);
    assert.equal(named("twitter:description"), named("description"));
    assert.equal(named("twitter:image"), property("og:image"));
    assert.equal(named("twitter:image:alt"), property("og:image:alt"));
  }
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

test("the static documentation catalog preserves node page metadata", async () => {
  const catalog = JSON.parse(await readFile(output("docs/content/catalog.json"), "utf8"));
  const nodeTopic = catalog.locales.en.tree.find((topic) => topic.id === "nodes");
  for (const page of nodeTopic.pages) {
    assert.deepEqual(page.marks, page.available ? ["available"] : []);
  }
  const publishedClasses = nodeTopic.pages.map((page) => page.id.slice("node/".length));
  const cache = JSON.parse(await readFile(output("docs/content/node-classes.json"), "utf8"));
  assert.deepEqual(publishedClasses, cache.order.filter((className) => publishedClasses.includes(className)));
});

test("the web demo publishes production node preview metadata", async () => {
  const cache = JSON.parse(await readFile(output("docs/content/node-classes.json"), "utf8"));
  assert.equal(cache.version, 1);
  assert.deepEqual(new Set(cache.order), new Set(Object.keys(cache.classes)));
  assert.ok(Object.keys(cache.classes).length > 0);
  for (const metadata of Object.values(cache.classes)) {
    assert.equal(typeof metadata.primary_type, "string");
    assert.equal(Array.isArray(metadata.initial_ports), true);
  }
  assert.equal(Object.values(cache.classes).some((entry) => Object.hasOwn(entry, "short_description")), false);
});

test("every catalog page maps to a published Markdown file", async () => {
  const catalog = JSON.parse(await readFile(output("docs/content/catalog.json"), "utf8"));
  const localeRoot = new URL("../../locales/", import.meta.url);
  for (const entry of await readdir(localeRoot, { withFileTypes: true })) {
    if (!entry.isDirectory()) continue;
    const manifest = JSON.parse(await readFile(new URL(`${entry.name}/manifest.json`, localeRoot), "utf8"));
    if (manifest.development_only) {
      assert.equal(Object.hasOwn(catalog.locales, manifest.locale), false,
        `development-only locale ${manifest.locale} was published`);
    }
  }

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

test("published web demo has no native imports or local file URLs", async () => {
  const files = await readdir(output("public/build/"));
  for (const name of ["admin.js", "daemon_docs.js", "websocket_admin_transport.js", "http_docs_client.js",
    "desktop.js", "desktop_admin_transport.js", "desktop_docs.js", "desktop_docs_client.js"]) {
    assert.equal(files.includes(name), false, name);
  }
  for (const name of ["webdemo.js", "static_docs.js", "wasm_transport.js"]) {
    assert.ok(files.includes(name), name);
  }
  assert.equal(files.some((name) => name.startsWith("desktop") && name !== "desktop_event_log.js" && name !== "desktop_route_memory.js"), false);
  for (const name of files.filter((name) => name.endsWith(".js"))) {
    const source = await readFile(output(`public/build/${name}`), "utf8");
    assert.doesNotMatch(source, /@tauri-apps\/|file:\/\//, name);
  }
  const demoEntry = await readFile(new URL("../public/build/webdemo.js", import.meta.url), "utf8");
  const docsEntry = await readFile(new URL("../public/build/static_docs.js", import.meta.url), "utf8");
  assert.match(demoEntry, /new WASMAdminTransport\(/);
  assert.match(demoEntry, /await import\("\.\/app\.js"\)/);
  assert.match(docsEntry, /new StaticDocsClient\(/);
  assert.match(docsEntry, /await import\("\.\/docs\.js"\)/);
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
  assert.ok(Object.keys(response.result.snapshot.nodes).length > 0);
  assert.ok(Object.keys(response.result.snapshot.ports).length > 0);
  assert.ok(Object.keys(response.result.snapshot.links).length > 0);
});

test("published Go Wasm runs and persists all web-demo node classes", async () => {
  const { stdout } = await promisify(execFile)(process.execPath, [
    new URL("helpers/run_webdemo_wasm_mutation.mjs", import.meta.url).pathname,
    output("").pathname,
  ], { maxBuffer: 8 * 1024 * 1024 });
  const result = JSON.parse(stdout);
  assert.equal(result.subscribed.error, undefined);
  assert.deepEqual(new Set(result.classes), new Set(webDemoNodeClasses));
  assert.equal(result.added.every((response) => response.error === undefined), true);
  assert.equal(Object.keys(result.snapshot.result.snapshot.nodes).length,
    result.initialNodeCount + result.added.length);
  assert.equal(result.events.some((event) => event.surface === "engine" && event.method === "notification"), true);
  for (const className of webDemoNodeClasses) assert.match(result.config, new RegExp(className.replace("/", "\\/")));
  assert.equal(result.persistedConfig, result.config);
});

test("published Go Wasm uses in-memory storage when localStorage is absent", async () => {
  const { stdout } = await promisify(execFile)(process.execPath, [
    new URL("helpers/run_webdemo_wasm_mutation.mjs", import.meta.url).pathname,
    output("").pathname,
    "no-storage",
  ], { maxBuffer: 8 * 1024 * 1024 });
  const result = JSON.parse(stdout);
  assert.equal(result.added.every((response) => response.error === undefined), true);
  assert.equal(Object.keys(result.snapshot.result.snapshot.nodes).length,
    result.initialNodeCount + result.added.length);
  for (const className of webDemoNodeClasses) assert.match(result.config, new RegExp(className.replace("/", "\\/")));
  assert.equal(result.persistedConfig, null);
});
