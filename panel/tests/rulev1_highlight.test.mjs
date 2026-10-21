import assert from "node:assert/strict";
import { test } from "node:test";
import { JSDOM } from "jsdom";
import { renderRuleV1Highlight, tokenizeRuleV1 } from "../public/build/rulev1_highlight.js";

function significantTokens(source) {
  return tokenizeRuleV1(source)
    .filter((token) => token.kind !== "plain" || token.text.trim() !== "")
    .map((token) => [token.kind, token.text]);
}

test("tokenizer recognizes the common RuleV1 line structure", () => {
  const source = [
    "  # Route selected traffic",
    "ADDR_RE (^|.*\\.)internal\\.example$",
    "AND",
    "BACKEND slot 2",
    "REMAP DST ADDR_PORT 127.0.0.1:8080",
    "SNIFF HTTP URL_PATTERN:/admin*",
  ].join("\n");

  assert.deepEqual(significantTokens(source), [
    ["comment", "# Route selected traffic"],
    ["instruction", "ADDR_RE"],
    ["argument", "(^|.*\\.)internal\\.example$"],
    ["instruction", "AND"],
    ["instruction", "BACKEND"],
    ["argument", "slot"],
    ["number", "2"],
    ["instruction", "REMAP"],
    ["argument", "DST"],
    ["argument", "ADDR_PORT"],
    ["argument", "127.0.0.1:8080"],
    ["instruction", "SNIFF"],
    ["argument", "HTTP"],
    ["argument", "URL_PATTERN:/admin*"],
  ]);
});

test("tokenizer preserves whitespace, CRLF, invalid drafts, and non-comment hashes", () => {
  const sources = [
    "\tTRUE\r\nSLOT 1\r\n",
    "RULE cmd daemon --flag # part of the argument",
    "unfinished <>&\u0000",
    "  # comment without a final newline",
  ];

  for (const source of sources) {
    assert.equal(tokenizeRuleV1(source).map((token) => token.text).join(""), source);
  }
  assert.deepEqual(significantTokens("RULE cmd daemon # text"), [
    ["instruction", "RULE"],
    ["argument", "cmd"],
    ["argument", "daemon"],
    ["argument", "#"],
    ["argument", "text"],
  ]);
});

test("renderer keeps arbitrary rule text out of the HTML parser", () => {
  const dom = new JSDOM("<!doctype html><pre id='highlight'></pre>", {
    runScripts: "dangerously",
  });
  const highlight = dom.window.document.querySelector("#highlight");
  const payload = [
    "ADDR_S </span><img src=x onerror=globalThis.__rulev1XSS=true>",
    "# <script>globalThis.__rulev1XSS=true</script>",
    "<svg/onload=globalThis.__rulev1XSS=true>",
  ].join("\n");

  renderRuleV1Highlight(highlight, payload);

  assert.equal(highlight.textContent, payload);
  assert.equal(highlight.querySelector("script,img,svg,iframe,math"), null);
  assert.equal(dom.window.__rulev1XSS, undefined);
  assert.equal(
    [...highlight.querySelectorAll("span")].every((span) => /^rulev1-(?:argument|comment|instruction|number)$/.test(span.className)),
    true,
  );
});
