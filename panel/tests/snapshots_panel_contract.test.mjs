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
  assert.notEqual(panel?.querySelector("#snapshot-import-file"), null);
  assert.notEqual(panel?.querySelector("#snapshot-import-name"), null);
  assert.notEqual(panel?.querySelector("#snapshot-import"), null);
  assert.notEqual(panel?.querySelector("#snapshots-reload"), null);
  assert.notEqual(panel?.querySelector("#snapshots-list"), null);
  assert.match(panel?.textContent ?? "", /Protected config is never included/);
});

test("snapshots panel uses all snapshot API methods and confirms destructive actions", () => {
  assert.match(app, /requestDecoded\([^)]*"snapshots",\s*"create"/);
  assert.match(app, /requestDecoded\([^)]*"snapshots",\s*"list"/);
  assert.match(app, /requestDecoded\([^)]*"snapshots",\s*"export"/);
  assert.match(app, /requestDecoded\([^)]*"snapshots",\s*"import"/);
  assert.match(app, /requestDecoded\([^)]*"snapshots",\s*"restore"/);
  assert.match(app, /request\([^)]*"snapshots",\s*"delete"/);
  assert.match(app, /requestDecoded\([^)]*"snapshots",\s*"rename"/);
  assert.match(app, /window\.confirm\([^)]*Restore snapshot/);
  assert.match(app, /window\.confirm\([^)]*Delete snapshot/);
  assert.match(app, /window\.prompt\([^)]*Snapshot name/);
  assert.match(app, /if \(entry\.example\)\s+setTab\("config"\)/);
  assert.match(app, /"restore",\s*\{ uuid: params\.uuid, example: params\.example \}/);
  assert.doesNotMatch(app, /Unix nanoseconds/);
  assert.match(app, /link\.download\s*=\s*`\$\{entry\.uuid\}\.zip`/);
});

test("snapshot import rejects oversized files before reading them", () => {
  const sizeCheck = app.indexOf("file.size > SNAPSHOT_ARCHIVE_MAX_BYTES");
  const fileRead = app.indexOf("fileAsBase64(file)");

  assert.notEqual(sizeCheck, -1);
  assert.notEqual(fileRead, -1);
  assert.ok(sizeCheck < fileRead);
  assert.match(app, /Snapshot archive exceeds the 32 MiB limit/);
});
