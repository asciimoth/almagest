import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import { JSDOM } from "jsdom";

const html = await readFile(new URL("../index.html", import.meta.url), "utf8");
const litegraph = await readFile(new URL("../vendor/litegraph/litegraph.js", import.meta.url), "utf8");
const app = await readFile(new URL("../src/app.ts", import.meta.url), "utf8");
const webdemo = await readFile(new URL("../src/webdemo.ts", import.meta.url), "utf8");

test("the panel has a restrictive content security policy", () => {
  const dom = new JSDOM(html);
  const policy = dom.window.document.querySelector("meta[http-equiv='Content-Security-Policy']")?.getAttribute("content") ?? "";

  assert.match(policy, /default-src 'self'/);
  assert.match(policy, /base-uri 'none'/);
  assert.match(policy, /form-action 'none'/);
  assert.match(policy, /frame-src 'none'/);
  assert.match(policy, /object-src 'none'/);
  assert.match(policy, /script-src 'self'/);
  assert.match(policy, /worker-src 'none'/);
  assert.doesNotMatch(policy, /script-src[^;]*'unsafe-(?:inline|eval)'/);
});

test("the vendored LiteGraph build does not register eval-built nodes at startup", () => {
  assert.match(litegraph, /global\.LGraphCanvas = LiteGraph\.LGraphCanvas/);
  assert.doesNotMatch(litegraph, /LiteGraph\.wrapFunctionAsNode\s*\(/);
});

test("the graph theme is configured before LiteGraph starts rendering", () => {
  assert.match(app, /new LGraphCanvas\("#graph-canvas", graph, \{ skip_render: true \}\)/);
  assert.match(app, /canvas\.background_image = GRAPH_GRID_IMAGE;[\s\S]*canvas\.startRendering\(\);/);
  assert.match(app, /window\.addEventListener\("resize", scheduleGraphCanvasResize\)/);
  assert.match(app, /window\.visualViewport\?\.addEventListener\("resize", scheduleGraphCanvasResize\)/);
  assert.doesNotMatch(app, /new ResizeObserver/);
  assert.match(app, /if \(width === graphCanvasWidth && height === graphCanvasHeight\) return;/);
});

test("desktop graph input uses standard wheel events without legacy duplicates", () => {
  assert.match(app, /currentPanelMode === "desktop"\) installDesktopGraphWheel\(\)/);
  assert.match(app, /removeEventListener\("mousewheel", legacyHandler\)/);
  assert.match(app, /addEventListener\("wheel", \(event\) =>/);
  assert.match(app, /applyStandardWheelZoom\(canvas, el\.graphCanvas, event\)/);
});

test("graph input uses pointer events for touch dragging", () => {
  const pointerSetup = app.indexOf('if (usesPointerEvents) LiteGraph.pointerevents_method = "pointer"');
  const canvasSetup = app.indexOf('new LGraphCanvas("#graph-canvas"');

  assert.ok(pointerSetup >= 0);
  assert.ok(pointerSetup < canvasSetup);
  assert.match(app, /new GraphPinchZoom\(canvas, el\.graphCanvas\)/);
  assert.match(app, /addEventListener\("pointerdown", \(event\) => \{[\s\S]*?graphPinchZoom\.start\(event\)[\s\S]*?event\.stopImmediatePropagation\(\);[\s\S]*?\}, \{ capture: true \}\)/);
  assert.match(app, /graphPinchZoom\.move\(event\)[\s\S]*?event\.stopImmediatePropagation\(\)/);
  assert.match(app, /addEventListener\("pointercancel", \(event\) => \{/);
  assert.match(app, /pointercancel[\s\S]*?cancelLiteGraphPointerInteraction\(canvas\)/);
  assert.match(app, /addEventListener\("dblclick"[\s\S]*?lastGraphPointerType !== "mouse"[\s\S]*?openGraphSearchOnBackgroundDoubleClick\(canvas, event\)/);
  assert.match(app, /canvas\.processContextMenu[\s\S]*?lastGraphPointerType === "touch"/);
});

test("returning to the graph tab waits for layout and forces a complete frame", () => {
  const setTab = app.slice(app.indexOf("function setTab"), app.indexOf("function moveTabFocus"));
  assert.match(setTab, /requestAnimationFrame\(\(\) => \{\s*window\.requestAnimationFrame/);
  assert.match(setTab, /resizeGraphCanvas\(\);\s*centerPendingGraphContent\(\);\s*redrawRevealedGraph\(canvas\)/);
});

test("description port links highlight selected node slots without HTML parsing", () => {
  assert.match(app, /drawHighlightedDescriptionPort\(ctx, this\)/);
  assert.match(app, /\(slot\.label \?\? slot\.name\) !== highlighted\.name/);
  assert.match(app, /setDescriptionPortHighlight\(id, side, name, highlighted\)/);
  assert.match(app, /if \(name !== "docs"\) clearDescriptionPortHighlight\(\)/);
});

test("empty node menus open the description tab by default", () => {
  assert.match(app, /firstMenuSnapshot = message\.type === "menu\.snapshot" && !selectedMenu\.receivedSnapshot/);
  assert.match(app, /accepted && firstMenuSnapshot/);
  assert.match(app, /Array\.isArray\(message\.blocks\) && message\.blocks\.length === 0/);
  assert.match(app, /tryBackend\("subscribeNodeMenu", \{ id: nodeID \}\)\.then/);
  assert.match(app, /selectedMenu\?\.node === nodeID && !selectedMenu\.receivedSnapshot/);
  assert.match(app, /setNodePanelTab\("docs"\)/);
});

test("the full documentation link is in the description header", () => {
  assert.match(app, /descriptionHeader\.append\(docsLink\)/);
  assert.match(app, /docsPanel\.append\(descriptionHeader, descriptionPanel\)/);
  assert.match(app, /localizedText\(\s*"node\.docs\.open",\s*currentPanelMode === "desktop" \? "Open documentation" : "Open in a new tab"/);
});

test("web demo node descriptions use the published static documentation", () => {
  assert.match(webdemo, /new StaticDocsClient\(new URL\(configuredSiteBase \|\| "\.\.\/", window\.location\.href\)\)/);
  assert.match(app, /currentPanelMode !== "webdemo"[\s\S]*tryBackend\("classDescription"/);
  assert.match(app, /docsClient\(\)\.raw\(document\.documentElement\.lang \|\| "en", `node\/\$\{className\}`\)/);
});

test("graph input recovers from lost mouse-up events", () => {
  assert.match(app, /installLiteGraphPointerGuard\(\)/);
  assert.match(app, /finally \{\s*normalizeLiteGraphMoveListener\(this\);\s*\}/);
  assert.match(app, /pointerListenerRemove\(graphDocument, "move", move, true\)/);
  assert.match(app, /pointerListenerRemove\(graphCanvas, "move", move, true\)/);
  assert.match(app, /pointerListenerAdd\(graphCanvas, "move", move, false\)/);
  assert.match(app, /if \(!canvas\?\.pointer_is_down \|\| event\.buttons !== 0\) return;/);
  assert.match(app, /cancelLiteGraphPointerInteraction\(canvas\)/);
});

test("SPA tabs use meaningful icons with localized accessible names", () => {
  const dom = new JSDOM(html);
  const tabElements = [...dom.window.document.querySelectorAll("[data-tab]")];
  const iconNames = tabElements.map((button) => button.querySelector(".tab-icon")?.getAttribute("data-icon"));
  const labels = tabElements.map((button) => button.getAttribute("aria-label"));

  assert.deepEqual(iconNames, ["workflow", "key-round", "network", "shield-check", "database", "cpu", "logs", "settings-2", "archive"]);
  assert.deepEqual(labels.slice(0, 5), ["Graph", "Secrets", "DNS", "TLS", "KV"]);
  assert.equal(labels.includes("Processes"), true);
  assert.equal(tabElements.every((tab) => tab.textContent?.trim() === ""), true);
  assert.equal(tabElements.every((tab) => tab.querySelector(".tab-icon")?.getAttribute("aria-hidden") === "true"), true);
  const docsIcon = dom.window.document.querySelector("#tab-docs .tab-icon");
  assert.equal(docsIcon?.getAttribute("data-icon"), "book-open");
  assert.equal(docsIcon?.getAttribute("aria-hidden"), "true");
  assert.equal(tabElements.every((tab) => tab.getAttribute("title") === tab.getAttribute("aria-label")), true);
  assert.equal(tabElements.every((tab) => tab.hasAttribute("data-i18n-title")), true);
  assert.equal(tabElements.every((tab) => tab.hasAttribute("data-i18n-aria-label")), true);
  assert.equal(tabElements.every((tab) => !tab.hasAttribute("data-i18n")), true);
  assert.equal(tabElements.every((tab) => tab.getAttribute("role") === "tab"), true);
  assert.equal(tabElements.every((tab) => tab.hasAttribute("aria-controls")), true);
  assert.equal(dom.window.document.querySelectorAll("[role='tabpanel']").length, tabElements.length);
  assert.equal(dom.window.document.querySelector("[data-panel='sec'] #sec-tree") !== null, true);
  assert.equal(dom.window.document.querySelector("[data-panel='sec'] #sec-value-panel") !== null, true);
  assert.equal(dom.window.document.querySelector("[data-panel='tls'] #tls-tree") !== null, true);
  assert.equal(dom.window.document.querySelector("[data-panel='tls'] #tls-value-panel") !== null, true);
  assert.equal(dom.window.document.querySelector("[data-panel='logs'] .logs-scroll > #logs-list") !== null, true);
});

test("SPA tabs restore and update the URL hash", () => {
  assert.match(app, /setTab\(tabNameFromLocation\(\), false\)/);
  assert.match(app, /window\.location\.hash = tabHash\(name\)/);
  assert.match(app, /addEventListener\("hashchange"/);
  assert.match(app, /document\.getElementById\("examples"\)\?\.scrollIntoView\(\)/);
});

test("language picker is the last glyph in the tab bar with a localized hint", () => {
  const dom = new JSDOM(html);
  const tabs = dom.window.document.querySelector(".tabs");
  const picker = dom.window.document.querySelector(".locale-picker");
  const icon = picker?.querySelector(".locale-picker-icon");
  const select = picker?.querySelector("#locale-select");

  assert.equal(tabs?.lastElementChild, picker);
  assert.equal(icon?.getAttribute("aria-hidden"), "true");
  assert.equal(picker?.querySelector("#locale-code"), null);
  assert.equal(picker?.getAttribute("title"), "Language");
  assert.equal(picker?.getAttribute("data-i18n-title"), "locale.label");
  assert.equal(select?.getAttribute("aria-label"), "Language");
  assert.equal(select?.querySelector("option")?.textContent, "English");
});

test("runtime security status is reported in the Processes tab", () => {
  const dom = new JSDOM(html);
  const header = dom.window.document.querySelector(".app-header");
  const processes = dom.window.document.querySelector("[data-panel='ps']");

  assert.equal(header?.querySelector("#isolation-status"), null);
  assert.equal(header?.querySelector("#sandbox-status"), null);
  assert.ok(processes?.querySelector(".ps-runtime-status > #isolation-status"));
  assert.ok(processes?.querySelector(".ps-runtime-status > #sandbox-status"));
});
