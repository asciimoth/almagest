import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import { JSDOM } from "jsdom";

const html = await readFile(new URL("../index.html", import.meta.url), "utf8");

test("Sec and TLS tabs are available in data-management order", () => {
  const dom = new JSDOM(html);
  const tabs = [...dom.window.document.querySelectorAll("[data-tab]")].map((button) => button.textContent?.trim());

  assert.deepEqual(tabs.slice(0, 5), ["Graph", "Sec", "DNS", "TLS", "KV"]);
  assert.equal(dom.window.document.querySelector("[data-panel='sec'] #sec-tree") !== null, true);
  assert.equal(dom.window.document.querySelector("[data-panel='sec'] #sec-value-panel") !== null, true);
  assert.equal(dom.window.document.querySelector("[data-panel='tls'] #tls-tree") !== null, true);
  assert.equal(dom.window.document.querySelector("[data-panel='tls'] #tls-value-panel") !== null, true);
});
