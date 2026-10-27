import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";

const css = await readFile(new URL("../style.css", import.meta.url), "utf8");

function cssRule(selector) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = css.match(new RegExp(`^${escaped}\\s*\\{(?<body>[^}]*)\\}`, "m"));
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

test("internal documentation links are distinct from external links", () => {
  const rule = cssRule(".node-description .markdown-link-docs,\n.node-description .markdown-link-docs:visited,\n.node-description .markdown-link-panel,\n.node-description .markdown-link-panel:visited");

  assert.match(rule, /color:\s*var\(--teal\);/);
  assert.match(rule, /text-decoration-style:\s*dashed;/);
});

test("highlighted documentation preview ports have a visible outline", () => {
  const rule = cssRule(".class-preview-port-highlighted");

  assert.match(rule, /outline:\s*2px solid var\(--yellow\);/);
});

test("the description header puts its documentation link at the right edge", () => {
  const rule = cssRule(".node-description-header");

  assert.match(rule, /display:\s*flex;/);
  assert.match(rule, /justify-content:\s*space-between;/);
});

test("SPA tab and locale icons use one fixed control width", () => {
  const tabRule = cssRule(".tabs button");
  const localeRule = cssRule(".locale-picker");

  for (const rule of [tabRule, localeRule]) {
    assert.match(rule, /width:\s*38px;/);
    assert.match(rule, /min-width:\s*38px;/);
    assert.match(rule, /padding:\s*0;/);
  }
});

test("SPA tab icons use a consistent line style", () => {
  const rule = cssRule(".tab-icon");

  assert.match(rule, /width:\s*19px;/);
  assert.match(rule, /height:\s*19px;/);
  assert.match(rule, /stroke:\s*currentColor;/);
  assert.match(rule, /stroke-linecap:\s*round;/);
  assert.match(rule, /stroke-linejoin:\s*round;/);
});

test("select controls do not use browser-specific native styling", () => {
  const selectRule = cssRule("select");
  const singleSelectRule = cssRule("select:not([multiple])");
  const optionRule = cssRule("select option");

  assert.match(selectRule, /-webkit-appearance:\s*none;/);
  assert.match(selectRule, /appearance:\s*none;/);
  assert.match(singleSelectRule, /padding-inline-end:\s*28px;/);
  assert.match(singleSelectRule, /background-image:\s*url\(/);
  assert.match(singleSelectRule, /background-repeat:\s*no-repeat;/);
  assert.match(optionRule, /background-color:\s*var\(--crust\);/);
  assert.match(optionRule, /color:\s*var\(--text\);/);
  assert.match(cssRule("#locale-select"), /background-image:\s*none;/);
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

test("compact layouts preserve content space and expose mobile navigation", () => {
  assert.match(css, /grid-template-columns:\s*minmax\(0, 1fr\) clamp\(17rem, 34vw, 22rem\);/);
  assert.match(css, /\.tab-list\s*\{[^}]*overflow-x:\s*auto;/s);
  assert.match(css, /\.tabs \.tab-list button:not\(\[hidden\]\)\s*\{[^}]*display:\s*flex;/s);
  assert.doesNotMatch(css, /\.tabs \.tab-list button\s*\{[^}]*display:\s*flex;/s);
  assert.match(css, /\.docs-tree\.open\s*\{[^}]*transform:\s*translateX\(0\);/s);
  assert.match(css, /\.docs-navigation-scrim\.open\s*\{/);
});

test("compact entry actions align at the row edge", () => {
  assert.match(css, /@media \(max-width: 56\.25rem\)[\s\S]*?\.kv-row\s*\{[^}]*grid-template-columns:\s*24px minmax\(0, 1fr\) max-content;/);
  assert.match(css, /@media \(max-width: 32\.5rem\)[\s\S]*?\.kv-row-actions\s*\{[^}]*justify-content:\s*flex-end;/);
  assert.match(css, /@media \(max-width: 68\.75rem\)[\s\S]*?\.snapshot-row-actions\s*\{[^}]*justify-content:\s*flex-end;/);
});

test("snapshot and example rows highlight under the cursor", () => {
  const rule = cssRule(".snapshot-row:hover,\n.snapshot-row:focus-within");

  assert.match(rule, /border-color:\s*color-mix\(in srgb, var\(--blue\), var\(--surface0\) 62%\);/);
  assert.match(rule, /background:\s*color-mix\(in srgb, var\(--surface0\), transparent 62%\);/);
});

test("compact logs use a minimum-width hybrid layout", () => {
  assert.match(css, /\.logs-scroll\s*\{[^}]*overflow-x:\s*auto;/s);
  assert.match(css, /#logs-list\s*\{[^}]*min-width:\s*36rem;/s);
  assert.match(css, /"level time source"\s*\n\s*"message message message"/);
});
