import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";

const source = await readFile(new URL("../src/desktop_admin_transport.ts", import.meta.url), "utf8");
const { DesktopAdminTransport } = await import("../public/build/desktop_admin_transport.js");

class FakeNativeAdminIPC {
  handler = null;
  handlers = [];
  sent = [];
  listenError = null;
  sendError = null;

  async listen(handler) {
    this.handler = handler;
    this.handlers.push(handler);
    if (this.listenError) throw this.listenError;
  }

  async send(message) {
    this.sent.push(message);
    if (this.sendError) throw this.sendError;
  }

  emit(event) {
    this.handler?.(event);
  }
}

test("desktop transport forwards connected native messages", async () => {
  const ipc = new FakeNativeAdminIPC();
  const messages = [];
  let closes = 0;
  const transport = new DesktopAdminTransport(ipc);
  const connection = transport.connect({
    message: (message) => messages.push(message),
    close: () => closes += 1,
  });

  ipc.emit({ type: "connection_status", connected: true });
  await connection;
  assert.equal(transport.connected, true);

  ipc.emit({ type: "message", message: '{"kind":"event"}' });
  transport.send("request");
  await Promise.resolve();
  assert.deepEqual(messages, ['{"kind":"event"}']);
  assert.deepEqual(ipc.sent, ["request"]);

  ipc.emit({ type: "connection_status", connected: false });
  assert.equal(transport.connected, false);
  assert.equal(closes, 1);
});

test("desktop transport reports initial disconnection while native retry remains active", async () => {
  const ipc = new FakeNativeAdminIPC();
  let closes = 0;
  let settled = false;
  const statuses = [];
  const diagnostics = [];
  const transport = new DesktopAdminTransport(ipc);
  const connection = transport.connect({
    message() {},
    close: () => closes += 1,
    status: (connected) => statuses.push(connected),
    diagnostic: (level, message) => diagnostics.push([level, message]),
  }).then(() => settled = true);

  ipc.emit({ type: "connection_status", connected: false });
  ipc.emit({ type: "connection_error", message: "connect to Unix socket: not found" });
  await Promise.resolve();

  assert.equal(settled, false);
  assert.equal(transport.connected, false);
  assert.equal(closes, 0);
  assert.deepEqual(statuses, [false]);
  assert.ok(diagnostics.some(([level, message]) =>
    level === "warn" && message === "connect to Unix socket: not found"));

  ipc.emit({ type: "connection_status", connected: true });
  await connection;
  assert.equal(transport.connected, true);
  assert.deepEqual(statuses, [false, true]);
});

test("desktop transport ignores malformed native events", async () => {
  const ipc = new FakeNativeAdminIPC();
  const messages = [];
  const transport = new DesktopAdminTransport(ipc);
  const connection = transport.connect({ message: (message) => messages.push(message), close() {} });

  for (const event of [null, [], {}, { type: "message", message: 3 }, { type: "connection_status", connected: "yes" }]) {
    ipc.emit(event);
  }
  assert.equal(transport.connected, false);
  assert.deepEqual(messages, []);
  ipc.emit({ type: "connection_status", connected: true });
  await connection;
});

test("desktop transport ignores events from a replaced native listener", async () => {
  const ipc = new FakeNativeAdminIPC();
  const firstMessages = [];
  const secondMessages = [];
  const transport = new DesktopAdminTransport(ipc);
  const first = transport.connect({ message: (message) => firstMessages.push(message), close() {} });
  ipc.emit({ type: "connection_status", connected: true });
  await first;
  const oldHandler = ipc.handlers[0];

  const second = transport.connect({ message: (message) => secondMessages.push(message), close() {} });
  ipc.emit({ type: "connection_status", connected: true });
  await second;
  oldHandler?.({ type: "message", message: "stale" });
  ipc.emit({ type: "message", message: "current" });

  assert.deepEqual(firstMessages, []);
  assert.deepEqual(secondMessages, ["current"]);
  assert.equal(transport.connected, true);
});

test("desktop transport reports listener and send failures", async () => {
  const listenIPC = new FakeNativeAdminIPC();
  listenIPC.listenError = "listener denied";
  let listenCloses = 0;
  const listener = new DesktopAdminTransport(listenIPC);
  await assert.rejects(
    listener.connect({ message() {}, close: () => listenCloses += 1 }),
    /register native admin listener: listener denied/,
  );
  assert.equal(listenCloses, 1);

  const sendIPC = new FakeNativeAdminIPC();
  let sendCloses = 0;
  const sender = new DesktopAdminTransport(sendIPC);
  const connection = sender.connect({ message() {}, close: () => sendCloses += 1 });
  sendIPC.emit({ type: "connection_status", connected: true });
  await connection;
  sendIPC.sendError = new Error("send failed");
  sender.send("request");
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(sender.connected, false);
  assert.equal(sendCloses, 1);
});

test("desktop transport times out when Tauri IPC reports no native status", async () => {
  const ipc = new FakeNativeAdminIPC();
  const diagnostics = [];
  let closes = 0;
  const transport = new DesktopAdminTransport(ipc, 5);

  await assert.rejects(
    transport.connect({
      message() {},
      close: () => closes += 1,
      diagnostic: (level, message) => diagnostics.push([level, message]),
    }),
    /Native listen_admin did not report status before the timeout/,
  );
  assert.equal(closes, 1);
  assert.deepEqual(diagnostics.at(-1), ["warn", "Native listen_admin did not report status before the timeout"]);
});

test("desktop native API fixes both Tauri command names and payloads", () => {
  assert.match(source, /invoke\("listen_admin", \{ channel \}\)/);
  assert.match(source, /invoke\("send_admin_message", \{ message \}\)/);
  assert.doesNotMatch(source, /innerHTML|outerHTML|insertAdjacentHTML|document\.write|eval\s*\(|new Function/);
});
