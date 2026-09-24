import assert from "node:assert/strict";
import { test } from "node:test";
import { errorMessage, parseAPIMessage } from "../public/build/api_model.js";

test("API message parser accepts valid protocol envelopes", () => {
  assert.deepEqual(parseAPIMessage(JSON.stringify({
    version: "v1",
    kind: "event",
    surface: "engine",
    method: "notification",
    payload: { untrusted: "<script>alert(1)</script>" },
  })), {
    version: "v1",
    kind: "event",
    surface: "engine",
    method: "notification",
    payload: { untrusted: "<script>alert(1)</script>" },
  });

  assert.deepEqual(parseAPIMessage(JSON.stringify({
    version: "v1",
    kind: "response",
    id: "null-result",
    result: null,
  })), {
    version: "v1",
    kind: "response",
    id: "null-result",
    result: null,
  });

  assert.deepEqual(parseAPIMessage(JSON.stringify({
    version: "v1",
    kind: "event",
    surface: "system",
    method: "status",
  })), {
    version: "v1",
    kind: "event",
    surface: "system",
    method: "status",
  });
});

test("API message parser rejects malformed and unknown envelopes", () => {
  const invalid = [
    "not JSON",
    "null",
    "[]",
    JSON.stringify({ version: "v2", kind: "event" }),
    JSON.stringify({ version: "v1", kind: "execute" }),
    JSON.stringify({ version: "v1", kind: "event", surface: "unknown" }),
    JSON.stringify({ version: "v1", kind: "event", surface: "engine" }),
    JSON.stringify({ version: "v1", kind: "event", surface: "engine", method: "" }),
    JSON.stringify({ version: "v1", kind: "response" }),
    JSON.stringify({ version: "v1", kind: "response", id: "1" }),
    JSON.stringify({
      version: "v1",
      kind: "response",
      id: "1",
      result: {},
      error: { code: "failed", message: "bad" },
    }),
    JSON.stringify({ version: "v1", kind: "response", id: 1 }),
    JSON.stringify({ version: "v1", kind: "request", id: "", surface: "engine", method: "snapshot" }),
    JSON.stringify({ version: "v1", kind: "request", id: "1", surface: "engine", method: "" }),
    JSON.stringify({ version: "v1", kind: "response", id: "1", error: { code: 500, message: "bad" } }),
  ];

  for (const message of invalid) assert.equal(parseAPIMessage(message), null, message);
});

test("unknown thrown values produce safe error text", () => {
  assert.equal(errorMessage(new Error("failed")), "failed");
  assert.equal(errorMessage("failed"), "failed");
  assert.equal(errorMessage({ message: "untrusted shape" }), "Unknown error");
  assert.equal(errorMessage(null), "Unknown error");
});
