import assert from "node:assert/strict";
import { test } from "node:test";
import {
  applySecretChange,
  normalizeSecretNodes,
  parseSecretNodeKey,
  parseSecretNodeText,
  replaceSecretNodeTextToken,
  secretNodeKey,
  secretNodeTextSuggestions,
  secretNodesText,
  sortSecrets,
} from "../public/build/secrets_model.js";

test("secret node keys round-trip untrusted names without delimiter parsing", () => {
  const key = `prod/db|token,${JSON.stringify(["nested"])}`;
  const nodeKey = secretNodeKey(key);

  assert.deepEqual(parseSecretNodeKey(nodeKey), { key });
});

test("secret changes upsert, delete, and keep deterministic order", () => {
  const initial = [
    { key: "z", value: "last", nodes: ["node-b"] },
    { key: "a", value: "first", nodes: ["node-a"] },
  ];

  assert.deepEqual(sortSecrets(initial).map((entry) => entry.key), ["a", "z"]);

  const upserted = applySecretChange(initial, {
    key: "m",
    entry: { key: "m", value: "middle", nodes: ["node-c", "node-a"] },
  });
  assert.deepEqual(upserted.map((entry) => entry.key), ["a", "m", "z"]);
  assert.deepEqual(upserted.find((entry) => entry.key === "m")?.nodes, ["node-a", "node-c"]);

  assert.deepEqual(applySecretChange(upserted, { key: "a", deleted: true }).map((entry) => entry.key), ["m", "z"]);
});

test("secret node text treats each line as one node name", () => {
  const nodes = parseSecretNodeText("db, primary\nweb proxy\napi\tnode\nweb proxy");

  assert.deepEqual(nodes, ["api\tnode", "db, primary", "web proxy"]);
  assert.deepEqual(normalizeSecretNodes([" node-b ", "", "node-a", "node-a"]), ["node-a", "node-b"]);
  assert.equal(secretNodesText(["node-b", "node-a"]), "node-a\nnode-b");
});

test("secret node text suggestions complete the active allow-list token", () => {
  const text = "db, primary\nweb p";
  const result = secretNodeTextSuggestions(text, text.length, ["db, primary", "web primary", "web proxy", "other"]);

  assert.deepEqual(result.token, {
    start: 12,
    end: 17,
    prefix: "web p",
    token: "web p",
  });
  assert.deepEqual(result.suggestions, ["web primary", "web proxy"]);
  assert.deepEqual(replaceSecretNodeTextToken(text, result.token, "web proxy"), {
    text: "db, primary\nweb proxy",
    cursor: 21,
  });
});
