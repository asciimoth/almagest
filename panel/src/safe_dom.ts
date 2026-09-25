import type { ID, NodeClassSnapshot, NodeSnapshot, WorkerSnapshot } from "./pasta_model.js";
import {
  DNS_JSON_RENDER_LIMIT,
  dnsRecordsFromValue,
  formatDNSCacheKey,
  formatDNSByteSize,
  type DNSDisplayRecord,
  type DNSCacheInfo,
  type DNSKeyInfo,
  type DNSRecordSection,
  type DNSValue,
} from "./dns_model.js";
import { KV_TEXT_RENDER_LIMIT, formatKVByteSize, type KVEntryInfo, type KVStoreInfo, type KVValue } from "./kv_model.js";
import {
  SECRET_TEXT_RENDER_LIMIT,
  formatSecretSource,
  type SecretEntry,
} from "./secrets_model.js";
import {
  describeTLSCertcatRecommendation,
  describeTLSScope,
  formatTLSCertcatMatch,
  formatTLSCertMeta,
  formatTLSPolicyMeta,
  formatTLSPolicySpec,
  type TLSCertcatMatch,
  type TLSCertEntry,
  type TLSPolicyEntry,
  type TLSScope,
} from "./tls_model.js";

export const EXPERIMENTAL_NODE_WARNING = "This node type is experimental and may be removed or significantly changed.";

export type ClassPreviewPort = {
  name: string;
  color: string;
};

export type PanelLogEntry = {
  at: string;
  source: string;
  level: string;
  message: string;
  node_id?: number;
  node_class?: string;
};

export type WorkerNodeBlock = {
  nodeID: ID;
  node?: NodeSnapshot;
  fallbackClass: string;
  fallbackName: string;
  workers: WorkerSnapshot[];
  color?: string;
  textColor?: string;
};

export type KVValuePreviewOptions = {
  revealHidden?: boolean;
  limit?: number;
};

export type DNSValuePreviewOptions = {
  limit?: number;
};

export type SecretValuePreviewOptions = {
  reveal?: boolean;
  limit?: number;
};

// Third-party nodes control class names, labels, descriptions, popups, menus,
// and logs. Keep node-sourced text out of HTML parser APIs; build elements and
// assign text through textContent/value/title/DOM attributes instead.
export function textElement<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  text: string,
  className = "",
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  if (className) node.className = className;
  node.textContent = text;
  return node;
}

// LiteGraph parses menu labels as markup. Escape text before it crosses that
// third-party boundary. Panel-owned rendering must continue to use DOM nodes
// and textContent directly.
export function escapeHTMLText(value: string): string {
  return value.replace(/[&<>"']/g, (character) => {
    switch (character) {
      case "&": return "&amp;";
      case "<": return "&lt;";
      case ">": return "&gt;";
      case "\"": return "&quot;";
      default: return "&#39;";
    }
  });
}

// Colormap values come from the daemon and plugins. A custom property keeps
// punctuation from escaping a declaration, but a non-color value can still
// become active when it is substituted into a broad property such as
// background. Accept only values that the browser parses as CSS colors.
export function safeCSSColor(value: string, fallback: string): string {
  const probe = document.createElement("span");
  probe.style.color = value;
  return probe.style.color ? value : fallback;
}

export function iconSeparator(): HTMLElement {
  const icon = document.createElement("i");
  icon.setAttribute("aria-hidden", "true");
  return icon;
}

export function createClassButtonContent(
  cls: NodeClassSnapshot,
  leftPorts: ClassPreviewPort[],
  rightPorts: ClassPreviewPort[],
): HTMLElement[] {
  const copy = document.createElement("div");
  copy.className = "class-button-copy";
  const title = textElement("strong", shortClass(cls.class));
  if (cls.experimental) {
    const warning = textElement("span", " ⚠", "class-experimental-sign");
    warning.title = `Warning: ${EXPERIMENTAL_NODE_WARNING}`;
    warning.setAttribute("aria-label", `Warning: ${EXPERIMENTAL_NODE_WARNING}`);
    title.append(warning);
  }
  copy.append(title, textElement("span", cls.short_description));
  return [copy, createClassPreview(cls, leftPorts, rightPorts)];
}

export function createExperimentalNodeWarning(): HTMLElement {
  const warning = textElement("div", `Warning: ${EXPERIMENTAL_NODE_WARNING}`, "experimental-node-warning");
  warning.setAttribute("role", "status");
  return warning;
}

function createClassPreview(
  cls: NodeClassSnapshot,
  leftPorts: ClassPreviewPort[],
  rightPorts: ClassPreviewPort[],
): HTMLElement {
  const preview = document.createElement("div");
  preview.className = "class-preview";
  preview.setAttribute("aria-hidden", "true");

  const node = document.createElement("div");
  node.className = "class-preview-node";
  node.append(
    textElement("div", shortClass(cls.class), "class-preview-title"),
    createClassPreviewBody(leftPorts, rightPorts),
  );
  preview.append(node);
  return preview;
}

function createClassPreviewBody(leftPorts: ClassPreviewPort[], rightPorts: ClassPreviewPort[]): HTMLElement {
  const body = document.createElement("div");
  body.className = "class-preview-body";
  const left = document.createElement("div");
  left.className = "class-preview-column class-preview-column-left";
  const right = document.createElement("div");
  right.className = "class-preview-column class-preview-column-right";
  left.append(...leftPorts.map((port) => createClassPreviewPort(port, "left")));
  right.append(...rightPorts.map((port) => createClassPreviewPort(port, "right")));
  body.append(left, right);
  return body;
}

function createClassPreviewPort(port: ClassPreviewPort, side: "left" | "right"): HTMLElement {
  const row = document.createElement("div");
  row.className = `class-preview-port class-preview-port-${side}`;
  const dot = document.createElement("span");
  dot.className = "class-preview-port-dot";
  dot.style.setProperty("--class-preview-port-color", safeCSSColor(port.color, "transparent"));
  const name = textElement("span", port.name, "class-preview-port-name");
  if (side === "left") row.append(dot, name);
  else row.append(name, dot);
  return row;
}

export function createNodeNameForm(name: string): HTMLFormElement {
  const form = document.createElement("form");
  form.className = "node-name-form";
  form.dataset.nodeNameForm = "";

  const label = textElement("label", "Name");
  label.htmlFor = "node-name-input";
  const row = document.createElement("div");
  row.className = "node-name-row";
  const input = document.createElement("input");
  input.id = "node-name-input";
  input.name = "name";
  input.autocomplete = "off";
  input.spellcheck = false;
  input.value = name;
  const button = textElement("button", "Rename");
  button.type = "submit";
  row.append(input, button);
  form.append(label, row);
  return form;
}

export function createKVStoreTitle(store: KVStoreInfo): HTMLElement {
  const title = document.createElement("span");
  title.className = "kv-store-title";
  title.append(
    textElement("strong", store.kind === "plugin" ? store.name ?? "" : "default"),
    textElement("em", store.kind === "plugin" ? "plugin" : "default store"),
  );
  return title;
}

export function createKVEntryTitle(entry: KVEntryInfo): HTMLElement {
  const title = document.createElement("span");
  title.className = "kv-entry-title";
  title.append(textElement("strong", entry.name), textElement("em", formatKVEntryMeta(entry)));
  return title;
}

export function createDNSCacheTitle(cache: DNSCacheInfo): HTMLElement {
  const title = document.createElement("span");
  title.className = "kv-store-title";
  title.append(textElement("strong", cache.name), textElement("em", "DNS cache"));
  return title;
}

export function createDNSKeyTitle(entry: DNSKeyInfo): HTMLElement {
  const title = document.createElement("span");
  title.className = "kv-entry-title";
  const label = textElement("strong", formatDNSCacheKey(entry.key));
  label.title = entry.key;
  title.append(label, textElement("em", formatDNSKeyMeta(entry)));
  return title;
}

export function createSecretTitle(entry: SecretEntry): HTMLElement {
  const title = document.createElement("span");
  title.className = "kv-entry-title sec-secret-title";
  title.append(textElement("strong", entry.key), textElement("em", formatSecretEntryMeta(entry)));
  return title;
}

export function createTLSCertTitle(entry: TLSCertEntry): HTMLElement {
  const title = document.createElement("span");
  title.className = "kv-entry-title tls-cert-title";
  title.append(textElement("strong", entry.name), createTLSCertMeta(entry));
  return title;
}

export function createTLSPolicyTitle(entry: TLSPolicyEntry): HTMLElement {
  const title = document.createElement("span");
  title.className = "kv-entry-title tls-policy-title";
  title.classList.toggle("tls-policy-invalid", !entry.valid);
  title.append(textElement("strong", entry.name), textElement("em", formatTLSPolicyMeta(entry)));
  return title;
}

export function formatKVEntryMeta(entry: KVEntryInfo): string {
  const parts = entry.kind === "bucket" ? ["subtree"] : [entry.string ? "text" : "bytes", formatKVByteSize(entry.size)];
  if (entry.hidden) parts.push("hidden");
  if (entry.owner) parts.push(`owned by ${entry.owner}`);
  return parts.join(" · ");
}

export function formatKVValueMeta(value: KVValue): string {
  const parts = [value.string ? "text" : "bytes", formatKVByteSize(value.size)];
  if (value.hidden) parts.push("hidden");
  if (value.owner) parts.push(`owned by ${value.owner}`);
  return parts.join(" · ");
}

export function formatDNSKeyMeta(entry: DNSKeyInfo): string {
  const parts = [formatDNSByteSize(entry.size)];
  if (entry.expires_at) parts.push(`expires ${entry.expires_at}`);
  return parts.join(" · ");
}

export function formatDNSValueMeta(value: DNSValue): string {
  const parts = [formatDNSByteSize(value.size)];
  if (value.expires_at) parts.push(`expires ${value.expires_at}`);
  return parts.join(" · ");
}

export function formatSecretEntryMeta(entry: SecretEntry): string {
  const nodes = entry.nodes ?? [];
  const parts = [
    formatSecretSource(entry.source),
    `${nodes.length} node${nodes.length === 1 ? "" : "s"}`,
  ];
  if (entry.read_only) parts.push("read-only");
  return parts.join(" · ");
}

// KV values are plugin-controlled data. This helper never parses value bytes as
// markup, refuses to render binary values, caps inline text rendering, and
// covers hidden text until the caller records an explicit reveal action.
export function createKVValuePreview(value: KVValue, options: KVValuePreviewOptions = {}): HTMLElement {
  const preview = document.createElement("div");
  preview.className = "kv-value-preview";
  if (!value.string) {
    preview.append(textElement("p", "Binary value"));
    return preview;
  }

  const limit = options.limit ?? KV_TEXT_RENDER_LIMIT;
  const text = value.data ?? "";
  if ((value.size ?? text.length) > limit || text.length > limit) {
    preview.append(textElement("p", `Text value is ${formatKVByteSize(value.size ?? text.length)}`));
    return preview;
  }

  const pre = textElement("pre", text, "kv-value-text");
  if (value.hidden && !options.revealHidden) {
    const spoiler = document.createElement("div");
    spoiler.className = "kv-value-spoiler";
    spoiler.append(pre, textElement("span", "Hidden", "kv-value-spoiler-label"));
    preview.append(spoiler);
  } else {
    preview.append(pre);
  }
  return preview;
}

// Secret values are intentionally returned by the admin API, but the panel
// still avoids casual disclosure by covering previews until an explicit reveal.
// Values remain plain text throughout and are never parsed as markup.
export function createSecretValuePreview(entry: SecretEntry, options: SecretValuePreviewOptions = {}): HTMLElement {
  const preview = document.createElement("div");
  preview.className = "kv-value-preview";
  const limit = options.limit ?? SECRET_TEXT_RENDER_LIMIT;
  const text = entry.value ?? "";
  if (text.length > limit) {
    preview.append(textElement("p", "Secret value is too large to preview."));
    return preview;
  }

  const pre = textElement("pre", text, "kv-value-text");
  if (!options.reveal) {
    const spoiler = document.createElement("div");
    spoiler.className = "kv-value-spoiler sec-value-spoiler";
    spoiler.append(pre, textElement("span", "Secret", "kv-value-spoiler-label"));
    preview.append(spoiler);
  } else {
    preview.append(pre);
  }
  return preview;
}

export function createTLSCertPreview(entry: TLSCertEntry): HTMLElement {
  const preview = document.createElement("div");
  preview.className = "kv-value-preview tls-cert-preview";
  preview.append(createTLSCertcatSummary(entry.certcat_matches ?? []));
  preview.append(createTLSFactList([
    ["Subject", entry.subject],
    ["Issuer", entry.issuer],
    ["Serial", entry.serial_number],
    ["Fingerprint", entry.fingerprint],
    ["SPKI", entry.spki_fingerprint],
    ["Not before", entry.not_before],
    ["Not after", entry.not_after],
  ]));
  if (entry.pem) preview.append(textElement("pre", entry.pem, "kv-value-text"));
  return preview;
}

export function createTLSPolicyPreview(entry: TLSPolicyEntry): HTMLElement {
  const preview = document.createElement("div");
  preview.className = "kv-value-preview tls-policy-preview";
  preview.append(createTLSPolicySummary(entry));
  preview.append(textElement("pre", formatTLSPolicySpec(entry.spec), "kv-value-text"));
  return preview;
}

function createTLSFactList(facts: Array<[string, string | undefined]>): HTMLElement {
  const list = document.createElement("dl");
  list.className = "tls-fact-list";
  for (const [label, value] of facts) {
    if (!value) continue;
    list.append(textElement("dt", label), textElement("dd", value));
  }
  if (!list.children.length) {
    list.append(textElement("dt", "Details"), textElement("dd", "No certificate metadata available."));
  }
  return list;
}

function createTLSCertMeta(entry: TLSCertEntry): HTMLElement {
  const meta = document.createElement("em");
  appendTLSStatusText(meta, formatTLSCertMeta(entry));
  return meta;
}

function appendTLSStatusText(parent: HTMLElement, text: string): void {
  let lastIndex = 0;
  const pattern = /\b(warning|notice)\b/g;
  for (const match of text.matchAll(pattern)) {
    const index = match.index ?? 0;
    if (index > lastIndex) parent.append(document.createTextNode(text.slice(lastIndex, index)));
    const status = match[1] ?? "";
    parent.append(textElement("span", status, `tls-cert-meta-status tls-cert-meta-status-${status}`));
    lastIndex = index + status.length;
  }
  if (lastIndex < text.length) parent.append(document.createTextNode(text.slice(lastIndex)));
}

function createTLSCertcatSummary(matches: TLSCertcatMatch[]): HTMLElement {
  const block = document.createElement("section");
  block.className = "tls-certcat-section";
  block.append(textElement("h3", "Warnings"));
  if (!matches.length) {
    block.append(textElement("p", "No warnings.", "kv-subtree-empty"));
    return block;
  }

  const list = document.createElement("ol");
  list.className = "tls-certcat-list";
  for (const match of matches) {
    const item = document.createElement("li");
    item.className = `tls-certcat-item tls-certcat-item-${tlsCertcatSeverityClass(match.severity)}`;
    item.append(textElement("strong", formatTLSCertcatMatch(match)));
    item.append(createTLSCertcatDetails(match));
    list.append(item);
  }
  block.append(list);
  return block;
}

function createTLSCertcatDetails(match: TLSCertcatMatch): HTMLElement {
  const list = document.createElement("dl");
  list.className = "tls-certcat-detail-list";
  appendTLSCertcatTextDetail(list, "Recommendation", describeTLSCertcatRecommendation(match));
  appendTLSCertcatAddressDetail(list, "DNS scope", match.scope?.permitted_dns_subtrees ?? []);
  appendTLSCertcatAddressDetail(list, "IP scope", match.scope?.permitted_ip_cidrs ?? []);
  if (match.scope?.requires_explicit_allowlist) {
    appendTLSCertcatTextDetail(list, "Allowlist", "Use only for explicitly allowed addresses.");
  }
  if (match.scope?.reject_ip_targets) {
    appendTLSCertcatTextDetail(list, "IP targets", "Do not use for direct IP targets.");
  }
  appendTLSCertcatTextDetail(list, "Platform", formatTLSCertcatCutoff(match));
  if (match.fingerprint) appendTLSCertcatCodeDetail(list, "Fingerprint", match.fingerprint);
  if (!list.children.length) appendTLSCertcatTextDetail(list, "Recommendation", "Review before you trust this certificate.");
  return list;
}

function appendTLSCertcatTextDetail(list: HTMLElement, label: string, value: string): void {
  if (!value) return;
  list.append(textElement("dt", label), textElement("dd", value));
}

function appendTLSCertcatCodeDetail(list: HTMLElement, label: string, value: string): void {
  if (!value) return;
  const detail = document.createElement("dd");
  detail.append(textElement("code", value));
  list.append(textElement("dt", label), detail);
}

function appendTLSCertcatAddressDetail(list: HTMLElement, label: string, values: string[]): void {
  if (!values.length) return;
  const detail = document.createElement("dd");
  detail.className = "tls-certcat-address-list";
  for (const value of values) detail.append(textElement("code", value));
  list.append(textElement("dt", label), detail);
}

function formatTLSCertcatCutoff(match: TLSCertcatMatch): string {
  const cutoff = match.platform_cutoff;
  if (!cutoff) return "";
  const parts = [
    cutoff.sct_timestamp_after ? `SCT after ${cutoff.sct_timestamp_after}` : "",
    cutoff.enforcement_start ? `starts ${cutoff.enforcement_start}` : "",
    cutoff.first_major_version ? `version ${cutoff.first_major_version}` : "",
  ].filter(Boolean);
  return parts.join(", ");
}

function tlsCertcatSeverityClass(severity: string | undefined): string {
  switch (severity) {
    case "critical":
    case "high":
    case "warning":
    case "info":
      return severity;
    default:
      return "warning";
  }
}

function createTLSPolicySummary(entry: TLSPolicyEntry): HTMLElement {
  const summary = document.createElement("div");
  summary.className = "tls-policy-summary";
  if (!entry.valid && entry.error) {
    summary.append(textElement("p", entry.error, "tls-policy-error"));
  }
  summary.append(
    createTLSPolicyRuleList("Trust anchors", entry.spec.trust_anchors ?? [], (rule) => rule.cert, (rule) => rule.scope),
    createTLSPolicyRuleList("Constraints", entry.spec.constraints ?? [], (rule) => (
      `${rule.cert}${rule.match ? ` (${rule.match})` : ""}`
    ), (rule) => rule.scope),
    createTLSPolicyRuleList("Pins", entry.spec.pins ?? [], (rule) => (
      `${rule.kind} ${rule.fingerprint}`
    ), (rule) => rule.scope),
    createTLSPolicyWatchers(entry.watchers ?? []),
  );
  return summary;
}

function createTLSPolicyRuleList<T>(
  title: string,
  rules: T[],
  label: (rule: T) => string,
  scope: (rule: T) => TLSScope,
): HTMLElement {
  const block = document.createElement("section");
  block.className = "tls-policy-section";
  block.append(textElement("h3", title));
  if (!rules.length) {
    block.append(textElement("p", "None.", "kv-subtree-empty"));
    return block;
  }
  const list = document.createElement("ol");
  list.className = "tls-policy-rule-list";
  for (const rule of rules) {
    const item = document.createElement("li");
    item.append(textElement("strong", label(rule)), textElement("span", describeTLSScope(scope(rule))));
    list.append(item);
  }
  block.append(list);
  return block;
}

function createTLSPolicyWatchers(watchers: string[]): HTMLElement {
  const block = document.createElement("section");
  block.className = "tls-policy-section";
  block.append(textElement("h3", "Watchers"));
  if (!watchers.length) {
    block.append(textElement("p", "None.", "kv-subtree-empty"));
    return block;
  }
  const list = document.createElement("ol");
  list.className = "sec-node-list";
  for (const watcher of watchers) {
    list.append(textElement("li", watcher, "sec-node-chip"));
  }
  block.append(list);
  return block;
}

// DNS values are JSON strings derived from cached upstream DNS messages. Parse
// only the known cache schema into record rows, keep all rendered content as
// text, and fall back to raw JSON text when parsing is not possible.
export function createDNSValuePreview(value: DNSValue, options: DNSValuePreviewOptions = {}): HTMLElement {
  const preview = document.createElement("div");
  preview.className = "kv-value-preview";
  const limit = options.limit ?? DNS_JSON_RENDER_LIMIT;
  const text = value.json ?? "";
  if ((value.size ?? text.length) > limit || text.length > limit) {
    preview.append(textElement("p", `JSON value is ${formatDNSByteSize(value.size ?? text.length)}`));
    return preview;
  }
  const sections = dnsRecordsFromValue(value);
  if (sections) {
    preview.append(createDNSRecordPreview(sections));
    return preview;
  }
  preview.append(textElement("pre", text, "kv-value-text"));
  return preview;
}

function createDNSRecordPreview(sections: DNSRecordSection[]): HTMLElement {
  const preview = document.createElement("div");
  preview.className = "dns-record-preview";
  if (!sections.length) {
    preview.append(textElement("p", "No DNS records in cached response."));
    return preview;
  }
  for (const section of sections) {
    const block = document.createElement("section");
    block.className = "dns-record-section";
    block.append(textElement("h3", section.title));
    const list = document.createElement("ol");
    list.className = "dns-record-list";
    for (const record of section.records) {
      list.append(createDNSRecordItem(record));
    }
    block.append(list);
    preview.append(block);
  }
  return preview;
}

function createDNSRecordItem(record: DNSDisplayRecord): HTMLLIElement {
  const item = document.createElement("li");
  item.className = "dns-record";
  item.append(
    textElement("span", record.name, "dns-record-name"),
    textElement("span", record.ttl === undefined ? "" : String(record.ttl), "dns-record-ttl"),
    textElement("span", record.class, "dns-record-class"),
    textElement("span", record.type, "dns-record-type"),
    textElement("span", record.value ?? "", "dns-record-value"),
  );
  return item;
}

export function createEmptyLogItem(): HTMLLIElement {
  const item = document.createElement("li");
  item.className = "logs-empty";
  item.append(textElement("span", "No cached daemon logs available."));
  return item;
}

export function createLogItem(row: PanelLogEntry): HTMLLIElement {
  const item = document.createElement("li");
  item.className = `log-${row.level}`;
  item.append(
    textElement("strong", formatLogLevel(row.level)),
    iconSeparator(),
    textElement("time", formatLogTime(row.at)),
    iconSeparator(),
    textElement("b", formatLogSource(row)),
    iconSeparator(),
    textElement("span", row.message),
  );
  return item;
}

export function createWorkerNodeBlock(block: WorkerNodeBlock, now: Date): HTMLElement {
  const item = document.createElement("li");
  item.className = "ps-node-block";
  item.dataset.nodeId = String(block.nodeID);
  item.classList.toggle("ps-node-block-primary", Boolean(block.color));
  if (block.color) {
    item.style.setProperty("--ps-primary-color", safeCSSColor(block.color, "#89b4fa"));
    item.style.setProperty("--ps-primary-text-color", safeCSSColor(block.textColor ?? "#ffffff", "#ffffff"));
  }

  const node = block.node;
  const nodeClass = node?.class || block.fallbackClass;
  const nodeName = node?.name || block.fallbackName || "unnamed";
  const meta = [
    node?.placeholder ? "placeholder" : "",
    !node ? "gone" : "",
    `${block.workers.length} worker${block.workers.length === 1 ? "" : "s"}`,
  ].filter(Boolean).join(" · ");

  const header = document.createElement("header");
  header.className = "ps-node-header";
  header.append(
    textElement("strong", `node ${block.nodeID} ${nodeName}`),
    textElement("span", shortClass(nodeClass)),
    textElement("em", meta),
  );

  const workers = document.createElement("ol");
  workers.className = "ps-worker-list";
  for (const worker of block.workers) {
    workers.append(createWorkerItem(worker, now));
  }

  item.append(header, workers);
  return item;
}

export function updateWorkerAgeElements(root: ParentNode, now: Date): void {
  for (const timer of root.querySelectorAll<HTMLTimeElement>(".ps-worker-age")) {
    const startedAt = timer.dataset.startedAt;
    if (!startedAt) continue;
    timer.textContent = `age ${formatWorkerAge(startedAt, now)}`;
  }
}

export function createEmptyWorkerItem(): HTMLLIElement {
  const item = document.createElement("li");
  item.className = "ps-empty";
  item.append(textElement("span", "No workers"));
  return item;
}

export function renderNodeDescription(node: HTMLElement, value: string): void {
  const text = value.trim();
  if (!text) {
    node.textContent = "No long description is registered for this node class.";
    return;
  }
  node.replaceChildren(...parseMarkdownBlocks(text).map(renderMarkdownBlock));
}

type MarkdownBlock =
  | { type: "paragraph"; text: string }
  | { type: "code"; text: string; language: string };

function parseMarkdownBlocks(input: string): MarkdownBlock[] {
  const blocks: MarkdownBlock[] = [];
  const lines = input.split(/\n/);
  let paragraph: string[] = [];
  let code: string[] | null = null;
  let language = "";

  const flushParagraph = () => {
    const text = paragraph.join("\n").trim();
    if (text) blocks.push({ type: "paragraph", text });
    paragraph = [];
  };

  for (const line of lines) {
    const fence = line.match(/^```([A-Za-z0-9_+.-]*)\s*$/);
    if (fence && code === null) {
      flushParagraph();
      code = [];
      language = fence[1] ?? "";
      continue;
    }
    if (fence && code !== null) {
      blocks.push({ type: "code", text: code.join("\n"), language });
      code = null;
      language = "";
      continue;
    }
    if (code !== null) {
      code.push(line);
      continue;
    }
    if (line.trim() === "") {
      flushParagraph();
      continue;
    }
    paragraph.push(line);
  }

  if (code !== null) {
    paragraph.push("```" + language, ...code);
  }
  flushParagraph();
  return blocks;
}

function renderMarkdownBlock(block: MarkdownBlock): HTMLElement {
  if (block.type === "code") return renderMarkdownCodeBlock(block.text, block.language);
  return renderMarkdownParagraph(block.text);
}

function renderMarkdownParagraph(input: string): HTMLParagraphElement {
  const paragraph = document.createElement("p");
  const lines = input.split(/\n/);
  lines.forEach((line, index) => {
    if (index > 0) paragraph.append(document.createElement("br"));
    paragraph.append(renderMarkdownInline(line));
  });
  return paragraph;
}

function renderMarkdownCodeBlock(input: string, language: string): HTMLPreElement {
  const pre = document.createElement("pre");
  const code = textElement("code", input);
  if (language) code.className = `language-${language}`;
  pre.append(code);
  return pre;
}

function renderMarkdownInline(input: string): DocumentFragment {
  const root = document.createDocumentFragment();
  const parts = input.split(/(\[[^\]]+\]\([^)]+\)|\*\*[^*]+\*\*|\*[^*\s][^*]*\*|_[^_\s][^_]*_|`[^`]+`)/g);
  for (const part of parts) {
    if (!part) continue;
    const link = part.match(/^\[([^\]]+)\]\(([^)]+)\)$/);
    if (link) {
      root.append(renderMarkdownLink(link[1] ?? "", link[2] ?? ""));
      continue;
    }
    if (part.startsWith("**") && part.endsWith("**")) {
      root.append(textElement("strong", part.slice(2, -2)));
      continue;
    }
    if (
      (part.startsWith("*") && part.endsWith("*")) ||
      (part.startsWith("_") && part.endsWith("_"))
    ) {
      root.append(textElement("em", part.slice(1, -1)));
      continue;
    }
    if (part.startsWith("`") && part.endsWith("`")) {
      root.append(textElement("code", part.slice(1, -1)));
      continue;
    }
    root.append(document.createTextNode(part));
  }
  return root;
}

function renderMarkdownLink(label: string, href: string): HTMLAnchorElement {
  const anchor = document.createElement("a");
  anchor.textContent = label;
  try {
    const url = new URL(href, window.location.href);
    if (url.protocol === "http:" || url.protocol === "https:" || url.protocol === "mailto:") {
      anchor.href = url.href;
    }
  } catch {
    anchor.removeAttribute("href");
  }
  anchor.rel = "noopener noreferrer";
  return anchor;
}

function formatLogTime(at: string): string {
  const parsed = new Date(at);
  if (Number.isNaN(parsed.getTime())) return at;
  const parts = new Intl.DateTimeFormat("en-US", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).formatToParts(parsed);
  const values = new Map(parts.map((part) => [part.type, part.value]));
  return `${values.get("month") ?? ""}/${values.get("day") ?? ""} ${values.get("hour") ?? ""}:${values.get("minute") ?? ""}:${values.get("second") ?? ""}`.trim();
}

function formatLogLevel(level: string): string {
  switch (level) {
    case "error":
      return "ERR";
    default:
      return level.slice(0, 5).toUpperCase();
  }
}

function formatLogSource(entry: PanelLogEntry): string {
  if (entry.source !== "node") return entry.source;
  if (entry.node_class && entry.node_id) return `${shortClass(entry.node_class)} #${entry.node_id}`;
  if (entry.node_id) return `node #${entry.node_id}`;
  return "node";
}

function createWorkerItem(worker: WorkerSnapshot, now: Date): HTMLLIElement {
  const item = document.createElement("li");
  item.className = "ps-worker";
  if (worker.error) item.classList.add("ps-worker-failed");
  const age = textElement("time", `age ${formatWorkerAge(worker.started_at, now)}`, "ps-worker-age");
  age.dateTime = worker.started_at;
  age.dataset.startedAt = worker.started_at;
  item.append(
    textElement("strong", `worker ${worker.id}`),
    iconSeparator(),
    textElement("span", worker.worker_name || "unnamed"),
    iconSeparator(),
    age,
  );
  if (worker.error) {
    item.append(iconSeparator(), textElement("b", worker.error));
  }
  return item;
}

function formatWorkerAge(startedAt: string, now: Date): string {
  const started = new Date(startedAt);
  if (Number.isNaN(started.getTime())) return "unknown";
  let seconds = Math.max(0, Math.floor((now.getTime() - started.getTime()) / 1000));
  const hours = Math.floor(seconds / 3600);
  seconds -= hours * 3600;
  const minutes = Math.floor(seconds / 60);
  seconds -= minutes * 60;
  if (hours > 0) return `${hours}h${minutes}m${seconds}s`;
  if (minutes > 0) return `${minutes}m${seconds}s`;
  return `${seconds}s`;
}

export function shortClass(name: string): string {
  return name.split("/").pop() ?? name;
}
