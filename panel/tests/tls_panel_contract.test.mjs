import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";

const app = await readFile(new URL("../public/build/app.js", import.meta.url), "utf8");

test("TLS tab exposes OS certificate reload action", () => {
  assert.match(app, /Reload OS certs/);
  assert.match(app, /reload_os_certs/);
});
