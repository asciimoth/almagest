import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import { JSDOM } from "jsdom";

const source = await readFile(new URL("../../app/isolation/index.js", import.meta.url), "utf8");

test("isolation hook does not use HTML or script execution sinks", () => {
  assert.doesNotMatch(source, /innerHTML|outerHTML|insertAdjacentHTML|document\.write|eval\s*\(|new Function/);
});

function isolationHook() {
  const dom = new JSDOM("<!doctype html>", { runScripts: "outside-only" });
  dom.window.eval(source);
  return dom.window.__TAURI_ISOLATION_HOOK__;
}

test("isolation hook permits only the two admin operations", () => {
  const filter = isolationHook();
  const listen = { cmd: "listen_admin", payload: { channel: "__CHANNEL__:42" } };
  const send = {
    cmd: "send_admin_message",
    payload: {
      message: JSON.stringify({
        version: "v1",
        kind: "request",
        id: "1",
        surface: "admin",
        method: "hello",
        payload: {},
      }),
    },
  };

  assert.equal(filter(listen), listen);
  assert.equal(filter(send), send);
  for (const request of [
    { cmd: "listen_admin", payload: { channel: "bad" } },
    { cmd: "listen_admin", payload: { channel: "__CHANNEL__:1", extra: true } },
    { cmd: "send_admin_message", payload: { message: "not JSON" } },
    { cmd: "unrelated", payload: {} },
  ]) assert.throws(() => filter(request), /blocked IPC operation/);
});

test("isolation hook permits only Tauri's narrow channel-data fetch", () => {
  const filter = isolationHook();
  const fetch = {
    cmd: "plugin:__TAURI_CHANNEL__|fetch",
    payload: null,
    options: { headers: { "Tauri-Channel-Id": "17" } },
  };

  assert.equal(filter(fetch), fetch);
  for (const request of [
    { ...fetch, payload: {} },
    { ...fetch, options: {} },
    { ...fetch, options: { headers: { "Tauri-Channel-Id": "not-a-number" } } },
    { ...fetch, options: { headers: { "Tauri-Channel-Id": "17", extra: "value" } } },
    { ...fetch, options: { headers: { "Tauri-Channel-Id": "17" }, extra: true } },
  ]) assert.throws(() => filter(request), /blocked IPC operation/);
});
