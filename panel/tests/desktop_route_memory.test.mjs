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

test("desktop surface changes use one immediate navigation", () => {
  assert.match(desktopEntry, /rememberedDesktopSurface\(window\.localStorage\) === "docs"/);
  assert.match(desktopEntry, /rememberedDesktopPanelTab\(window\.localStorage\)/);
  assert.match(desktopEntry, /window\.location\.hash !== savedHash/);
  assert.doesNotMatch(desktopDocsEntry, /performance\.getEntriesByType|sessionStorage/);
  assert.match(appSource, /currentPanelMode === "desktop"\) rememberDesktopPanelTab/);
  assert.match(appSource, /rememberDesktopSurface\(window\.localStorage, "docs"\);[\s\S]*window\.location\.replace\(url\)/);
  assert.match(appSource, /if \(currentPanelMode !== "desktop"\) return;[\s\S]*apiTransport\?\.close\(\)/);
  assert.match(docsSource, /rememberDesktopSurface\(window\.localStorage, "panel"\);\s*window\.location\.replace\(adminPanelLink\.href\)/);
  assert.match(docsSource, /if \(desktopMode\) \{[\s\S]*window\.history\.replaceState\(\{ page: id \}/);
  assert.match(docsSource, /window\.addEventListener\("pagehide", saveCurrentDesktopDocsScroll\)/);
  assert.match(docsSource, /if \(window\.location\.hash\) scrollToCurrentAnchor\(\);\s*else restoreDesktopDocsScroll\(id\)/);
  assert.doesNotMatch(appSource + docsSource + routeMemorySource, /restoreDesktopHistory|docs-depth|docs-forward|docs-target/);
});
