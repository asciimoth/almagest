import assert from "node:assert/strict";
import { test } from "node:test";

import { tabHash, tabNameFromHash } from "../public/build/tab_navigation.js";

const tabs = ["graph", "sec", "dns", "tls", "kv", "ps", "logs", "config", "snapshots"];

test("SPA tab names round-trip through URL hashes", () => {
  for (const tab of tabs) assert.equal(tabNameFromHash(tabHash(tab), tabs, "graph"), tab);
});

test("an absent or unknown tab hash selects the fallback", () => {
  assert.equal(tabNameFromHash("", tabs, "graph"), "graph");
  assert.equal(tabNameFromHash("#unknown", tabs, "graph"), "graph");
});

test("the examples anchor selects the snapshots tab", () => {
  assert.equal(tabNameFromHash("#examples", tabs, "graph"), "snapshots");
});
