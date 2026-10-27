import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import { JSDOM } from "jsdom";
import { setupMobileNavigation } from "../public/build/mobile_navigation.js";

const html = await readFile(new URL("../index.html", import.meta.url), "utf8");

test("phone section chooser supports dismissal, focus, locale changes and resize", async () => {
  const dom = new JSDOM(html, { url: "http://localhost/" });
  const { document, MutationObserver, KeyboardEvent, Event } = dom.window;
  globalThis.MutationObserver = MutationObserver;
  const compact = new dom.window.EventTarget();
  compact.matches = true;
  setupMobileNavigation(document, compact);
  const toggle = document.querySelector("#mobile-navigation-toggle");
  const nav = document.querySelector("#panel-navigation");
  const graph = document.querySelector("#tab-graph");
  const panel = document.querySelector("#panel-graph");
  const key = (value, shiftKey = false) => document.dispatchEvent(new KeyboardEvent("keydown", { key: value, shiftKey, cancelable: true }));

  toggle.click();
  assert.equal(toggle.getAttribute("aria-expanded"), "true");
  assert.equal(document.activeElement, graph);
  assert.equal(panel.inert, true);
  key("Tab", true);
  assert.equal(document.activeElement, toggle);
  key("Tab");
  assert.equal(document.activeElement, graph);
  key("Escape");
  assert.equal(document.activeElement, toggle);
  assert.equal(panel.inert, false);
  assert.equal(nav.classList.contains("mobile-open"), false);

  toggle.click();
  document.querySelector("#tab-logs svg").dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true }));
  assert.equal(toggle.getAttribute("aria-expanded"), "false");
  assert.equal(document.activeElement, toggle);
  graph.setAttribute("aria-selected", "false");
  const logs = document.querySelector("#tab-logs");
  logs.setAttribute("aria-selected", "true");
  logs.setAttribute("aria-label", "Journal");
  await Promise.resolve();
  assert.equal(document.querySelector("#mobile-current-section").textContent, "Journal");

  toggle.click();
  document.querySelector("#mobile-navigation-scrim").click();
  assert.equal(toggle.getAttribute("aria-expanded"), "false");
  toggle.click();
  dom.window.dispatchEvent(new Event("hashchange"));
  assert.equal(toggle.getAttribute("aria-expanded"), "false");
  toggle.click();
  compact.matches = false;
  compact.dispatchEvent(new Event("change"));
  assert.equal(panel.inert, false);
  assert.equal(toggle.getAttribute("aria-expanded"), "false");
  assert.equal(document.querySelector("#mobile-navigation-scrim").hidden, true);
  dom.window.close();
});
