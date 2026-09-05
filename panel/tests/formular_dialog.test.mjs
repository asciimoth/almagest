import assert from "node:assert/strict";
import { test } from "node:test";
import { JSDOM } from "jsdom";
import { FormularMenu } from "../vendor/formular-menu/formular-menu.js";

const captcha = {
  type: "dialog.create",
  menuId: "NODE1MENU",
  menuGeneration: 1,
  dialog: {
    id: "amnesia-captcha-1-1",
    kind: "captcha",
    title: "Complete captcha",
    text: "Verify this request.",
    resources: [{
      id: "challenge",
      mimeType: "image/png",
      data: "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=",
      alt: "Captcha challenge",
    }],
    placeholder: "Captcha text",
    submitLabel: "Complete captcha",
  },
};

test("vendored Formular renderer opens captcha dialogs", () => {
  const dom = new JSDOM('<div id="node-menu"></div>', { url: "http://localhost/" });
  const previousDocument = globalThis.document;
  const previousWindow = globalThis.window;
  const previousMutationObserver = globalThis.MutationObserver;
  globalThis.document = dom.window.document;
  globalThis.window = dom.window;
  globalThis.MutationObserver = dom.window.MutationObserver;

  try {
    const menu = new FormularMenu("node-menu", "NODE1MENU", () => {});
    assert.equal(menu.feed(captcha), true);
    const dialog = dom.window.document.querySelector("dialog[data-dialog-id='amnesia-captcha-1-1']");
    assert.ok(dialog);
    assert.equal(dialog.hasAttribute("open"), true);
    assert.equal(dialog.querySelector("img")?.getAttribute("src")?.startsWith("data:image/png;base64,"), true);
    assert.equal(dialog.querySelector("input")?.getAttribute("aria-label"), "Captcha response");
  } finally {
    globalThis.document = previousDocument;
    globalThis.window = previousWindow;
    globalThis.MutationObserver = previousMutationObserver;
    dom.window.close();
  }
});

test("published panel versions its application module", async () => {
  const index = await (await import("node:fs/promises")).readFile(
    new URL("../../bin/almagestd/static/index.html", import.meta.url),
    "utf8",
  );
  assert.match(index, /\.\/public\/build\/app\.js\?v=[a-f0-9]{16}/);
});
