import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import { JSDOM } from "jsdom";

const html = await readFile(new URL("../index.html", import.meta.url), "utf8");
const app = await readFile(new URL("../public/build/app.js", import.meta.url), "utf8");

test("snapshots tab provides snapshot management controls", () => {
  const dom = new JSDOM(html);
  const panel = dom.window.document.querySelector("[data-panel='snapshots']");

  assert.notEqual(dom.window.document.querySelector("[data-tab='snapshots']"), null);
  assert.notEqual(panel?.querySelector("#snapshot-name"), null);
  assert.notEqual(panel?.querySelector("#snapshot-create"), null);
  assert.notEqual(panel?.querySelector("#snapshots-reload"), null);
  assert.notEqual(panel?.querySelector("#snapshots-list"), null);
  assert.match(panel?.textContent ?? "", /Protected config is never included/);
});

test("snapshots panel uses all snapshot API methods and confirms destructive actions", () => {
  assert.match(app, /request\([^)]*"snapshots",\s*"create"/);
  assert.match(app, /request\([^)]*"snapshots",\s*"list"/);
  assert.match(app, /request\([^)]*"snapshots",\s*"restore"/);
  assert.match(app, /request\([^)]*"snapshots",\s*"delete"/);
  assert.match(app, /request\([^)]*"snapshots",\s*"rename"/);
  assert.match(app, /window\.confirm\([^)]*Restore snapshot/);
  assert.match(app, /window\.confirm\([^)]*Delete snapshot/);
  assert.match(app, /window\.prompt\([^)]*Snapshot name/);
  assert.doesNotMatch(app, /Unix nanoseconds/);
});
