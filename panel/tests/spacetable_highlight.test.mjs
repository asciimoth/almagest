import assert from "node:assert/strict";
import { test } from "node:test";
import { JSDOM } from "jsdom";
import { renderSpaceTableHighlight, tokenizeSpaceTable } from "../public/build/spacetable_highlight.js";

function significantTokens(source) {
  return tokenizeSpaceTable(source)
    .filter((token) => token.kind !== "plain" || token.text.trim() !== "")
    .map((token) => [token.kind, token.text]);
}

test("tokenizer recognizes comments and space-separated table cells", () => {
  const source = [
    "  # Local service records",
    "app.example.test A 192.0.2.10",
    "example.test\tMX\t10\tmail.example.test",
    "_https._tcp.example.test SRV 10 5 443 app.example.test",
    "example.test TXT \"managed locally\"",
  ].join("\n");

  assert.deepEqual(significantTokens(source), [
    ["comment", "# Local service records"],
    ["key", "app.example.test"],
    ["value", "A"],
    ["value", "192.0.2.10"],
    ["key", "example.test"],
    ["value", "MX"],
    ["number", "10"],
    ["value", "mail.example.test"],
    ["key", "_https._tcp.example.test"],
    ["value", "SRV"],
    ["number", "10"],
    ["number", "5"],
    ["number", "443"],
    ["value", "app.example.test"],
    ["key", "example.test"],
    ["value", "TXT"],
    ["string", "\"managed locally\""],
  ]);
});

test("tokenizer preserves whitespace, CRLF, drafts, escapes, and data hashes", () => {
  const sources = [
    "\tkey\tvalue\r\nnext 2\r\n",
    "key TXT \"escaped \\\" quote\"",
    "key TXT \"unfinished",
    "key value # data, not a comment",
    "  # comment without a final newline",
    "unfinished <>&\u0000",
  ];

  for (const source of sources) {
    assert.equal(tokenizeSpaceTable(source).map((token) => token.text).join(""), source);
  }
  assert.deepEqual(significantTokens("key value # text"), [
    ["key", "key"],
    ["value", "value"],
    ["value", "#"],
    ["value", "text"],
  ]);
});

test("renderer keeps arbitrary table text out of the HTML parser", () => {
  const dom = new JSDOM("<!doctype html><pre id='highlight'></pre>", {
    runScripts: "dangerously",
  });
  const highlight = dom.window.document.querySelector("#highlight");
  const payload = [
    "</span><img src=x onerror=globalThis.__spacetableXSS=true> value",
    "# <script>globalThis.__spacetableXSS=true</script>",
    "key \"<svg/onload=globalThis.__spacetableXSS=true>\"",
  ].join("\n");

  renderSpaceTableHighlight(highlight, payload);

  assert.equal(highlight.textContent, payload);
  assert.equal(highlight.querySelector("script,img,svg,iframe,math"), null);
  assert.equal(dom.window.__spacetableXSS, undefined);
  assert.equal(
    [...highlight.querySelectorAll("span")].every((span) => /^spacetable-(?:comment|key|number|string|value)$/.test(span.className)),
    true,
  );
});
