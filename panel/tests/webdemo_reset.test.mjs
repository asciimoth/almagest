import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import { JSDOM } from "jsdom";

import { installWebDemoReset } from "../public/build/webdemo_reset.js";

const html = await readFile(new URL("../index.html", import.meta.url), "utf8");

test("web demo reset clears only web demo data and reloads the page", () => {
  const document = new JSDOM(html).window.document;
  const values = new Map([
    ["almagest.webdemo.config.v1", "config"],
    ["almagest.webdemo.kv.v1", "kv"],
    ["almagest.webdemo.secrets.v1", "secrets"],
    ["almagest.panel.locale.v1", "en"],
    ["unrelated", "keep"],
  ]);
  const removed = [];
  let reloads = 0;

  installWebDemoReset(document, {
    removeItem(key) {
      removed.push(key);
      values.delete(key);
    },
  }, () => reloads += 1);

  const button = document.querySelector("#webdemo-reload-default");
  assert.equal(button?.parentElement?.nextElementSibling?.id, "sidekick");
  button?.dispatchEvent(new document.defaultView.Event("click"));

  assert.deepEqual(removed, [
    "almagest.webdemo.config.v1",
    "almagest.webdemo.kv.v1",
    "almagest.webdemo.secrets.v1",
  ]);
  assert.deepEqual([...values], [
    ["almagest.panel.locale.v1", "en"],
    ["unrelated", "keep"],
  ]);
  assert.equal(reloads, 1);
});

test("admin HTML does not contain the web demo reset control", () => {
  const document = new JSDOM(html).window.document;

  assert.equal(document.querySelector("#webdemo-reload-default"), null);
  assert.equal(document.querySelector(".webdemo-sidekick"), null);
});

test("web demo reset reloads when browser storage is unavailable", () => {
  const document = new JSDOM(html).window.document;
  let reloads = 0;

  installWebDemoReset(document, {
    removeItem() {
      throw new Error("storage disabled");
    },
  }, () => reloads += 1);

  document.querySelector("#webdemo-reload-default")?.dispatchEvent(new document.defaultView.Event("click"));

  assert.equal(reloads, 1);
});
