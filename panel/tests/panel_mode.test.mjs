import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import { JSDOM } from "jsdom";

import {
  applyPanelMode,
  panelMode,
  setPanelMode,
} from "../public/build/panel_mode.js";

const html = await readFile(new URL("../index.html", import.meta.url), "utf8");

test("web demo hides unsupported tabs and leaves empty runtime status hidden", () => {
  const document = new JSDOM(html).window.document;
  setPanelMode(document.documentElement, "webdemo");
  const mode = panelMode(document.documentElement);
  applyPanelMode(document, mode);

  assert.equal(document.querySelector("[data-tab='tls']")?.hidden, true);
  assert.equal(document.querySelector("[data-panel='tls']")?.hidden, true);
  assert.equal(document.querySelector("[data-tab='snapshots']")?.hidden, true);
  assert.equal(document.querySelector("[data-panel='snapshots']")?.hidden, true);
  assert.equal(document.querySelector("[data-tab='ps']")?.hidden, false);
  assert.equal(document.querySelector("#ps-runtime-status")?.hidden, true);
});

test("admin panel keeps all tabs and leaves empty runtime status hidden", () => {
  const document = new JSDOM(html).window.document;
  const mode = panelMode(document.documentElement);
  applyPanelMode(document, mode);

  assert.equal(mode, "admin");
  assert.equal([...document.querySelectorAll("[data-tab]")].every((tab) => !tab.hidden), true);
  assert.equal([...document.querySelectorAll("[data-panel]")].every((panel) => !panel.hidden), true);
  assert.equal(document.querySelector("#ps-runtime-status")?.hidden, true);
});

test("desktop panel keeps the complete UI and leaves empty runtime status hidden", () => {
  const document = new JSDOM(html).window.document;
  setPanelMode(document.documentElement, "desktop");
  const mode = panelMode(document.documentElement);
  applyPanelMode(document, mode);

  assert.equal(mode, "desktop");
  assert.equal([...document.querySelectorAll("[data-tab]")].every((tab) => !tab.hidden), true);
  assert.equal([...document.querySelectorAll("[data-panel]")].every((panel) => !panel.hidden), true);
  assert.equal(document.querySelector("#ps-runtime-status")?.hidden, true);
});
