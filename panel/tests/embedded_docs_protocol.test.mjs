import assert from "node:assert/strict";
import { test } from "node:test";

import { parseEmbeddedDocsMessage } from "../public/build/embedded_docs_protocol.js";

const source = "almagest-desktop";

test("embedded docs protocol accepts only its four read and locale operations", () => {
  for (const [surface, method, payload] of [
    ["admin", "set_locale", { locale: "en" }],
    ["docs", "tree", {}],
    ["docs", "page", { id: "guide/welcome" }],
    ["docs", "node_class", { id: "node/almagest/Tun" }],
  ]) {
    assert.deepEqual(parseEmbeddedDocsMessage({
      source,
      kind: "docs-request",
      id: "embedded-docs-1",
      surface,
      method,
      payload,
    }), {
      kind: "docs-request",
      id: "embedded-docs-1",
      surface,
      method,
      payload,
    });
  }
});

test("embedded docs protocol rejects malformed and privileged requests", () => {
  const request = (surface, method, payload, id = "embedded-docs-1") => ({
    source, kind: "docs-request", id, surface, method, payload,
  });
  for (const value of [
    null,
    [],
    { source: "other", kind: "docs-request" },
    request("engine", "snapshot", {}),
    request("admin", "hello", {}),
    request("admin", "set_locale", { locale: "bad\nlocale" }),
    request("admin", "set_locale", { locale: "en", extra: true }),
    request("docs", "page", { id: "bad\npage" }),
    request("docs", "page", { id: "" }),
    request("docs", "page", { id: "guide/welcome", extra: true }),
    request("docs", "tree", { extra: true }),
    request("docs", "tree", [], "id"),
    request("docs", "tree", {}, "x".repeat(129)),
    { source, kind: "docs-close", tab: "bad\ntab" },
  ]) {
    assert.equal(parseEmbeddedDocsMessage(value), null);
  }
});

test("embedded docs protocol validates panel close routes", () => {
  assert.deepEqual(parseEmbeddedDocsMessage({ source, kind: "docs-close", tab: "" }), {
    kind: "docs-close", tab: "",
  });
  assert.deepEqual(parseEmbeddedDocsMessage({ source, kind: "docs-close", tab: "config" }), {
    kind: "docs-close", tab: "config",
  });
});

test("embedded docs protocol validates the readiness signal", () => {
  assert.deepEqual(parseEmbeddedDocsMessage({ source, kind: "docs-ready" }), {
    kind: "docs-ready",
  });
  assert.equal(parseEmbeddedDocsMessage({ source, kind: "docs-ready", extra: true }), null);
});
