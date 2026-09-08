import assert from "node:assert/strict";
import { test } from "node:test";
import { JSDOM } from "jsdom";
import {
  removeSnapshot,
  renderSnapshotList,
  upsertSnapshot,
} from "../public/build/snapshots_panel.js";

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
