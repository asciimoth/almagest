import assert from "node:assert/strict";
import { test } from "node:test";
import {
  childKVNodeKey,
  desiredKVEntrySubscriptionKeys,
  kvNodeDescendsFrom,
  kvNodeKey,
  parseKVNodeKey,
} from "../public/build/kv_model.js";

test("KV node keys round-trip untrusted names without delimiter parsing", () => {
  const store = { kind: "plugin", name: `plug/in|"x"` };
  const path = [`a/b`, `quote"segment`, `<script>`];
  const key = kvNodeKey(store, path);
  const parsed = parseKVNodeKey(key);

  assert.deepEqual(parsed, { store, path });
});

test("KV desired entry subscriptions include only visible expanded nodes", () => {
  const store = { kind: "default" };
  const root = kvNodeKey(store);
  const child = childKVNodeKey(root, "cache");
  assert.ok(child);
  const grandchild = childKVNodeKey(child, "nested");
  assert.ok(grandchild);

  const entries = new Map([
    [root, { exists: true, entries: [{ kind: "bucket", name: "cache" }] }],
    [child, { exists: true, entries: [{ kind: "bucket", name: "nested" }] }],
    [grandchild, { exists: true, entries: [] }],
  ]);

  const expanded = new Set([root, child, grandchild]);
  assert.deepEqual([...desiredKVEntrySubscriptionKeys([store], expanded, entries)], [root, child, grandchild]);

  expanded.delete(child);
  assert.deepEqual([...desiredKVEntrySubscriptionKeys([store], expanded, entries)], [root]);

  expanded.add(child);
  expanded.delete(root);
  assert.deepEqual([...desiredKVEntrySubscriptionKeys([store], expanded, entries)], []);
});

test("KV descendant checks are scoped to the same store", () => {
  const defaultRoot = kvNodeKey({ kind: "default" });
  const defaultChild = kvNodeKey({ kind: "default" }, ["cache", "leaf"]);
  const pluginChild = kvNodeKey({ kind: "plugin", name: "cache" }, ["cache", "leaf"]);

  assert.equal(kvNodeDescendsFrom(defaultChild, defaultRoot), true);
  assert.equal(kvNodeDescendsFrom(pluginChild, defaultRoot), false);
});
