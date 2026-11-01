import assert from "node:assert/strict";
import { access, readFile } from "node:fs/promises";
import { test } from "node:test";
import { JSDOM } from "jsdom";

const docsHTML = await readFile(new URL("../docs.html", import.meta.url), "utf8");
const panelHTML = await readFile(new URL("../index.html", import.meta.url), "utf8");
const docsSource = await readFile(new URL("../src/docs.ts", import.meta.url), "utf8");
const docsClientSource = await readFile(new URL("../src/docs_client.ts", import.meta.url), "utf8");
const httpDocsClientSource = await readFile(new URL("../src/http_docs_client.ts", import.meta.url), "utf8");
const panelStyle = await readFile(new URL("../style.css", import.meta.url), "utf8");
const appSource = await readFile(new URL("../src/app.ts", import.meta.url), "utf8");
const siteNavigationSource = await readFile(new URL("../src/site_navigation.ts", import.meta.url), "utf8");
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
  assert.equal(document.querySelector("link[href='/style.css']")?.getAttribute("href"), "/style.css");
  assert.equal(document.querySelector("link[href='/vendor/litegraph/litegraph.css']") !== null, true);
  assert.equal(document.querySelector("script[src='/vendor/litegraph/litegraph.js']") !== null, true);
  assert.equal(document.querySelector("script[type='module']")?.getAttribute("src"), "/public/build/daemon_docs.js");
  assert.equal(document.querySelector("meta[name='almagest-site-base']")?.getAttribute("content"), "/");
});

test("daemon documentation assets resolve from nested page URLs", () => {
  const document = new JSDOM(docsHTML, {
    url: "http://127.0.0.1:8090/docs/node/almagest/NetworkFirewall",
  }).window.document;

  assert.equal(document.querySelector("link[href='/style.css']")?.href, "http://127.0.0.1:8090/style.css");
  assert.equal(document.querySelector("script[src='/vendor/litegraph/litegraph.js']")?.src,
    "http://127.0.0.1:8090/vendor/litegraph/litegraph.js");
  assert.equal(document.querySelector(".header-logo")?.src, "http://127.0.0.1:8090/logo.svg");
  assert.equal(document.querySelector("script[type='module']")?.src, "http://127.0.0.1:8090/public/build/daemon_docs.js");
});

test("panel and documentation logos link to their site roots", () => {
  const docsDocument = new JSDOM(docsHTML).window.document;
  const panelDocument = new JSDOM(panelHTML).window.document;

  for (const [document, brandSelector, source, target] of [
    [panelDocument, ".app-brand", "./logo.svg", "./"],
    [docsDocument, ".docs-brand", "/logo.svg", "/"],
  ]) {
    const brand = document.querySelector(brandSelector);
    const link = brand?.firstElementChild;
    const logo = link?.firstElementChild;
    assert.equal(link?.tagName, "A");
    assert.equal(link?.getAttribute("href"), target);
    assert.equal(link?.getAttribute("aria-label"), "Almagest home");
    assert.equal(logo?.tagName, "IMG");
    assert.equal(logo?.getAttribute("src"), source);
    assert.equal(logo?.getAttribute("alt"), "");
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

test("documentation panel links route to a requested admin or web demo tab", () => {
  assert.match(docsSource, /url: currentPanelTabURL/);
  assert.match(docsSource, /adminPanelLink\.getAttribute\("href"\)/);
  assert.match(docsSource, /openAdminPanelRoute\(window, href\)/);
  assert.match(docsSource, /rememberDesktopPanelTab\(window\.localStorage, tab\)/);
  assert.match(docsSource, /target: desktopMode \? "_self" : adminPanelWindowName/);
  assert.match(siteNavigationSource, /panelHref\.replace\(\/#\.\*\$\/, ""\)/);
});

test("documentation loads localized pages over HTTP without a WebSocket", () => {
  assert.match(docsSource, /docsClient\(\)\.tree\(locale\)/);
  assert.match(docsSource, /docsClient\(\)\.raw\(locale, id\)/);
  assert.match(docsSource, /renderNodeDescription\([\s\S]*?contentText,[\s\S]*?"docs",[\s\S]*?renderDocumentationGraph/);
  assert.match(docsSource, /scrollToCurrentAnchor\(\)/);
  assert.match(docsSource, /decodeURIComponent\(window\.location\.hash\.slice\(1\)\)/);
  assert.match(docsSource, /docsURL\(currentPage, window\.location\.hash\)/);
  assert.match(docsSource, /docsClient\(\)\.nodeClass\(locale, id\)/);
  assert.match(docsSource, /docsClient\(\)\.nodeClass\(locale, `node\/\$\{className\}`\)/);
  assert.match(docsSource, /loadPreview \? loadNodePreview\(locale, id\)/);
  assert.match(docsSource, /port\.dataset\.portSide === side && port\.dataset\.portName === name/);
  assert.match(docsSource, /port\.classList\.toggle\("class-preview-port-highlighted", highlighted\)/);
  assert.match(docsSource, /renderPageMarks\(page\.marks/);
  assert.match(docsSource, /available: ""/);
  assert.match(docsSource, /deprecated: "D"/);
  assert.match(docsSource, /status\.title = markMessages\[mark\]/);
  assert.match(docsSource, /status\.setAttribute\("aria-label", markMessages\[mark\]\)/);
  assert.match(docsSource, /pageMarks\.get\(id\)\?\.includes\("deprecated"\)/);
  assert.match(docsSource, /textElement\("div", markMessages\.deprecated, "docs-node-warning docs-node-deprecated"\)/);
  assert.match(docsSource, /if \(deprecated\) deprecated\.setAttribute\("role", "status"\)/);
  assert.match(panelStyle, /\.docs-node-warning\s*\{/);
  assert.doesNotMatch(docsSource, /innerHTML/);
  assert.match(docsSource, /pageAvailability\.get\(id\) !== false \|\| docsClient\(\)\.previewsUnavailableNodes/);
  assert.match(httpDocsClientSource, /readonly previewsUnavailableNodes = false/);
  assert.match(docsSource, /console\.error\(`Could not load node preview/);
  assert.match(docsSource, /writeLocaleCookie/);
  assert.match(docsSource, /docsClient\(\)\.pageFromLocation\(window\.location\)/);
  assert.match(docsClientSource, /new URL\(configured \|\| "\.\.\/\.\.\/", import\.meta\.url\)/);
  assert.match(httpDocsClientSource, /new URL\(`api\/docs\/\$\{path\}`, siteBaseURL\(\)\)/);
  assert.match(httpDocsClientSource, /const prefix = "\/docs\/"/);
  assert.match(httpDocsClientSource, /new URL\("\/docs\/", window\.location\.href\)/);
  assert.match(docsSource, /almagest\.docs\.topic-folds\.v1/);
  assert.match(docsSource, /function setNavigationOpen\(open: boolean\)/);
  assert.match(docsSource, /event\.key === "Escape"/);
  assert.match(docsSource, /addEventListener\("toggle"/);
  assert.match(docsSource, /addEventListener\("change"/);
  assert.match(docsSource, /treeElement\.addEventListener\("wheel"/);
  assert.match(docsSource, /handoffBoundaryScroll\(treeElement, pageElement, deltaY\)/);
  assert.match(docsSource, /defaultPageID\(tree\.topics\)/);
  assert.doesNotMatch(docsSource + docsClientSource + httpDocsClientSource, /WebSocket/);
});

test("documentation swaps a staged page only after its content and node preview are ready", () => {
  const loadingReplacement = /pageElement\.replaceChildren\(textElement\("p", "Loading documentation\."\)\)/;
  const loadStart = docsSource.indexOf("const [contentText, nodeClass] = await Promise.all");
  const previewRender = docsSource.indexOf("const preview = nodeClass ? renderNodePreview(nodeClass) : null;");
  const pageSwap = docsSource.indexOf("pageElement.replaceChildren(\n      title,");

  assert.doesNotMatch(docsSource, loadingReplacement);
  assert.ok(loadStart >= 0);
  assert.ok(previewRender > loadStart);
  assert.ok(pageSwap > previewRender);
  assert.match(docsSource, /const transition = \+\+pageTransition/);
  assert.match(docsSource, /if \(transition !== pageTransition\) return/);
  assert.match(docsSource, /pageElement\.setAttribute\("aria-busy", "true"\)/);
  assert.match(docsSource, /pageElement\.removeAttribute\("aria-busy"\)/);
  assert.match(docsSource, /a\.markdown-link-docs\[data-documentation-id\]/);
  assert.match(docsSource, /void openPage\(id, true\)/);
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
  assert.match(panelStyle, /\.docs-page-mark-available\s*\{[^}]*background:\s*var\(--green\)/s);
});

test("web panel documentation controls keep their separate-page behavior", () => {
  const dom = new JSDOM(panelHTML);
  const button = dom.window.document.querySelector("#tab-docs");

  assert.equal(button?.getAttribute("role"), "link");
  assert.equal(button?.hasAttribute("data-tab"), false);
  assert.match(appSource, /window\.open\(url, "_blank"\)/);
  assert.match(appSource, /navigateToDocumentationLink\(event, `node\/\$\{node\.class\}`\)/);
  assert.match(appSource, /if \(currentPanelMode !== "desktop"\) \{\s*docsLink\.target = "_blank"/);
  assert.match(appSource, /docsLink\.rel = "opener"/);
  assert.match(appSource, /documentationURL\(`node\/\$\{node\.class\}`\)/);
  assert.match(appSource, /button\.addEventListener\("contextmenu"/);
  assert.match(appSource, /localizedText\("node\.docs\.context_menu", "Open documentation"\)/);
  assert.match(appSource, /openDocumentation\(`node\/\$\{cls\.class\}`\)/);
  assert.match(appSource, /short_description\.trimStart\(\)\.startsWith\("DEBUG "\)/);
  assert.match(appSource, /import \{ documentationURL \} from "\.\/site_navigation\.js"/);
  assert.match(siteNavigationSource, /if \(configured\)/);
  assert.match(siteNavigationSource, /new URL\("\.\/docs\/", window\.location\.href\)/);
  assert.doesNotMatch(siteNavigationSource, /locale/);
});

test("published assets include the documentation page and module", async () => {
  await access(published("docs/index.html.gz"));
  await access(published("public/build/docs.js.gz"));
  await access(published("logo.svg.gz"));
});
