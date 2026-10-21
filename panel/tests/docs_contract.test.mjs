import assert from "node:assert/strict";
import { access, readFile } from "node:fs/promises";
import { test } from "node:test";
import { JSDOM } from "jsdom";

const docsHTML = await readFile(new URL("../docs.html", import.meta.url), "utf8");
const panelHTML = await readFile(new URL("../index.html", import.meta.url), "utf8");
const docsSource = await readFile(new URL("../src/docs.ts", import.meta.url), "utf8");
const panelStyle = await readFile(new URL("../style.css", import.meta.url), "utf8");
const appSource = await readFile(new URL("../src/app.ts", import.meta.url), "utf8");
const windowNavigationSource = await readFile(new URL("../src/window_navigation.ts", import.meta.url), "utf8");
const published = (path) => new URL(`../../bin/almagestd/static/${path}`, import.meta.url);

test("documentation uses a separate page and the shared panel stylesheet", () => {
  const dom = new JSDOM(docsHTML);
  const document = dom.window.document;

  assert.equal(document.body.classList.contains("docs-body"), true);
  assert.equal(document.querySelector("#docs-tree")?.tagName, "NAV");
  assert.equal(document.querySelector("#docs-page")?.tagName, "ARTICLE");
  assert.equal(document.querySelector("#docs-navigation-toggle")?.getAttribute("aria-controls"), "docs-tree");
  assert.equal(document.querySelector("#docs-navigation-scrim")?.getAttribute("aria-label"), "Close documentation topics");
  assert.equal(document.querySelector("#docs-locale-select")?.tagName, "SELECT");
  assert.equal(document.querySelector("link[rel='stylesheet']")?.getAttribute("href"), "/style.css");
  assert.equal(document.querySelector("script[type='module']")?.getAttribute("src"), "/public/build/docs.js");
});

test("panel and documentation headers show the embedded logo before their titles", () => {
  const docsDocument = new JSDOM(docsHTML).window.document;
  const panelDocument = new JSDOM(panelHTML).window.document;

  for (const [document, brandSelector] of [
    [panelDocument, ".app-brand"],
    [docsDocument, ".docs-brand"],
  ]) {
    const brand = document.querySelector(brandSelector);
    assert.equal(brand?.firstElementChild?.tagName, "IMG");
    assert.equal(brand?.firstElementChild?.getAttribute("src"), "/logo.svg");
    assert.equal(brand?.firstElementChild?.getAttribute("alt"), "");
    assert.ok(brand?.querySelector("h1"));
  }
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
  assert.match(docsSource, /renderNodeDescription\(content, contentText, "docs", id, highlightPreviewPort\)/);
  assert.match(docsSource, /scrollToCurrentAnchor\(\)/);
  assert.match(docsSource, /decodeURIComponent\(window\.location\.hash\.slice\(1\)\)/);
  assert.match(docsSource, /docsURL\(currentPage, window\.location\.hash\)/);
  assert.match(docsSource, /docsAPIURL\("node-class", \{ id \}\)/);
  assert.match(docsSource, /void loadNodePreview\(id, title\)/);
  assert.match(docsSource, /port\.dataset\.portSide === side && port\.dataset\.portName === name/);
  assert.match(docsSource, /port\.classList\.toggle\("class-preview-port-highlighted", highlighted\)/);
  assert.match(docsSource, /renderPageMarks\(page\.marks/);
  assert.match(docsSource, /deprecated: "D"/);
  assert.match(docsSource, /status\.title = markMessages\[mark\]/);
  assert.match(docsSource, /status\.setAttribute\("aria-label", markMessages\[mark\]\)/);
  assert.doesNotMatch(docsSource, /innerHTML/);
  assert.match(docsSource, /if \(!unavailable\) void loadNodePreview/);
  assert.match(docsSource, /console\.error\(`Could not load node preview/);
  assert.match(docsSource, /writeLocaleCookie/);
  assert.match(docsSource, /if \(urlLocale\) query\.set\("locale", urlLocale\)/);
  assert.match(docsSource, /pageIDFromPath\(window\.location\.pathname\)/);
  assert.match(docsSource, /new URL\("\/docs\/", window\.location\.href\)/);
  assert.doesNotMatch(docsSource, /searchParams\.set\("page"/);
  assert.match(docsSource, /almagest\.docs\.topic-folds\.v1/);
  assert.match(docsSource, /function setNavigationOpen\(open: boolean\)/);
  assert.match(docsSource, /event\.key === "Escape"/);
  assert.match(docsSource, /addEventListener\("toggle"/);
  assert.match(docsSource, /addEventListener\("change"/);
  assert.match(docsSource, /treeElement\.addEventListener\("wheel"/);
  assert.match(docsSource, /handoffBoundaryScroll\(treeElement, pageElement, deltaY\)/);
  assert.match(docsSource, /defaultPageID\(tree\.topics\)/);
  assert.doesNotMatch(docsSource, /WebSocket/);
});

test("documentation links adjacent pages in sidebar order", () => {
  assert.match(docsSource, /orderedPageIDs = ids/);
  assert.match(docsSource, /renderPageNavigation\(id\)/);
  assert.match(docsSource, /const previousID = orderedPageIDs\[index - 1\]/);
  assert.match(docsSource, /const nextID = orderedPageIDs\[index \+ 1\]/);
  assert.match(docsSource, /pageNavigationLink\(previousID, "prev"\)/);
  assert.match(docsSource, /pageNavigationLink\(nextID, "next"\)/);
  assert.match(docsSource, /link\.rel = direction/);
  assert.match(docsSource, /event\.ctrlKey \|\| event\.metaKey \|\| event\.shiftKey \|\| event\.altKey/);
  assert.match(panelStyle, /\.docs-page-navigation\s*\{/);
  assert.match(panelStyle, /\.docs-page-navigation-next\s*\{[^}]*grid-column:\s*2;/s);
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
