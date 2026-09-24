import assert from "node:assert/strict";
import { access, readFile, stat } from "node:fs/promises";
import { test } from "node:test";
import { gunzipSync } from "node:zlib";

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
  await access(published("public/build/app.js.gz"));
  await assert.rejects(access(published("index.html")));
  await assert.rejects(access(published("public/build/app.js")));
  await assert.rejects(access(published("public/build/app.d.ts")));
  await assert.rejects(access(published("vendor/litegraph/litegraph.d.ts")));
});
