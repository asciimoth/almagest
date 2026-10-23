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

test("focus and visible-page events request an immediate reconnect", () => {
  assert.match(app, /window\.addEventListener\("focus", requestImmediateAPIReconnect\)/);
  assert.match(app, /document\.addEventListener\("visibilitychange",[\s\S]*document\.visibilityState === "visible"[\s\S]*requestImmediateAPIReconnect/);
});
