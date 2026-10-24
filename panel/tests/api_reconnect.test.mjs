import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";

import {
  API_RECONNECT_MAX_DELAY_MS,
  apiReconnectDelay,
} from "../public/build/api_reconnect.js";

const app = await readFile(new URL("../src/app.ts", import.meta.url), "utf8");

test("API reconnect delay grows exponentially and has a maximum", () => {
  assert.deepEqual(
    [0, 1, 2, 3, 4, 5].map(apiReconnectDelay),
    [1_000, 2_000, 4_000, 8_000, 16_000, 30_000],
  );
  assert.equal(apiReconnectDelay(20), API_RECONNECT_MAX_DELAY_MS);
});

test("focus, restored-page, and visible-page events request an immediate reconnect", () => {
  assert.match(app, /window\.addEventListener\("focus", requestImmediateAPIReconnect\)/);
  assert.match(app, /window\.addEventListener\("pageshow",[\s\S]*event\.persisted[\s\S]*requestImmediateAPIReconnect/);
  assert.match(app, /document\.addEventListener\("visibilitychange",[\s\S]*document\.visibilityState === "visible"[\s\S]*requestImmediateAPIReconnect/);
});

test("page boot and immediate reconnect drop the previous backoff state", () => {
  const reset = app.slice(
    app.indexOf("function resetAPIReconnectBackoff"),
    app.indexOf("function requestImmediateAPIReconnect"),
  );
  assert.match(reset, /window\.clearTimeout\(apiReconnectTimer\)/);
  assert.match(reset, /apiReconnectTimer = 0/);
  assert.match(reset, /apiReconnectAttempt = 0/);

  const reconnect = app.slice(
    app.indexOf("function requestImmediateAPIReconnect"),
    app.indexOf("function request(", app.indexOf("function requestImmediateAPIReconnect")),
  );
  assert.match(reconnect, /resetAPIReconnectBackoff\(\)/);

  const boot = app.slice(app.indexOf("function boot"), app.indexOf("boot();"));
  assert.match(boot, /resetAPIReconnectBackoff\(\)[\s\S]*void startAPIConnection\(\)/);
});

test("an immediate reconnect cancels a pending connection attempt", () => {
  const reconnect = app.slice(
    app.indexOf("function requestImmediateAPIReconnect"),
    app.indexOf("function request(", app.indexOf("function requestImmediateAPIReconnect")),
  );
  assert.match(reconnect, /if \(apiConnectPending\)/);
  assert.match(reconnect, /apiReconnectAfterPending = true/);
  assert.match(reconnect, /apiTransport\?\.close\(\)/);

  const connection = app.slice(
    app.indexOf("async function startAPIConnection"),
    app.indexOf("function boot", app.indexOf("async function startAPIConnection")),
  );
  assert.match(connection, /if \(apiReconnectAfterPending\)[\s\S]*void startAPIConnection\(\)/);
});

test("the panel distinguishes the WebSocket handshake from API initialization", () => {
  const initialization = app.slice(
    app.indexOf("async function initializeAPISession"),
    app.indexOf("async function startAPIConnection"),
  );
  assert.match(initialization, /await connectAPI\(\);[\s\S]*app\.status\.initializing/);
});
