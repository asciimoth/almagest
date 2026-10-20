import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import { JSDOM } from "jsdom";

const html = await readFile(new URL("../index.html", import.meta.url), "utf8");
const litegraph = await readFile(new URL("../vendor/litegraph/litegraph.js", import.meta.url), "utf8");

test("the panel has a restrictive content security policy", () => {
  const dom = new JSDOM(html);
  const policy = dom.window.document.querySelector("meta[http-equiv='Content-Security-Policy']")?.getAttribute("content") ?? "";

  assert.match(policy, /default-src 'self'/);
  assert.match(policy, /base-uri 'none'/);
  assert.match(policy, /form-action 'none'/);
  assert.match(policy, /frame-src 'none'/);
  assert.match(policy, /object-src 'none'/);
  assert.match(policy, /script-src 'self'/);
  assert.match(policy, /worker-src 'none'/);
  assert.doesNotMatch(policy, /script-src[^;]*'unsafe-(?:inline|eval)'/);
});

test("the vendored LiteGraph build does not register eval-built nodes at startup", () => {
  assert.match(litegraph, /global\.LGraphCanvas = LiteGraph\.LGraphCanvas/);
  assert.doesNotMatch(litegraph, /LiteGraph\.wrapFunctionAsNode\s*\(/);
});

test("data-management tabs use clear labels and accessible tab semantics", () => {
  const dom = new JSDOM(html);
  const tabElements = [...dom.window.document.querySelectorAll("[data-tab]")];
  const tabs = tabElements.map((button) => button.textContent?.trim());

  assert.deepEqual(tabs.slice(0, 5), ["Graph", "Secrets", "DNS", "TLS", "KV"]);
  assert.equal(tabs.includes("Processes"), true);
  assert.equal(tabElements.every((tab) => tab.getAttribute("role") === "tab"), true);
  assert.equal(tabElements.every((tab) => tab.hasAttribute("aria-controls")), true);
  assert.equal(dom.window.document.querySelectorAll("[role='tabpanel']").length, tabElements.length);
  assert.equal(dom.window.document.querySelector("[data-panel='sec'] #sec-tree") !== null, true);
  assert.equal(dom.window.document.querySelector("[data-panel='sec'] #sec-value-panel") !== null, true);
  assert.equal(dom.window.document.querySelector("[data-panel='tls'] #tls-tree") !== null, true);
  assert.equal(dom.window.document.querySelector("[data-panel='tls'] #tls-value-panel") !== null, true);
});

test("language picker has a compact visible code and an accessible native select", () => {
  const dom = new JSDOM(html);
  const picker = dom.window.document.querySelector(".locale-picker");
  const icon = picker?.querySelector(".locale-picker-icon");
  const code = picker?.querySelector("#locale-code");
  const select = picker?.querySelector("#locale-select");

  assert.equal(icon?.getAttribute("aria-hidden"), "true");
  assert.equal(code?.textContent, "en");
  assert.equal(code?.getAttribute("aria-hidden"), "true");
  assert.equal(select?.getAttribute("aria-label"), "Language");
  assert.equal(select?.querySelector("option")?.textContent, "English");
});
