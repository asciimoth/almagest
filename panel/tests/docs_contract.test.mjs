import assert from "node:assert/strict";
import { access, readFile } from "node:fs/promises";
import { test } from "node:test";
import { JSDOM } from "jsdom";

const docsHTML = await readFile(new URL("../docs.html", import.meta.url), "utf8");
const panelHTML = await readFile(new URL("../index.html", import.meta.url), "utf8");
const docsSource = await readFile(new URL("../src/docs.ts", import.meta.url), "utf8");
const appSource = await readFile(new URL("../src/app.ts", import.meta.url), "utf8");
const published = (path) => new URL(`../../bin/almagestd/static/${path}`, import.meta.url);

test("documentation uses a separate page and the shared panel stylesheet", () => {
  const dom = new JSDOM(docsHTML);
  const document = dom.window.document;

  assert.equal(document.body.classList.contains("docs-body"), true);
  assert.equal(document.querySelector("#docs-tree")?.tagName, "NAV");
  assert.equal(document.querySelector("#docs-page")?.tagName, "ARTICLE");
  assert.equal(document.querySelector("#docs-locale-select")?.tagName, "SELECT");
  assert.equal(document.querySelector("link[rel='stylesheet']")?.getAttribute("href"), "/style.css");
  assert.equal(document.querySelector("script[type='module']")?.getAttribute("src"), "/public/build/docs.js");
});

test("documentation loads localized pages over HTTP without a WebSocket", () => {
  assert.match(docsSource, /docsAPIURL\("tree"\)/);
  assert.match(docsSource, /docsAPIURL\("raw", \{ id \}\)/);
  assert.match(docsSource, /docsAPIURL\("node-class", \{ id \}\)/);
  assert.match(docsSource, /void loadNodePreview\(id, title\)/);
  assert.match(docsSource, /console\.error\(`Could not load node preview/);
  assert.match(docsSource, /writeLocaleCookie/);
  assert.match(docsSource, /if \(urlLocale\) query\.set\("locale", urlLocale\)/);
  assert.match(docsSource, /pageIDFromPath\(window\.location\.pathname\)/);
  assert.match(docsSource, /new URL\("\/docs\/", window\.location\.href\)/);
  assert.doesNotMatch(docsSource, /searchParams\.set\("page"/);
  assert.match(docsSource, /almagest\.docs\.topic-folds\.v1/);
  assert.match(docsSource, /addEventListener\("toggle"/);
  assert.match(docsSource, /addEventListener\("change"/);
  assert.match(docsSource, /defaultPageID\(tree\.topics\)/);
  assert.doesNotMatch(docsSource, /WebSocket/);
});

test("panel documentation controls open the separate page in a new tab", () => {
  const dom = new JSDOM(panelHTML);
  const button = dom.window.document.querySelector("#tab-docs");

  assert.equal(button?.getAttribute("role"), "link");
  assert.equal(button?.hasAttribute("data-tab"), false);
  assert.match(appSource, /window\.open\(documentationURL\(\), "_blank"/);
  assert.match(appSource, /docsLink\.target = "_blank"/);
  assert.match(appSource, /documentationURL\(`node\/\$\{node\.class\}`\)/);
  assert.match(appSource, /short_description\.trimStart\(\)\.startsWith\("DEBUG "\)/);
  assert.match(appSource, /url\.pathname \+= page\.split\("\/"\)\.map\(encodeURIComponent\)\.join\("\/"\)/);
  assert.doesNotMatch(appSource.match(/function documentationURL[\s\S]*?\n\}/)?.[0] ?? "", /locale/);
});

test("published assets include the documentation page and module", async () => {
  await access(published("docs/index.html.gz"));
  await access(published("public/build/docs.js.gz"));
});
