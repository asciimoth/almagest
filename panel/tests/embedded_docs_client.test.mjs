import assert from "node:assert/strict";
import { test } from "node:test";

import { EmbeddedDocsClient } from "../public/build/embedded_docs_client.js";

function harness() {
  const messages = [];
  let listener = () => {};
  const parent = {
    postMessage(message, origin) {
      messages.push({ message, origin });
    },
  };
  globalThis.window = {
    location: { origin: "tauri://localhost" },
    parent,
    addEventListener(name, callback) {
      if (name === "message") listener = callback;
    },
  };
  return {
    messages,
    dispatch(data, origin = "tauri://localhost", messageSource = parent) {
      listener({ origin, source: messageSource, data });
    },
    respond(request, result, error) {
      this.dispatch({
        source: "almagest-desktop",
        kind: "docs-response",
        id: request.message.id,
        result,
        ...(error ? { error } : {}),
      });
    },
  };
}

async function nextTask() {
  await new Promise((resolve) => setImmediate(resolve));
}

test("embedded documentation sends only documentation requests through its parent", async (context) => {
  const originalWindow = globalThis.window;
  context.after(() => { globalThis.window = originalWindow; });
  const bridge = harness();
  const client = new EmbeddedDocsClient();

  const result = client.tree("en");
  assert.equal(bridge.messages[0].message.surface, "admin");
  assert.equal(bridge.messages[0].message.method, "set_locale");
  bridge.respond(bridge.messages[0], {});
  await nextTask();
  assert.equal(bridge.messages[1].message.surface, "docs");
  assert.equal(bridge.messages[1].message.method, "tree");
  bridge.respond(bridge.messages[1], { locale: "en", topics: [] });
  assert.deepEqual(await result, { locale: "en", topics: [] });

  const page = client.raw("en", "guide/welcome");
  await nextTask();
  assert.equal(bridge.messages[2].message.method, "page");
  bridge.respond(bridge.messages[2], { content: "Safe Markdown" });
  assert.equal(await page, "Safe Markdown");
});

test("embedded documentation keeps its navigation inside the persistent frame", (context) => {
  const originalWindow = globalThis.window;
  context.after(() => { globalThis.window = originalWindow; });
  harness();
  const client = new EmbeddedDocsClient();

  assert.equal(
    client.pageURL("node/a b", "en", "#port"),
    "./index.html?embedded=1&page=node%2Fa+b&locale=en#port",
  );
});

test("embedded documentation ignores responses from another origin or window", async (context) => {
  const originalWindow = globalThis.window;
  context.after(() => { globalThis.window = originalWindow; });
  const bridge = harness();
  const client = new EmbeddedDocsClient();
  let settled = false;
  const result = client.tree("en").then((value) => {
    settled = true;
    return value;
  });
  const response = {
    source: "almagest-desktop",
    kind: "docs-response",
    id: bridge.messages[0].message.id,
    result: {},
  };

  bridge.dispatch(response, "https://attacker.invalid");
  bridge.dispatch(response, "tauri://localhost", {});
  await nextTask();
  assert.equal(settled, false);

  bridge.respond(bridge.messages[0], {});
  await nextTask();
  bridge.respond(bridge.messages[1], { locale: "en", topics: [] });
  assert.deepEqual(await result, { locale: "en", topics: [] });
});
