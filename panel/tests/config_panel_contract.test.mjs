import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import { JSDOM } from "jsdom";

const html = await readFile(new URL("../index.html", import.meta.url), "utf8");
const app = await readFile(new URL("../public/build/app.js", import.meta.url), "utf8");

test("config tab provides a HuJSON editor without a protected config field", () => {
  const dom = new JSDOM(html);
  const panel = dom.window.document.querySelector("[data-panel='config']");

  assert.notEqual(panel?.querySelector("#config-editor"), null);
  assert.equal(panel?.querySelector("#config-highlight")?.getAttribute("aria-hidden"), "true");
  assert.notEqual(panel?.querySelector("#config-reload"), null);
  assert.notEqual(panel?.querySelector("#config-apply"), null);
  assert.match(panel?.textContent ?? "", /Protected daemon config is never available here/);
  assert.equal(panel?.querySelector("[name*='protected' i]"), null);
});

test("config editor uses revision-controlled snapshot and update methods", () => {
  assert.match(app, /requestDecoded\([^)]*"config",\s*"snapshot"/);
  assert.match(app, /requestDecoded\([^)]*"config",\s*"update"/);
  assert.match(app, /revision:\s*configSnapshot\.revision/);
});

test("config tab accepts changed server text even when an unsaved draft exists", () => {
  assert.match(app, /const preserveDraft = configSnapshot !== null && configDraft !== configSnapshot\.text/);
  assert.match(app, /await loadConfig\(preserveDraft\)/);
  assert.match(app, /const keepDraft = preserveDraft && configSnapshot !== null && next\.text === configSnapshot\.text/);
  assert.match(app, /if \(!keepDraft\)\s*\{\s*configDraft = next\.text/);
});
