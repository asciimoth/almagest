import assert from "node:assert/strict";
import { access, readFile, readdir, stat } from "node:fs/promises";
import { test } from "node:test";
import { gunzipSync } from "node:zlib";
import { JSDOM } from "jsdom";

const source = (path) => new URL(`../${path}`, import.meta.url);
const published = (path) => new URL(`../../bin/almagestd/static/${path}`, import.meta.url);

test("published panel assets are minified", async () => {
  for (const path of ["public/build/app.js", "style.css", "vendor/litegraph/litegraph.js"]) {
    const sourceSize = (await stat(source(path))).size;
    const compressed = await readFile(published(`${path}.gz`));
    const publishedSize = gunzipSync(compressed).length;
    assert.ok(publishedSize < sourceSize, `${path}: ${publishedSize} is not less than ${sourceSize}`);
  }

  const sourceHTML = await readFile(source("index.html"), "utf8");
  const publishedHTML = gunzipSync(await readFile(published("index.html.gz"))).toString("utf8");
  assert.ok(publishedHTML.length < sourceHTML.length);
});

test("published panel includes only compressed runtime assets", async () => {
  await access(published("index.html.gz"));
  await access(published("favicon.svg.gz"));
  await access(published("LUCIDE_LICENSE.txt.gz"));
  await access(published("public/build/app.js.gz"));
  await assert.rejects(access(published("index.html")));
  await assert.rejects(access(published("public/build/app.js")));
  await assert.rejects(access(published("public/build/app.d.ts")));
  await assert.rejects(access(published("vendor/litegraph/litegraph.d.ts")));
});

test("daemon pages use the generated favicon", async () => {
  const sourceFavicon = await readFile(new URL("../../logo-light.svg", import.meta.url));
  assert.deepEqual(gunzipSync(await readFile(published("favicon.svg.gz"))), sourceFavicon);

  const panel = new JSDOM(gunzipSync(await readFile(published("index.html.gz"))).toString("utf8")).window.document;
  const docs = new JSDOM(gunzipSync(await readFile(published("docs/index.html.gz"))).toString("utf8")).window.document;
  assert.equal(panel.querySelector("link[rel='icon']")?.getAttribute("href"), "./favicon.svg");
  assert.equal(docs.querySelector("link[rel='icon']")?.getAttribute("href"), "/favicon.svg");
});

test("daemon publication retains the existing panel controls and uses the daemon entry point", async () => {
  const sourceHTML = await readFile(source("index.html"), "utf8");
  const publishedHTML = gunzipSync(await readFile(published("index.html.gz"))).toString("utf8");
  const sourceDocument = new JSDOM(sourceHTML).window.document;
  const publishedDocument = new JSDOM(publishedHTML, { url: "http://127.0.0.1:8090/" }).window.document;

  assert.deepEqual(
    [...publishedDocument.querySelectorAll("[id]")].map((element) => element.id).sort(),
    [...sourceDocument.querySelectorAll("[id]")].map((element) => element.id).sort(),
  );
  assert.match(publishedDocument.querySelector("script[type='module']")?.getAttribute("src") ?? "", /^\.\/public\/build\/admin\.js\?v=[a-f0-9]{16}$/);
  assert.equal(publishedDocument.querySelector("meta[name='almagest-site-base']"), null);
  assert.equal(publishedDocument.querySelector(".header-logo-link")?.href, "http://127.0.0.1:8090/");
});

test("daemon publication retains path-based documentation and root assets", async () => {
  const publishedHTML = gunzipSync(await readFile(published("docs/index.html.gz"))).toString("utf8");
  const document = new JSDOM(publishedHTML, {
    url: "http://127.0.0.1:8090/docs/node/almagest/NetworkFirewall",
  }).window.document;

  assert.equal(document.querySelector("meta[name='almagest-site-base']")?.getAttribute("content"), "/");
  assert.equal(document.querySelector(".header-logo-link")?.href, "http://127.0.0.1:8090/");
  assert.equal(document.querySelector("link[href='/style.css']")?.href, "http://127.0.0.1:8090/style.css");
  assert.equal(document.querySelector("script[src='/vendor/litegraph/litegraph.js']")?.src,
    "http://127.0.0.1:8090/vendor/litegraph/litegraph.js");
  assert.match(document.querySelector("script[type='module']")?.src ?? "", /^http:\/\/127\.0\.0\.1:8090\/public\/build\/daemon_docs\.js\?v=[a-f0-9]{16}$/);
});

test("daemon publication selects daemon adapters and excludes other entry points", async () => {
  const files = await readdir(published("public/build/"));
  for (const name of ["admin.js.gz", "daemon_docs.js.gz", "app.js.gz", "docs.js.gz",
    "websocket_admin_transport.js.gz", "http_docs_client.js.gz"]) {
    assert.ok(files.includes(name), name);
  }
  for (const name of ["desktop.js.gz", "desktop_admin_transport.js.gz", "desktop_docs.js.gz",
    "desktop_docs_client.js.gz", "webdemo.js.gz", "wasm_transport.js.gz", "static_docs.js.gz"]) {
    assert.equal(files.includes(name), false, name);
  }
  const admin = await readFile(source("public/build/admin.js"), "utf8");
  const docs = await readFile(source("public/build/daemon_docs.js"), "utf8");
  assert.match(admin, /new WebSocketAdminTransport\(\)/);
  assert.match(docs, /new HTTPDocsClient\(\)/);
  assert.match(admin, /await import\("\.\/app\.js"\)/);
  assert.match(docs, /await import\("\.\/docs\.js"\)/);
  for (const name of files.filter((name) => name.endsWith(".js.gz"))) {
    const source = gunzipSync(await readFile(published(`public/build/${name}`))).toString("utf8");
    assert.doesNotMatch(source, /@tauri-apps\/|almagest\.wasm|almagest\.webdemo/, name);
  }
});
