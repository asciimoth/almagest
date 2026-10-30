import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";

import {
  rememberDesktopDocsPage,
  rememberDesktopDocsScroll,
  rememberDesktopPanelTab,
  rememberDesktopSurface,
  rememberedDesktopDocsPage,
  rememberedDesktopDocsScroll,
  rememberedDesktopPanelTab,
  rememberedDesktopSurface,
} from "../public/build/desktop_route_memory.js";

const desktopEntry = await readFile(new URL("../src/desktop.ts", import.meta.url), "utf8");
const desktopDocsEntry = await readFile(new URL("../src/desktop_docs.ts", import.meta.url), "utf8");
const routeMemorySource = await readFile(new URL("../src/desktop_route_memory.ts", import.meta.url), "utf8");
const appSource = await readFile(new URL("../src/app.ts", import.meta.url), "utf8");
const docsSource = await readFile(new URL("../src/docs.ts", import.meta.url), "utf8");

class MemoryStorage {
  #values = new Map();

  getItem(key) {
    return this.#values.get(key) ?? null;
  }

  setItem(key, value) {
    this.#values.set(key, String(value));
  }

}

test("desktop route memory persists only valid route identifiers", () => {
  const storage = new MemoryStorage();
  assert.equal(rememberedDesktopSurface(storage), "panel");

  rememberDesktopSurface(storage, "docs");
  rememberDesktopPanelTab(storage, "config");
  rememberDesktopDocsPage(storage, "node/almagest/Tun");
  assert.equal(rememberedDesktopSurface(storage), "docs");
  assert.equal(rememberedDesktopPanelTab(storage), "config");
  assert.equal(rememberedDesktopDocsPage(storage), "node/almagest/Tun");

  const invalid = new MemoryStorage();
  rememberDesktopPanelTab(invalid, "bad\nroute");
  rememberDesktopDocsPage(invalid, "x".repeat(1025));
  assert.equal(rememberedDesktopPanelTab(invalid), "");
  assert.equal(rememberedDesktopDocsPage(invalid), "");
});

test("desktop docs scroll belongs only to its saved page", () => {
  const storage = new MemoryStorage();
  rememberDesktopDocsScroll(storage, "guide/welcome", 417.6);

  assert.equal(rememberedDesktopDocsScroll(storage, "guide/welcome"), 418);
  assert.equal(rememberedDesktopDocsScroll(storage, "guide/other"), null);
  rememberDesktopDocsScroll(storage, "guide/welcome", Number.NaN);
  assert.equal(rememberedDesktopDocsScroll(storage, "guide/welcome"), 418);
});

test("desktop surface changes preserve routes in persistent panel and docs documents", () => {
  assert.doesNotMatch(desktopEntry, /window\.location\.replace\(`\.\/docs/);
  assert.match(desktopEntry, /rememberedDesktopPanelTab\(window\.localStorage\)/);
  assert.match(desktopEntry, /window\.location\.hash !== savedHash/);
  assert.match(desktopDocsEntry, /new EmbeddedDocsClient\(\)/);
  assert.match(appSource, /currentPanelMode === "desktop"\) rememberDesktopPanelTab/);
  assert.match(appSource, /rememberDesktopSurface\(window\.localStorage, "docs"\);\s*showDesktopDocumentation/);
  assert.match(appSource, /function navigateToDocumentationLink[\s\S]*event\.preventDefault\(\);\s*openDocumentation\(page\)/);
  assert.match(appSource, /renderNodeDescription\([\s\S]*navigateToDocumentationLink/);
  assert.match(appSource, /documentation iframe load deferred until the admin API is connected/);
  assert.match(appSource, /currentPanelMode === "desktop" && desktopDocsLoadRequested/);
  const boot = appSource.slice(appSource.indexOf("function boot"), appSource.indexOf("boot();"));
  assert.doesNotMatch(boot, /else if \(currentPanelMode === "desktop"\)[\s\S]*loadDesktopDocumentation\(\)/);
  assert.match(appSource, /function installDesktopDocsBridge\(\)/);
  assert.match(appSource, /createDesktopDocsFrame\(docsURL\)/);
  assert.match(appSource, /frame\.src = url\.href;\s*desktopDocsFrame = frame;\s*document\.body\.append\(frame\)/);
  assert.doesNotMatch(appSource, /querySelector<HTMLIFrameElement>\("#desktop-docs-frame"\)/);
  assert.match(appSource, /event\.source !== desktopDocsFrame\?\.contentWindow/);
  assert.match(appSource, /kind: "docs-reconnect"/);
  assert.match(appSource, /currentPanelMode === "desktop"\) reconnectDesktopDocumentation\(\)/);
  assert.match(appSource, /await request\(message\.surface, message\.method, message\.payload\)/);
  assert.match(appSource, /setLocaleFromEmbeddedDocs\(String\(message\.payload\.locale\)\)/);
  assert.match(docsSource, /kind: "docs-close"/);
  assert.match(docsSource, /kind !== "docs-navigate"/);
  assert.match(docsSource, /message\.kind === "docs-reconnect"[\s\S]*loadTree\(effectiveLocale \|\| requestedLocale\)/);
  assert.match(docsSource, /if \(desktopMode\) \{[\s\S]*window\.history\.replaceState\(\{ page: id \}/);
  assert.match(docsSource, /window\.addEventListener\("pagehide", saveCurrentDesktopDocsScroll\)/);
  assert.match(docsSource, /if \(window\.location\.hash\) scrollToCurrentAnchor\(\);\s*else restoreDesktopDocsScroll\(id\)/);
  assert.doesNotMatch(appSource + docsSource + routeMemorySource, /restoreDesktopHistory|docs-depth|docs-target/);
});
