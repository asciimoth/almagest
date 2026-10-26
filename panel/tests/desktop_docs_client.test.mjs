import assert from "node:assert/strict";
import { test } from "node:test";

import { DesktopDocsClient } from "../public/build/desktop_docs_client.js";

class FakeTransport {
  connected = false;
  reconnect = true;
  handlers = null;
  requests = [];

  async connect(handlers) {
    this.handlers = handlers;
    this.connected = true;
  }

  send(data) {
    const request = JSON.parse(data);
    this.requests.push(request);
    let result = {};
    if (request.surface === "docs" && request.method === "tree") result = { locale: "en", topics: [] };
    if (request.surface === "docs" && request.method === "page") result = { content: "Safe **Markdown**" };
    if (request.surface === "docs" && request.method === "node_class") result = { snapshot: {}, colormap: {} };
    queueMicrotask(() => this.handlers?.message(JSON.stringify({
      version: "v1", kind: "response", id: request.id, result,
    })));
  }

  close() {
    this.connected = false;
    this.handlers?.close();
  }
}

test("desktop documentation uses the native admin session and reuses its locale", async () => {
  const transport = new FakeTransport();
  const client = new DesktopDocsClient(transport);

  assert.deepEqual(await client.tree("en"), { locale: "en", topics: [] });
  assert.equal(await client.raw("en", "guide/welcome"), "Safe **Markdown**");
  assert.deepEqual(await client.nodeClass("en", "node/almagest/Tun"), { snapshot: {}, colormap: {} });

  assert.deepEqual(transport.requests.map(({ surface, method }) => [surface, method]), [
    ["admin", "set_locale"],
    ["docs", "tree"],
    ["docs", "page"],
    ["docs", "node_class"],
  ]);
  assert.equal(transport.requests[2].payload.id, "guide/welcome");
});

test("desktop documentation rejects API errors and invalid page results", async () => {
  const transport = new FakeTransport();
  transport.send = function send(data) {
    const request = JSON.parse(data);
    queueMicrotask(() => this.handlers?.message(JSON.stringify(
      request.method === "set_locale"
        ? { version: "v1", kind: "response", id: request.id, result: {} }
        : { version: "v1", kind: "response", id: request.id, result: { content: 7 } },
    )));
  };
  const client = new DesktopDocsClient(transport);
  await assert.rejects(client.raw("en", "guide/welcome"), /invalid result/);

  transport.send = function send(data) {
    const request = JSON.parse(data);
    queueMicrotask(() => this.handlers?.message(JSON.stringify({
      version: "v1", kind: "response", id: request.id,
      error: { code: "not_found", message: "missing" },
    })));
  };
  await assert.rejects(client.tree("en"), /not_found: missing/);
});

test("desktop documentation URLs stay on the packaged documentation page", () => {
  const client = new DesktopDocsClient(new FakeTransport());
  assert.equal(client.pageURL("node/a b", "en", "#port"), "./index.html?page=node%2Fa+b&locale=en#port");
});

test("desktop documentation releases its page listener before cache suspension", async () => {
  const transport = new FakeTransport();
  const client = new DesktopDocsClient(transport);
  await client.tree("en");

  client.suspend();

  assert.equal(transport.connected, false);
  await client.tree("en");
  assert.equal(transport.connected, true);
});
