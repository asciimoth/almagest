import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import { JSDOM } from "jsdom";

const html = await readFile(new URL("../index.html", import.meta.url), "utf8");

test("Sec tab is available between Graph and DNS", () => {
  const dom = new JSDOM(html);
  const tabs = [...dom.window.document.querySelectorAll("[data-tab]")].map((button) => button.textContent?.trim());

  assert.deepEqual(tabs.slice(0, 3), ["Graph", "Sec", "DNS"]);
  assert.equal(dom.window.document.querySelector("[data-panel='sec'] #sec-tree") !== null, true);
  assert.equal(dom.window.document.querySelector("[data-panel='sec'] #sec-value-panel") !== null, true);
});
