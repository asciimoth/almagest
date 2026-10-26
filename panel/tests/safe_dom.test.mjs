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
  createClassPreview,
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

  const preview = createClassPreview(
    cls,
    [{ name: "known", color: "#123456" }],
    [{ name: payload, color: "#9A9" }],
  );
  const rows = [...preview.querySelectorAll(".class-preview-port")];
  assert.deepEqual(rows.map((row) => [row.dataset.portSide, row.dataset.portName]), [
    ["left", "known"],
    ["right", payload],
  ]);
  assertNoParsedPayload(preview);
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
  assert.equal(root.querySelector(".ps-worker-sandbox")?.title, "Sandbox process");
  assert.equal(root.querySelector(".ps-worker-kind"), null);
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
  assert.equal(root.querySelector(".ps-worker-go")?.title, "Built-in Go worker");
  assert.equal(root.querySelector(".ps-worker-kind"), null);
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

test("documentation graph fences use the graph renderer and stay out of HTML parsers", () => {
  installDOM();
  const root = document.createElement("div");
  const source = `{"${payload}": {"Class": "demo/Node"}}`;
  const calls = [];
  renderNodeDescription(root, `Before\n\n\`\`\`graph\n${source}\n\`\`\`\n\nAfter`, "docs", "guide/graph", undefined, undefined,
    (value, host) => {
      calls.push([value, host]);
      host.append(document.createTextNode(value));
    });

  assert.equal(calls.length, 1);
  assert.equal(calls[0][0], source);
  assert.equal(calls[0][1].className, "graph-preview");
  assert.equal(root.querySelector("pre"), null);
  assertNoParsedPayload(root);
  assert.match(root.textContent ?? "", /Before/);
  assert.match(root.textContent ?? "", /After/);
});

test("compact panel node descriptions silently omit graph fences", () => {
  installDOM();
  const root = document.createElement("div");
  let rendered = false;
  renderNodeDescription(root, "Before\n\n```graph\n{}\n```\n\nAfter", "panel", "", undefined, undefined, () => {
    rendered = true;
  });

  assert.equal(rendered, false);
  assert.deepEqual([...root.children].map((child) => child.textContent), ["Before", "After"]);
  assert.equal(root.querySelector(".graph-preview, pre"), null);
});

test("node descriptions highlight JSON-family code blocks without parsing source as HTML", () => {
  installDOM();

  for (const language of ["json", "hjson", "hujson"]) {
    const root = document.createElement("div");
    const source = [
      "{",
      "  \"enabled\": true,",
      `  // ${payload}`,
      "  \"count\": 42,",
      "}",
    ].join("\n");
    renderNodeDescription(root, `\`\`\`${language}\n${source}\n\`\`\``);

    assertNoParsedPayload(root);
    const code = root.querySelector("pre > code");
    assert.equal(code?.className, `language-${language}`);
    assert.equal(code?.textContent, source);
    assert.equal(code?.querySelector(".hujson-key")?.textContent, "\"enabled\"");
    assert.equal(code?.querySelector(".hujson-keyword")?.textContent, "true");
    assert.equal(code?.querySelector(".hujson-comment")?.textContent, `// ${payload}`);
    assert.equal(code?.querySelector(".hujson-number")?.textContent, "42");
  }
});

test("node descriptions highlight RuleV1 code blocks without parsing source as HTML", () => {
  installDOM();
  const root = document.createElement("div");
  const source = [
    `# ${payload}`,
    "ADDR_RE (^|.*\\.)internal\\.example$",
    "SLOT 2",
  ].join("\n");

  renderNodeDescription(root, `\`\`\`rulev1\n${source}\n\`\`\``);

  assertNoParsedPayload(root);
  const code = root.querySelector("pre > code");
  assert.equal(code?.className, "language-rulev1");
  assert.equal(code?.textContent, source);
  assert.equal(code?.querySelector(".rulev1-comment")?.textContent, `# ${payload}`);
  assert.equal(code?.querySelector(".rulev1-instruction")?.textContent, "ADDR_RE");
  assert.equal(code?.querySelector(".rulev1-argument")?.textContent, "(^|.*\\.)internal\\.example$");
  assert.equal(code?.querySelector(".rulev1-number")?.textContent, "2");
});

test("node descriptions highlight space-separated tables without parsing source as HTML", () => {
  installDOM();
  const root = document.createElement("div");
  const source = [
    `# ${payload}`,
    "app.example.test A 192.0.2.10",
    "example.test MX 10 mail.example.test",
    "example.test TXT \"managed locally\"",
  ].join("\n");

  renderNodeDescription(root, `\`\`\`spacetable\n${source}\n\`\`\``);

  assertNoParsedPayload(root);
  const code = root.querySelector("pre > code");
  assert.equal(code?.className, "language-spacetable");
  assert.equal(code?.textContent, source);
  assert.equal(code?.querySelector(".spacetable-comment")?.textContent, `# ${payload}`);
  assert.equal(code?.querySelector(".spacetable-key")?.textContent, "app.example.test");
  assert.equal(code?.querySelector(".spacetable-value")?.textContent, "A");
  assert.equal(code?.querySelector(".spacetable-number")?.textContent, "10");
  assert.equal(code?.querySelector(".spacetable-string")?.textContent, "\"managed locally\"");
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

test("documentation links resolve for the panel and open in a new tab", () => {
  installDOM();
  const root = document.createElement("div");
  renderNodeDescription(root, '[MeshNames](@docs/node/almagest/MeshNames "Mesh resolver")');

  const link = root.querySelector("a");
  assert.equal(link?.getAttribute("href"), "./docs/node/almagest/MeshNames");
  assert.equal(link?.target, "_blank");
  assert.equal(link?.rel, "noopener noreferrer");
  assert.equal(link?.className, "markdown-link-docs");
  assert.equal(link?.title, "Mesh resolver");
});

test("documentation links use the hosting-mode URL resolver when supplied", () => {
  installDOM();
  const root = document.createElement("div");
  const destinations = [];
  renderNodeDescription(
    root,
    "[MeshNames](@docs/node/almagest/MeshNames)",
    "panel",
    "",
    undefined,
    (destination) => {
      destinations.push(destination);
      return `../docs/?page=${encodeURIComponent(destination)}`;
    },
  );

  const link = root.querySelector("a");
  assert.deepEqual(destinations, ["node/almagest/MeshNames"]);
  assert.equal(link?.getAttribute("href"), "../docs/?page=node%2Falmagest%2FMeshNames");
  assert.equal(link?.target, "_blank");
  assert.equal(link?.rel, "noopener noreferrer");
});

test("desktop documentation links stay in the current webview without parsing HTML", () => {
  installDOM();
  const root = document.createElement("div");
  renderNodeDescription(
    root,
    `[${payload}](@docs/node/almagest/Tun)`,
    "panel",
    "",
    undefined,
    () => "./docs/index.html?page=node%2Falmagest%2FTun",
    undefined,
    "_self",
  );

  const link = root.querySelector("a");
  assert.equal(link?.target, "_self");
  assertNoParsedPayload(root);
  // The generated href is fixed by the trusted resolver, not by Markdown.
  assert.equal(link?.getAttribute("href"), "./docs/index.html?page=node%2Falmagest%2FTun");
});

test("documentation links are relative to the current documentation page", () => {
  installDOM();
  const root = document.createElement("div");
  renderNodeDescription(root, [
    "[MeshNames](@docs/node/almagest/MeshNames)",
    "[Guide](@docs/guides/welcome)",
  ].join(" "), "docs", "node/almagest/DnsInject");

  const links = [...root.querySelectorAll("a")];
  assert.deepEqual(links.map((link) => link.getAttribute("href")), [
    "./MeshNames",
    "../../guides/welcome",
  ]);
  assert.equal(links.every((link) => link.target === ""), true);
});

test("port links render as safe bold text and report hover state", () => {
  const dom = installDOM();
  const root = document.createElement("div");
  const changes = [];
  renderNodeDescription(root, [
    '[Network](@port/right/Network "Output port")',
    `[Unsafe ${payload}](@port/left/Input)`,
    "[Wrong side](@port/top/Network)",
  ].join(" "), "docs", "node/demo", (side, name, highlighted) => {
    changes.push([side, name, highlighted]);
  });

  const ports = [...root.querySelectorAll(".markdown-link-port")];
  assert.equal(root.querySelector("a"), null);
  assert.equal(ports.length, 2);
  assert.equal(ports[0]?.tagName, "STRONG");
  assert.equal(ports[0]?.textContent, "Network");
  assert.equal(ports[0]?.dataset.portSide, "right");
  assert.equal(ports[0]?.dataset.portName, "Network");
  assert.equal(ports[0]?.title, "Output port");
  assertNoParsedPayload(root);

  ports[0]?.dispatchEvent(new dom.window.Event("pointerenter"));
  ports[0]?.dispatchEvent(new dom.window.Event("pointerleave"));
  assert.deepEqual(changes, [
    ["right", "Network", true],
    ["right", "Network", false],
  ]);
  assert.match(root.textContent ?? "", /Wrong side/);
});

test("custom Markdown links reject unknown types and unsafe documentation IDs", () => {
  installDOM();
  const root = document.createElement("div");
  renderNodeDescription(root, [
    `[Unknown ${payload}](@future/value)`,
    `[Traversal ${payload}](@docs/node/../secret)`,
  ].join(" "));

  assert.equal(root.querySelector("a"), null);
  assertNoParsedPayload(root);
  assert.match(root.textContent ?? "", /Unknown <img/);
});

test("documentation link paths encode scheme-like page IDs", () => {
  installDOM();
  const root = document.createElement("div");
  renderNodeDescription(root, "[Scheme](@docs/javascript:alert)");

  const link = root.querySelector("a");
  assert.equal(link?.getAttribute("href"), "./docs/javascript%3Aalert");
  assert.equal(link?.protocol, "https:");
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
  // Keep the desktop entry point inside the same automatic XSS scan as the
  // daemon panel and web demo entry points.
  assert.equal(files.includes("src/desktop.ts"), true);
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
