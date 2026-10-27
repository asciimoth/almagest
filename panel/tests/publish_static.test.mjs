import assert from "node:assert/strict";
import { access, readFile, stat } from "node:fs/promises";
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
  await access(published("LUCIDE_LICENSE.txt.gz"));
  await access(published("public/build/app.js.gz"));
  await assert.rejects(access(published("index.html")));
  await assert.rejects(access(published("public/build/app.js")));
  await assert.rejects(access(published("public/build/app.d.ts")));
  await assert.rejects(access(published("vendor/litegraph/litegraph.d.ts")));
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
  assert.match(publishedDocument.querySelector("script[type='module']")?.getAttribute("src") ?? "", /^\.\/public\/build\/app\.js\?v=[a-f0-9]{16}$/);
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
  assert.match(document.querySelector("script[type='module']")?.src ?? "", /^http:\/\/127\.0\.0\.1:8090\/public\/build\/docs\.js\?v=[a-f0-9]{16}$/);
});
