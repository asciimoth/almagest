import assert from "node:assert/strict";
import { test } from "node:test";
import { JSDOM } from "jsdom";

import { applyLocale, matchPreferredLocale, parseLocaleSnapshot, text } from "../public/build/localization.js";

test("browser locale matching follows preference order and available parents", () => {
  const available = [
    { locale: "en", name: "English" },
    { locale: "pt-BR", name: "Portuguese (Brazil)" },
    { locale: "zh-Hant", name: "Chinese (Traditional)" },
  ];

  assert.equal(matchPreferredLocale(available, ["pt_BR", "en-US"]), "pt-BR");
  assert.equal(matchPreferredLocale(available, ["fr-CA", "en-US"]), "en");
  assert.equal(matchPreferredLocale(available, ["zh-Hant-TW"]), "zh-Hant");
  assert.equal(matchPreferredLocale(available, ["pt-PT"]), null);
});

test("locale parser rejects malformed snapshots", () => {
  assert.equal(parseLocaleSnapshot(null), null);
  assert.equal(parseLocaleSnapshot({ requested: "en", effective: "en", available: [], panel: {}, revision: 1.5 }), null);
  assert.equal(parseLocaleSnapshot({ requested: "en", effective: "en", available: [{ locale: "en" }], panel: {}, revision: 1 }), null);
  assert.equal(parseLocaleSnapshot({ requested: "en", effective: "en", available: [], panel: { key: 1 }, revision: 1 }), null);
});

test("locale application uses text and attribute fallbacks safely", () => {
  const dom = new JSDOM(`<!doctype html><html lang="en"><body>
    <button data-i18n="action" data-i18n-title="action.title" title="Built-in title">Built-in</button>
    <input data-i18n-placeholder="input.placeholder" placeholder="Built-in placeholder">
  </body></html>`);
  const snapshot = parseLocaleSnapshot({
    requested: "lorem",
    effective: "lorem",
    available: [{ locale: "lorem", name: "Lorem" }],
    panel: { action: "<b>Safe text</b>", "action.title": "Localized title", greeting: "Hello {name}" },
    revision: 2,
  });
  assert.notEqual(snapshot, null);
  applyLocale(snapshot, dom.window.document);

  const button = dom.window.document.querySelector("button");
  const input = dom.window.document.querySelector("input");
  assert.equal(dom.window.document.documentElement.lang, "lorem");
  assert.equal(button?.textContent, "<b>Safe text</b>");
  assert.equal(button?.querySelector("b"), null);
  assert.equal(button?.title, "Localized title");
  assert.equal(input?.placeholder, "Built-in placeholder");
  assert.equal(text("greeting", "Fallback", { name: "world" }), "Hello world");
});

test("rerender keeps compiled English fallbacks", () => {
  const dom = new JSDOM(`<p data-i18n="message">English fallback</p>`);
  const first = { requested: "lorem", effective: "lorem", available: [], panel: { message: "Lorem" }, revision: 1 };
  const second = { requested: "en", effective: "en", available: [], panel: {}, revision: 2 };
  applyLocale(first, dom.window.document);
  applyLocale(second, dom.window.document);
  assert.equal(dom.window.document.querySelector("p")?.textContent, "English fallback");
});

test("rerender preserves the current dynamic status instead of its initial state", () => {
  const dom = new JSDOM(`<span data-i18n="app.status.loading">Loading</span>`);
  const status = dom.window.document.querySelector("span");
  status.dataset.i18n = "app.status.connected";
  status.dataset.i18nBuiltin = "Admin API connected";

  applyLocale({
    requested: "lorem",
    effective: "lorem",
    available: [],
    panel: {
      "app.status.loading": "Lorem loading",
      "app.status.connected": "Lorem connected",
    },
    revision: 1,
  }, dom.window.document);

  assert.equal(status.textContent, "Lorem connected");
});
