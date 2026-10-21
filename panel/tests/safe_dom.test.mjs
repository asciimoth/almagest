import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import { test } from "node:test";
import { JSDOM } from "jsdom";
import {
  classPreviewPorts,
  createDNSCacheTitle,
  createDNSKeyTitle,
  createDNSValuePreview,
  createClassButtonContent,
  createEmptyWorkerItem,
  createExperimentalNodeWarning,
  createKVEntryTitle,
  createKVStoreTitle,
  createKVValuePreview,
  createLogItem,
  createNodeNameForm,
  createSecretTitle,
  createSecretValuePreview,
  createTLSCertPreview,
  createTLSCertTitle,
  createTLSPolicyPreview,
  createTLSPolicyTitle,
  createWorkerNodeBlock,
  escapeHTMLText,
  renderNodeDescription,
  safeCSSColor,
  updateWorkerAgeElements,
} from "../public/build/safe_dom.js";
import { createNotificationToast } from "../public/build/notification_center.js";
import { setTypeColors } from "../public/build/pasta_model.js";

const payload = `<img src=x onerror="globalThis.__xss = true"><script>globalThis.__xss = true</script>`;

function installDOM() {
  const dom = new JSDOM("<!doctype html><main></main>", {
    url: "https://panel.test/",
  });
  globalThis.window = dom.window;
  globalThis.document = dom.window.document;
  return dom;
}

function assertNoParsedPayload(root) {
  assert.equal(root.querySelector("script,img,svg,iframe,math"), null);
  assert.equal(globalThis.__xss, undefined);
}

test("LiteGraph-bound text is escaped for its HTML parser", () => {
  assert.equal(
    escapeHTMLText(`<img src=x onerror='alert(1)' title="bad">&`),
    "&lt;img src=x onerror=&#39;alert(1)&#39; title=&quot;bad&quot;&gt;&amp;",
  );
});

test("daemon CSS values are restricted to colors", () => {
  installDOM();
  assert.equal(safeCSSColor("#89b4fa", "black"), "#89b4fa");
  assert.equal(safeCSSColor("rgb(10, 20, 30)", "black"), "rgb(10, 20, 30)");
  assert.equal(safeCSSColor("url(https://attacker.test/pixel)", "black"), "black");
  assert.equal(safeCSSColor("red; background: url(https://attacker.test/pixel)", "black"), "black");
});

test("node-sourced class picker text is not parsed as HTML", () => {
  installDOM();
  const root = document.createElement("button");
  root.append(...createClassButtonContent({
    class: `evil/${payload}`,
    short_description: payload,
    long_description: "",
    primary_type: "",
    initial_ports: [],
    experimental: true,
  }, [
    { name: payload, color: "#f00" },
  ], [
    { name: payload, color: "#0f0" },
  ]));

  assertNoParsedPayload(root);
  assert.match(root.textContent ?? "", /<img src=x/);
  assert.match(root.querySelector("strong")?.textContent ?? "", /⚠$/);
  assert.equal(
    root.querySelector(".class-experimental-sign")?.title,
    "Warning: This node type is experimental and may be removed or significantly changed.",
  );
});

test("node class previews use the shared type palette and fallback color", () => {
  setTypeColors({ "demo/known": { color: "#123456", textColor: "#ffffff" } });
  const cls = {
    class: "demo/node",
    short_description: "",
    unique: false,
    primary_type: "",
    initial_ports: [
      { direction: "input", name: "known", types: ["demo/known"] },
      { direction: "output", name: "fallback", types: ["demo/unknown"] },
    ],
    experimental: false,
  };

  assert.deepEqual(classPreviewPorts(cls, "left"), [{ name: "known", color: "#123456" }]);
  assert.deepEqual(classPreviewPorts(cls, "right"), [{ name: "fallback", color: "#9A9" }]);
  setTypeColors({});
});

test("experimental node warning explains the compatibility risk", () => {
  installDOM();
  const warning = createExperimentalNodeWarning();

  assert.equal(warning.className, "experimental-node-warning");
  assert.equal(warning.getAttribute("role"), "status");
  assert.equal(
    warning.textContent,
    "Warning: This node type is experimental and may be removed or significantly changed.",
  );
});

test("node name, logs, and notifications keep payloads as text", () => {
  installDOM();
  const root = document.createElement("div");
  root.append(
    createNodeNameForm(payload),
    createLogItem({
      at: payload,
      source: "node",
      level: "error",
      message: payload,
      node_id: 7,
      node_class: `evil/${payload}`,
    }),
    createNotificationToast(document, payload),
  );

  assertNoParsedPayload(root);
  assert.equal(root.querySelector("input")?.value, payload);
  assert.match(root.textContent ?? "", /<script>/);
});

test("worker process blocks keep node and worker payloads as text", () => {
  installDOM();
  const root = document.createElement("ol");
  root.append(
    createWorkerNodeBlock({
      nodeID: 4,
      node: {
        class: `evil/${payload}`,
        name: payload,
        primary_type: "demo/type",
        label: "",
        position: "",
        placeholder: false,
        root: false,
        has_root_path: false,
        left_ports: [],
        right_ports: [],
      },
      fallbackClass: `evil/${payload}`,
      fallbackName: payload,
      workers: [
        {
          id: 9,
          kind: "sandbox",
          node: 4,
          class: `evil/${payload}`,
          name: payload,
          worker_name: payload,
          started_at: new Date(Date.now() - 2000).toISOString(),
          orphan: false,
          error: payload,
        },
      ],
      color: "#89b4fa",
      textColor: "#ffffff",
    }, new Date()),
    createEmptyWorkerItem(),
  );

  assertNoParsedPayload(root);
  assert.match(root.textContent ?? "", /worker 9/);
  assert.equal(root.querySelector(".ps-worker-kind-sandbox")?.textContent, "sandbox process");
  assert.match(root.textContent ?? "", /<script>/);
});

test("worker age updates preserve existing worker rows", () => {
  installDOM();
  const root = document.createElement("ol");
  const started = new Date("2026-01-01T00:00:00.000Z").toISOString();
  root.append(
    createWorkerNodeBlock({
      nodeID: 4,
      node: undefined,
      fallbackClass: "demo/Worker",
      fallbackName: "worker-node",
      workers: [
        {
          id: 9,
          kind: "go",
          node: 4,
          class: "demo/Worker",
          name: "worker-node",
          worker_name: "runner",
          started_at: started,
          orphan: false,
        },
      ],
    }, new Date("2026-01-01T00:00:02.000Z")),
  );

  const row = root.querySelector(".ps-worker");
  const age = root.querySelector(".ps-worker-age");
  assert.equal(age?.textContent, "age 2s");

  updateWorkerAgeElements(root, new Date("2026-01-01T00:01:05.000Z"));

  assert.equal(root.querySelector(".ps-worker"), row);
  assert.equal(root.querySelector(".ps-worker-age"), age);
  assert.equal(root.querySelector(".ps-worker-kind-go")?.textContent, "Go worker");
  assert.equal(age?.textContent, "age 1m5s");
});

test("KV store and entry labels keep plugin and key payloads as text", () => {
  installDOM();
  const root = document.createElement("div");
  root.append(
    createKVStoreTitle({ kind: "plugin", name: payload }),
    createKVEntryTitle({ kind: "bucket", name: payload, owner: payload }),
    createKVEntryTitle({ kind: "value", name: payload, size: 12, string: true, hidden: true, owner: payload }),
  );

  assertNoParsedPayload(root);
  assert.match(root.textContent ?? "", /<script>/);
  assert.match(root.textContent ?? "", /hidden/);
  assert.match(root.textContent ?? "", /owned by/);
});

test("KV value preview renders text safely and covers hidden text", () => {
  installDOM();
  const visible = createKVValuePreview({
    data: payload,
    encoding: "string",
    size: payload.length,
    string: true,
  });
  const hidden = createKVValuePreview({
    data: payload,
    encoding: "string",
    size: payload.length,
    string: true,
    hidden: true,
  });
  const binary = createKVValuePreview({
    data: "AAEC",
    encoding: "base64",
    size: 3,
    string: false,
  });
  const root = document.createElement("div");
  root.append(visible, hidden, binary);

  assertNoParsedPayload(root);
  assert.equal(hidden.querySelector(".kv-value-spoiler") !== null, true);
  assert.equal(binary.querySelector("pre"), null);
  assert.match(root.textContent ?? "", /<img src=x/);
});

test("DNS cache labels and value previews keep payloads as text", () => {
  installDOM();
  const root = document.createElement("div");
  root.append(
    createDNSCacheTitle({ name: payload }),
    createDNSKeyTitle({ key: payload, size: 42, expires_at: payload }),
    createDNSValuePreview({ json: payload, size: payload.length, expires_at: payload }),
  );

  assertNoParsedPayload(root);
  assert.match(root.textContent ?? "", /<script>/);
  assert.match(root.textContent ?? "", /42 B/);
});

test("DNS values render cached JSON as human-readable records", () => {
  installDOM();
  const root = document.createElement("div");
  root.append(
    createDNSKeyTitle({ key: "example.test.|1|1", size: 42 }),
    createDNSValuePreview({
      json: JSON.stringify({
        message: {
          questions: [{ name: "example.test.", type: 1, class: 1 }],
          answers: [
            { name: "example.test.", type: 1, class: 1, ttl: 60, data: Buffer.from([192, 0, 2, 40]).toString("base64") },
          ],
        },
      }),
    }),
  );

  assertNoParsedPayload(root);
  assert.match(root.textContent ?? "", /example\.test\. IN A/);
  assert.match(root.textContent ?? "", /192\.0\.2\.40/);
  assert.equal(root.querySelector(".dns-record-preview") !== null, true);
});

test("secret labels and value previews keep payloads as covered text", () => {
  installDOM();
  const root = document.createElement("div");
  root.append(
    createSecretTitle({
      key: payload,
      value: payload,
      nodes: [payload],
      source: "protected",
      read_only: true,
    }),
    createSecretValuePreview({
      key: payload,
      value: payload,
      nodes: [payload],
      source: "mutable",
    }),
  );

  assertNoParsedPayload(root);
  assert.equal(root.querySelector(".sec-value-spoiler") !== null, true);
  assert.match(root.textContent ?? "", /<script>/);
  assert.match(root.textContent ?? "", /read-only/);
});

test("secret metadata and oversized previews do not disclose value length", () => {
  installDOM();
  const root = document.createElement("div");
  root.append(
    createSecretTitle({
      key: "api-token",
      value: "abcd",
      nodes: [],
      source: "mutable",
    }),
    createSecretValuePreview({
      key: "api-token",
      value: "abcd",
      source: "mutable",
    }, { limit: 3 }),
  );

  assert.equal(root.textContent?.includes("4 chars"), false);
  assert.match(root.textContent ?? "", /Secret value is too large to preview\./);
});

test("TLS labels and previews keep certificate and policy payloads as text", () => {
  installDOM();
  const root = document.createElement("div");
  root.append(
    createTLSCertTitle({
      name: payload,
      pem: payload,
      subject: payload,
      issuer: payload,
      fingerprint: payload,
      source: "protected",
      read_only: true,
      certcat_matches: [{
        fingerprint: payload,
        match_kind: payload,
        kind: "hazard_certificate",
        hazard_class: "interception_ca",
        action: "warn",
        severity: "warning",
        recommendation: "trust_only_for_declared_scope",
        scope: {
          basis: payload,
          permitted_dns_subtrees: [payload],
          permitted_ip_cidrs: [payload],
          requires_explicit_allowlist: true,
          reject_ip_targets: true,
        },
      }],
    }),
    createTLSCertPreview({
      name: payload,
      pem: payload,
      subject: payload,
      issuer: payload,
      fingerprint: payload,
      source: "mutable",
      certcat_matches: [{
        fingerprint: payload,
        match_kind: payload,
        kind: "hazard_certificate",
        hazard_class: "interception_ca",
        action: "warn",
        severity: "warning",
        recommendation: "trust_only_for_declared_scope",
      }],
    }),
    createTLSCertTitle({
      name: "notice-cert",
      source: "mutable",
      certcat_matches: [{ severity: "info" }],
    }),
    createTLSPolicyTitle({
      name: payload,
      spec: {},
      valid: false,
      error: payload,
      watchers: [payload],
    }),
    createTLSPolicyPreview({
      name: payload,
      spec: {
        trust_anchors: [{ cert: payload, scope: { dns: [{ domain: payload, include_subdomains: true }] } }],
      },
      valid: false,
      error: payload,
      watchers: [payload],
    }),
  );

  assertNoParsedPayload(root);
  assert.match(root.textContent ?? "", /<script>/);
  assert.match(root.textContent ?? "", /read-only/);
  assert.match(root.textContent ?? "", /warning/);
  assert.match(root.textContent ?? "", /Interception certificate authority/);
  assert.equal(root.querySelector(".tls-cert-meta-status-warning")?.textContent, "warning");
  assert.equal(root.querySelector(".tls-cert-meta-status-notice")?.textContent, "notice");
  assert.doesNotMatch(root.textContent ?? "", /certcat warning/);
  assert.doesNotMatch(root.textContent ?? "", /warning · warn · hazard_certificate · interception_ca/);
  assert.doesNotMatch(root.textContent ?? "", /trust_only_for_declared_scope/);
  assert.equal(root.querySelector(".tls-policy-error") !== null, true);
});


test("node descriptions allow safe markdown only", () => {
  installDOM();
  const root = document.createElement("div");
  renderNodeDescription(root, `[${payload}](javascript:alert(1)) **${payload}** \`${payload}\``);

  assertNoParsedPayload(root);
  assert.equal(root.querySelector("a")?.hasAttribute("href"), false);
  assert.match(root.textContent ?? "", /<img src=x/);
});

test("node description links reject active and local URL schemes", () => {
  installDOM();
  const root = document.createElement("div");
  renderNodeDescription(root, [
    "[script](JaVaScRiPt:alert(1))",
    "[data](data:text/html,<script>alert(1)</script>)",
    "[local](file:///etc/passwd)",
    "[web](https://example.test/path)",
    "[mail](mailto:admin@example.test)",
  ].join(" "));

  const links = [...root.querySelectorAll("a")];
  assert.deepEqual(links.map((link) => link.hasAttribute("href")), [false, false, false, true, true]);
  assertNoParsedPayload(root);
});

test("node descriptions render multiline code blocks as text", () => {
  installDOM();
  const root = document.createElement("div");
  renderNodeDescription(root, [
    "Before",
    "",
    "```ts",
    "const value = `<script>globalThis.__xss = true</script>`;",
    "",
    "console.log(value);",
    "```",
    "",
    "After `inline`",
  ].join("\n"));

  assertNoParsedPayload(root);
  const blocks = [...root.children].map((child) => child.tagName.toLowerCase());
  assert.deepEqual(blocks, ["p", "pre", "p"]);

  const code = root.querySelector("pre > code");
  assert.equal(code?.className, "language-ts");
  assert.equal(code?.textContent, [
    "const value = `<script>globalThis.__xss = true</script>`;",
    "",
    "console.log(value);",
  ].join("\n"));
  assert.equal(root.querySelectorAll("p code").length, 1);
});

test("node descriptions render Markdown soft and hard line breaks", () => {
  installDOM();
  const root = document.createElement("div");
  renderNodeDescription(root, [
    "A regular line",
    "break is a space.",
    "Two spaces create a hard break.  ",
    "This stays on the next line.\\",
    "A backslash also creates a hard break.",
  ].join("\n"));

  const paragraph = root.querySelector("p");
  assert.equal(paragraph?.textContent, "A regular line break is a space. Two spaces create a hard break.This stays on the next line.A backslash also creates a hard break.");
  assert.equal(paragraph?.querySelectorAll("br").length, 2);
});

test("node description inline styles continue across soft line breaks", () => {
  installDOM();
  const root = document.createElement("div");
  renderNodeDescription(root, [
    "Before **Bold",
    " text** and *italic",
    " text* after.",
  ].join("\n"));

  assert.equal(root.querySelector("strong")?.textContent, "Bold text");
  assert.equal(root.querySelector("em")?.textContent, "italic text");
  assert.equal(root.querySelectorAll("br").length, 0);
  assert.equal(root.textContent, "Before Bold text and italic text after.");
});

test("node descriptions render unordered and ordered Markdown lists", () => {
  installDOM();
  const root = document.createElement("div");
  renderNodeDescription(root, [
    "Intro paragraph.",
    "- First **bold** item",
    "- Second item",
    "  continues on a soft line",
    "3. Third item",
    "4) Fourth `code` item",
    "",
    "After paragraph.",
  ].join("\n"));

  assert.deepEqual([...root.children].map((child) => child.tagName), ["P", "UL", "OL", "P"]);
  assert.deepEqual([...root.querySelectorAll("ul > li")].map((item) => item.textContent), [
    "First bold item",
    "Second item continues on a soft line",
  ]);
  const ordered = root.querySelector("ol");
  assert.equal(ordered?.start, 3);
  assert.deepEqual([...root.querySelectorAll("ol > li")].map((item) => item.textContent), [
    "Third item",
    "Fourth code item",
  ]);
  assert.equal(root.querySelectorAll("li strong").length, 1);
  assert.equal(root.querySelectorAll("li code").length, 1);
});

test("ordered list numbers other than one do not interrupt paragraphs", () => {
  installDOM();
  const root = document.createElement("div");
  renderNodeDescription(root, [
    "For example, `TCP`, `PORT 443`, `AND` matches TCP packets with destination port",
    "443.",
    "",
    "443. A list can still start with another number after a blank line.",
    "",
    "A paragraph",
    "1. A list starting with one interrupts it.",
    "",
    "Another paragraph",
    "-",
  ].join("\n"));

  assert.deepEqual([...root.children].map((child) => child.tagName), ["P", "OL", "P", "OL", "P"]);
  assert.equal(root.children[0]?.textContent, "For example, TCP, PORT 443, AND matches TCP packets with destination port 443.");
  assert.equal(root.querySelector("ol")?.start, 443);
  assert.equal(root.children[2]?.textContent, "A paragraph");
  assert.equal(root.children[3]?.textContent, "A list starting with one interrupts it.");
  assert.equal(root.children[4]?.textContent, "Another paragraph -");
});

test("node descriptions render headings as bold panel text", () => {
  installDOM();
  const root = document.createElement("div");
  renderNodeDescription(root, "# Main **heading**\n\nSubheading\n---\n\n#### Small heading ####");

  assert.equal(root.querySelector("h1,h2,h3,h4,h5,h6"), null);
  assert.deepEqual([...root.querySelectorAll(":scope > p > strong")].map((heading) => heading.textContent), [
    "Main heading",
    "Subheading",
    "Small heading",
  ]);
  assert.equal(root.querySelectorAll(":scope > p > strong strong").length, 1);
});

test("documentation headings use levels and unique URL anchor IDs", () => {
  installDOM();
  const root = document.createElement("div");
  renderNodeDescription(root, [
    "# Café & routing",
    "## Duplicate",
    "## Duplicate",
    "###### **Styled** heading #",
    "####### Not a heading",
  ].join("\n"), "docs");

  assert.deepEqual([...root.children].map((child) => child.tagName), ["H1", "H2", "H2", "H6", "P"]);
  assert.deepEqual([...root.querySelectorAll("h1,h2,h3,h4,h5,h6")].map((heading) => heading.id), [
    "cafe-routing",
    "duplicate",
    "duplicate-1",
    "styled-heading",
  ]);
  assert.equal(root.querySelector("h6 strong")?.textContent, "Styled");
  assert.equal(root.lastElementChild?.textContent, "####### Not a heading");
});

test("node descriptions render all GitHub alert types and their Markdown bodies", () => {
  installDOM();
  const root = document.createElement("div");
  renderNodeDescription(root, [
    "> [!NOTE]",
    "> A **note** with two lines.",
    "> The second line is soft-wrapped.",
    "",
    "> [!TIP]",
    "> - First tip",
    "> - Second `tip`",
    "",
    "> [!IMPORTANT]",
    "> Important text.",
    "",
    "> [!WARNING]",
    "> Warning text.",
    "",
    "> [!CAUTION]",
    "> Caution text.",
  ].join("\n"), "docs");

  const alerts = [...root.querySelectorAll(":scope > .markdown-alert")];
  assert.deepEqual(alerts.map((alert) => alert.className), [
    "markdown-alert markdown-alert-note",
    "markdown-alert markdown-alert-tip",
    "markdown-alert markdown-alert-important",
    "markdown-alert markdown-alert-warning",
    "markdown-alert markdown-alert-caution",
  ]);
  assert.deepEqual(alerts.map((alert) => alert.getAttribute("aria-label")), [
    "note", "tip", "important", "warning", "caution",
  ]);
  assert.equal(alerts[0]?.querySelector("strong")?.textContent, "note");
  assert.deepEqual([...alerts[1].querySelectorAll("li")].map((item) => item.textContent), [
    "First tip", "Second tip",
  ]);
  assert.equal(alerts[1]?.querySelector("code")?.textContent, "tip");
});

test("GitHub alerts require an exact uppercase marker and quoted body lines", () => {
  installDOM();
  const root = document.createElement("div");
  renderNodeDescription(root, [
    "> [!note]",
    "> Lowercase is ordinary text.",
    "",
    "> [!NOTE] extra",
    "> A suffix makes this ordinary text.",
    "",
    "> [!NOTE]",
    "This unquoted line is outside the empty alert.",
  ].join("\n"));

  assert.equal(root.querySelectorAll(".markdown-alert").length, 1);
  assert.equal(root.querySelector(".markdown-alert")?.children.length, 1);
  assert.match(root.children[0]?.textContent ?? "", /\[!note\]/);
  assert.match(root.children[1]?.textContent ?? "", /\[!NOTE\] extra/);
  assert.equal(root.lastElementChild?.textContent, "This unquoted line is outside the empty alert.");
});

test("Markdown images support safe URLs, relative paths, alt text, and hover titles", () => {
  installDOM();
  const root = document.createElement("div");
  renderNodeDescription(root, [
    '![Relative logo](/images/logo.svg "Logo hover text")',
    "![External](https://images.example.test/picture.png 'External title')",
    `![${payload}](javascript:evil '${payload}')`,
    "![Mail](mailto:admin@example.test)",
  ].join("\n"));

  const images = [...root.querySelectorAll("img")];
  assert.equal(images.length, 4);
  assert.equal(images[0]?.src, "https://panel.test/images/logo.svg");
  assert.equal(images[0]?.alt, "Relative logo");
  assert.equal(images[0]?.title, "Logo hover text");
  assert.equal(images[0]?.loading, "lazy");
  assert.equal(images[1]?.src, "https://images.example.test/picture.png");
  assert.equal(images[1]?.title, "External title");
  assert.equal(images[2]?.hasAttribute("src"), false);
  assert.equal(images[2]?.alt, payload);
  assert.equal(images[2]?.title, payload);
  assert.equal(images[3]?.hasAttribute("src"), false);
  assert.equal(root.querySelector("script,svg,iframe,math"), null);
  assert.equal(globalThis.__xss, undefined);
});

test("Markdown links resolve relative destinations and preserve safe hover titles", () => {
  installDOM();
  const root = document.createElement("div");
  renderNodeDescription(root, [
    '[Relative](../guides/start "Start guide")',
    "[Root](/docs/reference)",
    "[Fragment](#details)",
  ].join(" "));

  const links = [...root.querySelectorAll("a")];
  assert.deepEqual(links.map((link) => link.href), [
    "https://panel.test/guides/start",
    "https://panel.test/docs/reference",
    "https://panel.test/#details",
  ]);
  assert.equal(links[0]?.title, "Start guide");
  assert.equal(links.every((link) => link.rel === "noopener noreferrer"), true);
});

test("alert contents and image attributes do not parse hostile HTML", () => {
  installDOM();
  const root = document.createElement("div");
  renderNodeDescription(root, [
    "> [!WARNING]",
    `> ${payload}`,
    "> ![safe alt](data:image/svg+xml,evil '<script>bad</script>')",
  ].join("\n"));

  const image = root.querySelector(".markdown-alert img");
  assert.equal(image?.hasAttribute("src"), false);
  assert.equal(image?.title, "<script>bad</script>");
  assert.equal(root.querySelector("script,svg,iframe,math"), null);
  assert.equal(globalThis.__xss, undefined);
  assert.match(root.textContent ?? "", /<script>/);
});

test("panel source does not use script execution or HTML parser sinks", async () => {
  const files = (await readdir(new URL("../src/", import.meta.url)))
    .filter((file) => file.endsWith(".ts"))
    .map((file) => `src/${file}`);
  for (const file of files) {
    const source = await readFile(new URL(`../${file}`, import.meta.url), "utf8");
    assert.doesNotMatch(
      source,
      /\b(?:innerHTML|outerHTML|insertAdjacentHTML|DOMParser|srcdoc)\b|\bdocument\.write\s*\(|\beval\s*\(|\bnew\s+Function\b/,
      file,
    );
  }
});

test("panel startup disables LiteGraph script nodes", async () => {
  const source = await readFile(new URL("../src/app.ts", import.meta.url), "utf8");
  assert.match(source, /LiteGraph\.allow_scripts\s*=\s*false/);
});

test("panel startup disables overlapping LiteGraph node UI", async () => {
  const source = await readFile(new URL("../src/app.ts", import.meta.url), "utf8");
  assert.match(source, /canvas\.onShowNodePanel\s*=\s*scheduleSidekickUpdate/);
  assert.match(source, /canvas\.processContextMenu\s*=.*=>[\s\S]*?if \(node\) return;/);
});
