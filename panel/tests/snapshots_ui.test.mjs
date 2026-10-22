import assert from "node:assert/strict";
import { test } from "node:test";
import { JSDOM } from "jsdom";
import {
  SNAPSHOT_ARCHIVE_MAX_BYTES,
  removeSnapshot,
  renderSnapshotList,
  upsertSnapshot,
} from "../public/build/snapshots_panel.js";

test("snapshot archive limit matches the daemon limit", () => {
  assert.equal(SNAPSHOT_ARCHIVE_MAX_BYTES, 32 * 1024 * 1024);
});

const first = {
  uuid: "11111111-1111-4111-8111-111111111111",
  timestamp: 1788955200000000000,
  name: "First snapshot",
};

test("snapshot rows render safe metadata and invoke all management actions", () => {
  const dom = new JSDOM("<ol id='snapshots'></ol>");
  const list = dom.window.document.querySelector("#snapshots");
  const calls = [];
  renderSnapshotList(list, [{ ...first, name: "<img src=x onerror=alert(1)>" }], false, {
    export: (entry) => calls.push(["export", entry.uuid]),
    rename: (entry) => calls.push(["rename", entry.uuid]),
    restore: (entry) => calls.push(["restore", entry.uuid]),
    delete: (entry) => calls.push(["delete", entry.uuid]),
  });

  assert.equal(list.querySelector("img"), null);
  assert.equal(list.querySelector("strong")?.textContent, "<img src=x onerror=alert(1)>");
  assert.equal(list.querySelector("code")?.textContent, first.uuid);
  assert.notEqual(list.querySelector(".snapshot-meta")?.textContent, "");
  for (const selector of [".snapshot-export", ".snapshot-rename", ".snapshot-restore", ".snapshot-delete"]) {
    list.querySelector(selector)?.click();
  }
  assert.deepEqual(calls, [
    ["export", first.uuid],
    ["rename", first.uuid],
    ["restore", first.uuid],
    ["delete", first.uuid],
  ]);
});

test("snapshot management updates create, rename, and delete state", () => {
  const second = {
    uuid: "22222222-2222-4222-8222-222222222222",
    timestamp: first.timestamp + 1_000_000,
    name: "Second snapshot",
  };
  let entries = upsertSnapshot([first], second);
  assert.deepEqual(entries.map((entry) => entry.uuid), [second.uuid, first.uuid]);

  entries = upsertSnapshot(entries, { ...first, name: "Renamed snapshot" });
  assert.deepEqual(entries.map((entry) => entry.name), ["Second snapshot", "Renamed snapshot"]);

  entries = removeSnapshot(entries, second.uuid);
  assert.deepEqual(entries, [{ ...first, name: "Renamed snapshot" }]);
});

test("snapshot list shows empty state and disables actions during mutations", () => {
  const dom = new JSDOM("<ol id='snapshots'></ol>");
  const list = dom.window.document.querySelector("#snapshots");
  const actions = { export() {}, rename() {}, restore() {}, delete() {} };

  renderSnapshotList(list, [], false, actions);
  assert.equal(list.querySelector(".snapshot-empty")?.textContent, "No snapshots are available.");

  renderSnapshotList(list, [first], true, actions);
  assert.equal(list.querySelectorAll("button").length, 4);
  assert.equal([...list.querySelectorAll("button")].every((button) => button.disabled), true);
});

test("examples render below a delimiter with safe text and only a Load action", () => {
  const dom = new JSDOM("<ol id='snapshots'></ol>");
  const list = dom.window.document.querySelector("#snapshots");
  const calls = [];
  const example = {
    example: "hostile-example",
    name: "<img src=x onerror=alert(1)>",
    comment: "<script>alert(2)</script>",
  };
  renderSnapshotList(list, [example, first], false, {
    export() { throw new Error("example export action was exposed"); },
    rename() { throw new Error("example rename action was exposed"); },
    restore: (entry) => calls.push(entry.example),
    delete() { throw new Error("example delete action was exposed"); },
  });

  assert.equal(list.querySelector("img"), null);
  assert.equal(list.querySelector("script"), null);
  assert.equal(list.querySelectorAll(".snapshot-examples-delimiter").length, 1);
  const row = list.querySelector(".snapshot-example-row");
  assert.equal(row?.querySelector("strong")?.textContent, example.name);
  assert.equal(row?.querySelector(".snapshot-meta")?.textContent, example.comment);
  assert.equal(row?.querySelector("code"), null);
  assert.equal(row?.querySelectorAll("button").length, 1);
  assert.equal(row?.querySelector("button")?.textContent, "Load");
  row?.querySelector("button")?.click();
  assert.deepEqual(calls, [example.example]);
});
