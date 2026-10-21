import assert from "node:assert/strict";
import { access, readFile } from "node:fs/promises";
import { test } from "node:test";
import { JSDOM } from "jsdom";

const docsHTML = await readFile(new URL("../docs.html", import.meta.url), "utf8");
const panelHTML = await readFile(new URL("../index.html", import.meta.url), "utf8");
const docsSource = await readFile(new URL("../src/docs.ts", import.meta.url), "utf8");
const appSource = await readFile(new URL("../src/app.ts", import.meta.url), "utf8");
const windowNavigationSource = await readFile(new URL("../src/window_navigation.ts", import.meta.url), "utf8");
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

test("documentation reuses an open admin panel tab or opens a new tab", () => {
  const dom = new JSDOM(docsHTML);
  const link = dom.window.document.querySelector("#admin-panel-link");

  assert.equal(link?.getAttribute("href"), "/");
  assert.equal(link?.getAttribute("target"), "_blank");
  assert.match(docsSource, /adminPanelLink\.target = adminPanelWindowName\(window\.location\.origin\)/);
  assert.match(docsSource, /openOrFocusAdminPanel\(window, adminPanelLink\.href\)/);
  assert.match(appSource, /window\.name = adminPanelWindowName\(window\.location\.origin\)/);
  assert.match(windowNavigationSource, /source\.open\("", adminPanelWindowName\(source\.location\.origin\)\)/);
  assert.match(windowNavigationSource, /panel\.location\.href === "about:blank"/);
  assert.match(windowNavigationSource, /panel\.focus\(\)/);
});

test("documentation loads localized pages over HTTP without a WebSocket", () => {
  assert.match(docsSource, /docsAPIURL\("tree"\)/);
  assert.match(docsSource, /docsAPIURL\("raw", \{ id \}\)/);
  assert.match(docsSource, /renderNodeDescription\(content, contentText, "docs"\)/);
  assert.match(docsSource, /scrollToCurrentAnchor\(\)/);
  assert.match(docsSource, /decodeURIComponent\(window\.location\.hash\.slice\(1\)\)/);
  assert.match(docsSource, /docsURL\(currentPage, window\.location\.hash\)/);
  assert.match(docsSource, /docsAPIURL\("node-class", \{ id \}\)/);
  assert.match(docsSource, /void loadNodePreview\(id, title\)/);
  assert.match(docsSource, /page\.available === false/);
  assert.match(docsSource, /docs-node-unavailable-dot/);
  assert.match(docsSource, /if \(!unavailable\) void loadNodePreview/);
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
  assert.match(appSource, /docsLink\.rel = "opener"/);
  assert.match(appSource, /documentationURL\(`node\/\$\{node\.class\}`\)/);
  assert.match(appSource, /button\.addEventListener\("contextmenu"/);
  assert.match(appSource, /localizedText\("node\.docs\.context_menu", "Open documentation"\)/);
  assert.match(appSource, /window\.open\(documentationURL\(`node\/\$\{cls\.class\}`\), "_blank"\)/);
  assert.match(appSource, /short_description\.trimStart\(\)\.startsWith\("DEBUG "\)/);
  assert.match(appSource, /url\.pathname \+= page\.split\("\/"\)\.map\(encodeURIComponent\)\.join\("\/"\)/);
  assert.doesNotMatch(appSource.match(/function documentationURL[\s\S]*?\n\}/)?.[0] ?? "", /locale/);
});

test("published assets include the documentation page and module", async () => {
  await access(published("docs/index.html.gz"));
  await access(published("public/build/docs.js.gz"));
  await access(published("logo.svg.gz"));
});
