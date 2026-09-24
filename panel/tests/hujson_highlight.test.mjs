import assert from "node:assert/strict";
import { test } from "node:test";
import { JSDOM } from "jsdom";
import { renderHuJSONHighlight, tokenizeHuJSON } from "../public/build/hujson_highlight.js";

function significantTokens(source) {
  return tokenizeHuJSON(source)
    .filter((token) => token.kind !== "plain" || token.text.trim() !== "")
    .map((token) => [token.kind, token.text]);
}

test("tokenizer recognizes HuJSON comments, keys, values, and trailing commas", () => {
  const source = `{
    // line comment
    "name" /* key comment */ : "alm\\\"agest",
    "enabled": true,
    "count": -12.5e+2,
    "missing": null,
  }`;

  assert.deepEqual(significantTokens(source), [
    ["punctuation", "{"],
    ["comment", "// line comment"],
    ["key", '"name"'],
    ["comment", "/* key comment */"],
    ["punctuation", ":"],
    ["string", '"alm\\\"agest"'],
    ["punctuation", ","],
    ["key", '"enabled"'],
    ["punctuation", ":"],
    ["keyword", "true"],
    ["punctuation", ","],
    ["key", '"count"'],
    ["punctuation", ":"],
    ["number", "-12.5e+2"],
    ["punctuation", ","],
    ["key", '"missing"'],
    ["punctuation", ":"],
    ["keyword", "null"],
    ["punctuation", ","],
    ["punctuation", "}"],
  ]);
});

test("tokenizer preserves incomplete and invalid drafts exactly", () => {
  const sources = [
    '{"unfinished": "value',
    "{/* unterminated comment",
    '{"notKeyword": trueish, "notNumber": 12abc}',
    "\r\n\t bad input <>&\u0000",
  ];

  for (const source of sources) {
    assert.equal(tokenizeHuJSON(source).map((token) => token.text).join(""), source);
  }
  assert.deepEqual(significantTokens("trueish false_value null$ 12abc"), [
    ["plain", "trueish false_value null$ "],
    ["number", "12"],
    ["plain", "abc"],
  ]);
});

test("renderer keeps arbitrary config text out of the HTML parser", () => {
  const dom = new JSDOM("<!doctype html><pre id='highlight'></pre>", {
    runScripts: "dangerously",
  });
  const highlight = dom.window.document.querySelector("#highlight");
  const payload = [
    '"</pre><img src=x onerror=globalThis.__hujsonXSS=true>":',
    '"<script>globalThis.__hujsonXSS=true</script>",',
    '// <svg onload=globalThis.__hujsonXSS=true>',
  ].join("\n");

  renderHuJSONHighlight(highlight, payload);

  assert.equal(highlight.textContent, payload);
  assert.equal(highlight.querySelector("script,img,svg,iframe,math"), null);
  assert.equal(dom.window.__hujsonXSS, undefined);
  assert.equal(
    [...highlight.querySelectorAll("span")].every((span) => /^hujson-(?:comment|key|keyword|number|punctuation|string)$/.test(span.className)),
    true,
  );
});
