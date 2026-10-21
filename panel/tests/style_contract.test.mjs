import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";

const css = await readFile(new URL("../style.css", import.meta.url), "utf8");

function cssRule(selector) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = css.match(new RegExp(`${escaped}\\s*\\{(?<body>[^}]*)\\}`, "m"));
  assert.ok(match?.groups?.body, `missing CSS rule for ${selector}`);
  return match.groups.body;
}

test("links use the interface palette and focus treatment", () => {
  assert.match(cssRule("a"), /color:\s*var\(--blue\);/);
  assert.match(cssRule("a:visited"), /color:\s*var\(--mauve\);/);
  assert.match(cssRule("a:hover"), /var\(--blue\)/);
  assert.match(cssRule("a:active,\na:visited:active"), /color:\s*var\(--teal\);/);
  assert.match(cssRule("a:focus-visible"), /outline:\s*3px solid var\(--focus-ring\);/);
});

test("KV, DNS, Sec, and TLS value inspectors keep content top-aligned", () => {
  const rule = cssRule(".kv-value-panel");

  assert.match(rule, /grid-template-rows:\s*auto auto;/);
  assert.match(rule, /align-content:\s*start;/);
});

test("KV, DNS, Sec, and TLS toolbar buttons use compact tab controls", () => {
  const compactButtonRule = css.match(/(?<selectors>(?:\.[\w-]+\s+button,\n)+\.[\w-]+\s+button)\s*\{[^}]*min-height:\s*26px;[^}]*\}/m);

  assert.ok(compactButtonRule?.groups?.selectors, "missing compact button rule");
  assert.match(compactButtonRule.groups.selectors, /\.sec-header button/);
  assert.match(compactButtonRule.groups.selectors, /\.kv-toolbar button/);
});

test("TLS forms and policy rows collapse to one column on mobile", () => {
  assert.match(css, /\.tls-cert-form,/);
  assert.match(css, /\.tls-policy-form,/);
  assert.match(css, /\.tls-policy-rule-list li,/);
});
