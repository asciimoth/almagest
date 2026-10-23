import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import { JSDOM } from "jsdom";

const app = await readFile(new URL("../src/app.ts", import.meta.url), "utf8");
const transport = await readFile(new URL("../src/admin_transport.ts", import.meta.url), "utf8");
const webdemo = await readFile(new URL("../src/webdemo.ts", import.meta.url), "utf8");
const wasmTransport = await readFile(new URL("../src/wasm_transport.ts", import.meta.url), "utf8");

const { WebSocketAdminTransport, configureAdminTransport, createAdminTransport } = await import(
  "../public/build/admin_transport.js"
);
const { WASMAdminTransport } = await import("../public/build/wasm_transport.js");

class MockWebSocket extends EventTarget {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;
  static instances = [];

  constructor(url) {
    super();
    this.url = String(url);
    this.readyState = MockWebSocket.CONNECTING;
    this.sent = [];
    MockWebSocket.instances.push(this);
  }

  open() {
    this.readyState = MockWebSocket.OPEN;
    this.dispatchEvent(new Event("open"));
  }

  message(data) {
    this.dispatchEvent(new MessageEvent("message", { data }));
  }

  send(data) {
    this.sent.push(data);
  }

  close() {
    this.readyState = MockWebSocket.CLOSED;
    this.dispatchEvent(new Event("close"));
  }

  fail() {
    this.dispatchEvent(new Event("error"));
  }
}

function installBrowser(url, base = "") {
  const meta = base ? `<meta name="almagest-site-base" content="${base}">` : "";
  const dom = new JSDOM(`<!doctype html>${meta}`, { url });
  globalThis.window = dom.window;
  globalThis.document = dom.window.document;
  globalThis.WebSocket = MockWebSocket;
  MockWebSocket.instances.length = 0;
}

test("the panel uses a configurable admin API transport", () => {
  assert.match(app, /createAdminTransport\(\)/);
  assert.doesNotMatch(app, /new WebSocket/);
  assert.match(transport, /export interface AdminTransport/);
  assert.match(transport, /class WebSocketAdminTransport implements AdminTransport/);
  assert.match(transport, /new URL\("\.\/api\/ws", siteBaseURL\(\)\)/);
  assert.match(webdemo, /configureAdminTransport/);
  assert.match(webdemo, /WASMAdminTransport/);
  assert.match(wasmTransport, /class WASMAdminTransport implements AdminTransport/);
  assert.match(wasmTransport, /loadWebDemoAPI/);
  assert.match(webdemo, /await import\("\.\/app\.js"\)/);
});

test("WebSocket transport connects to the daemon API and forwards messages", async () => {
  installBrowser("https://panel.test/admin/", "https://panel.test/");
  const messages = [];
  let closes = 0;
  const transport = new WebSocketAdminTransport();
  const connected = transport.connect({
    message: (data) => messages.push(data),
    close: () => closes += 1,
  });
  const socket = MockWebSocket.instances[0];

  assert.equal(socket?.url, "wss://panel.test/api/ws");
  assert.equal(transport.connected, false);
  socket.open();
  await connected;
  assert.equal(transport.connected, true);

  socket.message('{"kind":"event"}');
  transport.send("request");
  assert.deepEqual(messages, ['{"kind":"event"}']);
  assert.deepEqual(socket.sent, ["request"]);

  transport.close();
  assert.equal(transport.connected, false);
  assert.equal(closes, 1);
});

test("WebSocket transport supports HTTP and a configured subpath", () => {
  installBrowser("http://example.test/almagest/demo/", "../");
  const transport = new WebSocketAdminTransport();
  void transport.connect({ message() {}, close() {} });

  assert.equal(MockWebSocket.instances[0]?.url, "ws://example.test/almagest/api/ws");
});

test("Wasm transport imports the in-process API and forwards messages", async () => {
  const requests = [];
  const messages = [];
  let closes = 0;
  const transport = new WASMAdminTransport(async () => (request) => {
    requests.push(request);
    return `response:${request}`;
  });

  assert.equal(transport.connected, false);
  assert.equal(transport.reconnect, false);
  await transport.connect({ message: (message) => messages.push(message), close: () => closes += 1 });
  assert.equal(transport.connected, true);
  transport.send("request");
  assert.deepEqual(requests, ["request"]);
  assert.deepEqual(messages, ["response:request"]);

  transport.close();
  assert.equal(transport.connected, false);
  assert.equal(closes, 1);
  assert.throws(() => transport.send("request"), /Admin API is not connected/);
});

test("WebSocket transport rejects connection errors and closed connections", async () => {
  installBrowser("http://panel.test/");
  const onError = new WebSocketAdminTransport();
  const errorConnection = onError.connect({ message() {}, close() {} });
  MockWebSocket.instances[0]?.fail();
  await assert.rejects(errorConnection, /WebSocket connection failed/);

  let closes = 0;
  const onClose = new WebSocketAdminTransport();
  const closeConnection = onClose.connect({ message() {}, close: () => closes += 1 });
  MockWebSocket.instances[1]?.close();
  await assert.rejects(closeConnection, /WebSocket connection closed/);
  assert.equal(closes, 1);
  assert.throws(() => onClose.send("request"), /Admin API is not connected/);
});

test("WebSocket transport ignores close events from a replaced socket", async () => {
  installBrowser("http://panel.test/");
  let closes = 0;
  const transport = new WebSocketAdminTransport();
  const firstConnection = transport.connect({ message() {}, close: () => closes += 1 });
  const first = MockWebSocket.instances[0];
  const secondConnection = transport.connect({ message() {}, close: () => closes += 1 });
  const second = MockWebSocket.instances[1];

  first?.close();
  await assert.rejects(firstConnection, /WebSocket connection closed/);
  assert.equal(closes, 0);
  second?.open();
  await secondConnection;
  assert.equal(transport.connected, true);
});

test("admin transport factory can select a browser-local implementation", () => {
  const local = { connected: false, reconnect: false, connect: async () => {}, send() {}, close() {} };
  configureAdminTransport(() => local);
  assert.equal(createAdminTransport(), local);
  configureAdminTransport(() => new WebSocketAdminTransport());
});
