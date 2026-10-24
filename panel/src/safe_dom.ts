import {
  DEFAULT_LINK_COLOR,
  typeColor,
  type ID,
  type NodeClassSnapshot,
  type NodeSnapshot,
  type WorkerSnapshot,
} from "./pasta_model.js";
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
import { renderHuJSONHighlight } from "./hujson_highlight.js";
import { text as localizedText } from "./localization.js";
import { renderRuleV1Highlight } from "./rulev1_highlight.js";
import { renderSpaceTableHighlight } from "./spacetable_highlight.js";

export const EXPERIMENTAL_NODE_WARNING = "This node type is experimental and may be removed or significantly changed.";

function t(key: string, builtin: string, parameters: Record<string, string | number> = {}): string {
  return localizedText(key, builtin, parameters);
}

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
    const warningText = t("node.experimental.warning", "Warning: {warning}", {
      warning: t("node.experimental.description", EXPERIMENTAL_NODE_WARNING),
    });
    warning.title = warningText;
    warning.setAttribute("aria-label", warningText);
    title.append(warning);
  }
  copy.append(title, textElement("span", cls.short_description));
  return [copy, createClassPreview(cls, leftPorts, rightPorts)];
}

export function classPreviewPorts(cls: NodeClassSnapshot, side: "left" | "right"): ClassPreviewPort[] {
  return cls.initial_ports
    .filter((port) => classPreviewPortSide(port.direction) === side)
    .map((port) => ({
      name: port.name,
      color: port.types.map(typeColor).find((color) => color !== null)?.color ?? DEFAULT_LINK_COLOR,
    }));
}

function classPreviewPortSide(direction: string): "left" | "right" | null {
  switch (direction.toLowerCase()) {
    case "left":
    case "in":
    case "input":
      return "left";
    case "right":
    case "out":
    case "output":
      return "right";
    default:
      return null;
  }
}

export function createExperimentalNodeWarning(): HTMLElement {
  const warning = textElement("div", t("node.experimental.warning", "Warning: {warning}", {
    warning: t("node.experimental.description", EXPERIMENTAL_NODE_WARNING),
  }), "experimental-node-warning");
  warning.setAttribute("role", "status");
  return warning;
}

export function createClassPreview(
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
  row.dataset.portSide = side;
  row.dataset.portName = port.name;
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

  const label = textElement("label", t("node.name.label", "Name"));
  label.htmlFor = "node-name-input";
  const row = document.createElement("div");
  row.className = "node-name-row";
  const input = document.createElement("input");
  input.id = "node-name-input";
  input.name = "name";
  input.autocomplete = "off";
  input.spellcheck = false;
  input.value = name;
  const button = textElement("button", t("node.name.rename", "Rename"));
  button.type = "submit";
  row.append(input, button);
  form.append(label, row);
  return form;
}

export function createKVStoreTitle(store: KVStoreInfo): HTMLElement {
  const title = document.createElement("span");
  title.className = "kv-store-title";
  title.append(
    textElement("strong", store.kind === "plugin" ? store.name ?? "" : t("kv.store.default.name", "default")),
    textElement("em", store.kind === "plugin" ? t("kv.store.plugin", "plugin") : t("kv.store.default", "default store")),
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
  title.append(textElement("strong", cache.name), textElement("em", t("dns.cache.kind", "DNS cache")));
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
  const parts = entry.kind === "bucket"
    ? [t("kv.meta.subtree", "subtree")]
    : [entry.string ? t("kv.meta.text", "text") : t("kv.meta.bytes", "bytes"), formatKVByteSize(entry.size)];
  if (entry.hidden) parts.push(t("kv.meta.hidden", "hidden"));
  if (entry.owner) parts.push(t("kv.meta.owned_by", "owned by {owner}", { owner: entry.owner }));
  return parts.join(" · ");
}

export function formatKVValueMeta(value: KVValue): string {
  const parts = [value.string ? t("kv.meta.text", "text") : t("kv.meta.bytes", "bytes"), formatKVByteSize(value.size)];
  if (value.hidden) parts.push(t("kv.meta.hidden", "hidden"));
  if (value.owner) parts.push(t("kv.meta.owned_by", "owned by {owner}", { owner: value.owner }));
  return parts.join(" · ");
}

export function formatDNSKeyMeta(entry: DNSKeyInfo): string {
  const parts = [formatDNSByteSize(entry.size)];
  if (entry.expires_at) parts.push(t("dns.meta.expires", "expires {time}", { time: entry.expires_at }));
  return parts.join(" · ");
}

export function formatDNSValueMeta(value: DNSValue): string {
  const parts = [formatDNSByteSize(value.size)];
  if (value.expires_at) parts.push(t("dns.meta.expires", "expires {time}", { time: value.expires_at }));
  return parts.join(" · ");
}

export function formatSecretEntryMeta(entry: SecretEntry): string {
  const nodes = entry.nodes ?? [];
  const parts = [
    formatSecretSource(entry.source),
    t("secrets.meta.nodes", "{count} node(s)", { count: nodes.length }),
  ];
  if (entry.read_only) parts.push(t("secrets.meta.read_only", "read-only"));
  return parts.join(" · ");
}

// KV values are plugin-controlled data. This helper never parses value bytes as
// markup, refuses to render binary values, caps inline text rendering, and
// covers hidden text until the caller records an explicit reveal action.
export function createKVValuePreview(value: KVValue, options: KVValuePreviewOptions = {}): HTMLElement {
  const preview = document.createElement("div");
  preview.className = "kv-value-preview";
  if (!value.string) {
    preview.append(textElement("p", t("kv.value.binary", "Binary value")));
    return preview;
  }

  const limit = options.limit ?? KV_TEXT_RENDER_LIMIT;
  const text = value.data ?? "";
  if ((value.size ?? text.length) > limit || text.length > limit) {
    preview.append(textElement("p", t("kv.value.text_size", "Text value is {size}", { size: formatKVByteSize(value.size ?? text.length) })));
    return preview;
  }

  const pre = textElement("pre", text, "kv-value-text");
  if (value.hidden && !options.revealHidden) {
    const spoiler = document.createElement("div");
    spoiler.className = "kv-value-spoiler";
    spoiler.append(pre, textElement("span", t("kv.value.hidden", "Hidden"), "kv-value-spoiler-label"));
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
    preview.append(textElement("p", t("secrets.value.too_large", "Secret value is too large to preview.")));
    return preview;
  }

  const pre = textElement("pre", text, "kv-value-text");
  if (!options.reveal) {
    const spoiler = document.createElement("div");
    spoiler.className = "kv-value-spoiler sec-value-spoiler";
    spoiler.append(pre, textElement("span", t("secrets.value.hidden", "Secret"), "kv-value-spoiler-label"));
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
  if (entry.revocation_matches?.length) {
    const block = document.createElement("section");
    block.className = "tls-certcat-section";
    block.append(textElement("h3", t("tls.revocation.title", "Revocation warnings")));
    const list = document.createElement("ol");
    list.className = "tls-certcat-list";
    for (const match of entry.revocation_matches) {
      list.append(textElement("li", t("tls.revocation.match", "CRL {source} revoked serial {serial} at {time} (reason {reason})", {
        source: match.source,
        serial: match.serial_number,
        time: match.revocation_time,
        reason: match.reason_code,
      })));
    }
    block.append(list);
    preview.append(block);
  }
  if (entry.distrust_matches?.length) {
    const block = document.createElement("section");
    block.className = "tls-certcat-section";
    block.append(textElement("h3", t("tls.distrust.title", "System distrust warnings")));
    const list = document.createElement("ol");
    list.className = "tls-certcat-list";
    for (const match of entry.distrust_matches) {
      list.append(textElement("li", t("tls.distrust.match", "{source} explicitly distrusts this certificate ({kind}).", {
        source: match.source,
        kind: match.kind,
      })));
    }
    block.append(list);
    preview.append(block);
  }
  preview.append(createTLSFactList([
    [t("tls.field.subject", "Subject"), entry.subject],
    [t("tls.field.issuer", "Issuer"), entry.issuer],
    [t("tls.field.serial", "Serial"), entry.serial_number],
    [t("tls.field.fingerprint", "Fingerprint"), entry.fingerprint],
    ["SPKI", entry.spki_fingerprint],
    [t("tls.field.not_before", "Not before"), entry.not_before],
    [t("tls.field.not_after", "Not after"), entry.not_after],
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
    list.append(textElement("dt", t("tls.field.details", "Details")), textElement("dd", t("tls.certificate.metadata_empty", "No certificate metadata available.")));
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
  block.append(textElement("h3", t("tls.warnings.title", "Warnings")));
  if (!matches.length) {
    block.append(textElement("p", t("tls.warnings.empty", "No warnings."), "kv-subtree-empty"));
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
  appendTLSCertcatTextDetail(list, t("tls.field.recommendation", "Recommendation"), describeTLSCertcatRecommendation(match));
  appendTLSCertcatAddressDetail(list, t("tls.field.dns_scope", "DNS scope"), match.scope?.permitted_dns_subtrees ?? []);
  appendTLSCertcatAddressDetail(list, t("tls.field.ip_scope", "IP scope"), match.scope?.permitted_ip_cidrs ?? []);
  if (match.scope?.requires_explicit_allowlist) {
    appendTLSCertcatTextDetail(list, t("tls.field.allowlist", "Allowlist"), t("tls.certcat.explicit_allowlist", "Use only for explicitly allowed addresses."));
  }
  if (match.scope?.reject_ip_targets) {
    appendTLSCertcatTextDetail(list, t("tls.field.ip_targets", "IP targets"), t("tls.certcat.reject_ip_targets", "Do not use for direct IP targets."));
  }
  appendTLSCertcatTextDetail(list, t("tls.field.platform", "Platform"), formatTLSCertcatCutoff(match));
  if (match.fingerprint) appendTLSCertcatCodeDetail(list, t("tls.field.fingerprint", "Fingerprint"), match.fingerprint);
  if (!list.children.length) appendTLSCertcatTextDetail(list, t("tls.field.recommendation", "Recommendation"), t("tls.certcat.review", "Review before you trust this certificate."));
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
    cutoff.sct_timestamp_after ? t("tls.cutoff.sct_after", "SCT after {time}", { time: cutoff.sct_timestamp_after }) : "",
    cutoff.enforcement_start ? t("tls.cutoff.starts", "starts {time}", { time: cutoff.enforcement_start }) : "",
    cutoff.first_major_version ? t("tls.cutoff.version", "version {version}", { version: cutoff.first_major_version }) : "",
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
    createTLSPolicyRuleList(t("tls.policy.trust_anchors", "Trust anchors"), entry.spec.trust_anchors ?? [], (rule) => rule.cert, (rule) => rule.scope),
    createTLSPolicyRuleList(t("tls.policy.constraints", "Constraints"), entry.spec.constraints ?? [], (rule) => (
      `${rule.cert}${rule.match ? ` (${rule.match})` : ""}`
    ), (rule) => rule.scope),
    createTLSPolicyRuleList(t("tls.policy.pins", "Pins"), entry.spec.pins ?? [], (rule) => (
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
    block.append(textElement("p", t("common.none", "None."), "kv-subtree-empty"));
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
  block.append(textElement("h3", t("tls.policy.watchers", "Watchers")));
  if (!watchers.length) {
    block.append(textElement("p", t("common.none", "None."), "kv-subtree-empty"));
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
    preview.append(textElement("p", t("dns.value.json_size", "JSON value is {size}", { size: formatDNSByteSize(value.size ?? text.length) })));
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
    preview.append(textElement("p", t("dns.value.records_empty", "No DNS records in cached response.")));
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
  item.append(textElement("span", t("logs.empty", "No cached daemon logs available.")));
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
  const nodeName = node?.name || block.fallbackName || t("processes.node.unnamed", "unnamed");
  const meta = [
    node?.placeholder ? t("processes.node.placeholder", "placeholder") : "",
    !node ? t("processes.node.gone", "gone") : "",
    t("processes.node.workers", "{count} worker(s)", { count: block.workers.length }),
  ].filter(Boolean).join(" · ");

  const header = document.createElement("header");
  header.className = "ps-node-header";
  header.append(
    textElement("strong", t("processes.node.title", "node {id} {name}", { id: block.nodeID, name: nodeName })),
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
    timer.textContent = t("processes.worker.age", "age {age}", { age: formatWorkerAge(startedAt, now) });
  }
}

export function createEmptyWorkerItem(): HTMLLIElement {
  const item = document.createElement("li");
  item.className = "ps-empty";
  item.append(textElement("span", t("processes.empty", "No workers")));
  return item;
}

export type MarkdownRenderMode = "panel" | "docs";

export type MarkdownPortHighlightHandler = (
  side: "left" | "right",
  name: string,
  highlighted: boolean,
) => void;

type MarkdownRenderContext = {
  mode: MarkdownRenderMode;
  documentID: string;
  onPortHighlight: MarkdownPortHighlightHandler | undefined;
  documentationURL: ((id: string) => string) | undefined;
};

export function renderNodeDescription(
  node: HTMLElement,
  value: string,
  mode: MarkdownRenderMode = "panel",
  documentID = "",
  onPortHighlight?: MarkdownPortHighlightHandler,
  documentationURL?: (id: string) => string,
): void {
  const text = value.trim();
  if (!text) {
    node.textContent = t("node.description.empty", "No long description is registered for this node class.");
    return;
  }
  const context: MarkdownRenderContext = { mode, documentID, onPortHighlight, documentationURL };
  const headingIDs = new Map<string, number>();
  node.replaceChildren(...parseMarkdownBlocks(text).map((block) => renderMarkdownBlock(block, context, headingIDs)));
}

type MarkdownBlock =
  | { type: "paragraph"; text: string }
  | { type: "code"; text: string; language: string }
  | { type: "heading"; text: string; level: number }
  | { type: "list"; items: string[]; ordered: boolean; start: number }
  | { type: "alert"; kind: MarkdownAlertKind; blocks: MarkdownBlock[] };

type MarkdownAlertKind = "note" | "tip" | "important" | "warning" | "caution";

function parseMarkdownBlocks(input: string): MarkdownBlock[] {
  const blocks: MarkdownBlock[] = [];
  const lines = input.replace(/\r\n?/g, "\n").split("\n");
  let paragraph: string[] = [];
  let code: string[] | null = null;
  let language = "";
  let list: Extract<MarkdownBlock, { type: "list" }> | null = null;

  const flushParagraph = () => {
    const text = paragraph.join("\n").trim();
    if (text) blocks.push({ type: "paragraph", text });
    paragraph = [];
  };
  const flushList = () => {
    if (list) blocks.push(list);
    list = null;
  };

  for (let lineIndex = 0; lineIndex < lines.length; lineIndex += 1) {
    const line = lines[lineIndex] ?? "";
    const fence = line.match(/^ {0,3}```([A-Za-z0-9_+.-]*)\s*$/);
    if (fence && code === null) {
      flushParagraph();
      flushList();
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
      flushList();
      continue;
    }

    const alert = line.match(/^ {0,3}>[ \t]?\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\][ \t]*$/);
    if (alert) {
      flushParagraph();
      flushList();
      const body: string[] = [];
      while (lineIndex + 1 < lines.length) {
        const quoted = (lines[lineIndex + 1] ?? "").match(/^ {0,3}>[ \t]?(.*)$/);
        if (!quoted) break;
        body.push(quoted[1] ?? "");
        lineIndex += 1;
      }
      blocks.push({
        type: "alert",
        kind: (alert[1] ?? "").toLowerCase() as MarkdownAlertKind,
        blocks: parseMarkdownBlocks(body.join("\n")),
      });
      continue;
    }

    const heading = line.match(/^ {0,3}(#{1,6})(?:[ \t]+(.*?)|[ \t]*)$/);
    if (heading) {
      flushParagraph();
      flushList();
      const text = (heading[2] ?? "").replace(/[ \t]+#+[ \t]*$/, "").trim();
      blocks.push({ type: "heading", text, level: heading[1]?.length ?? 1 });
      continue;
    }

    const setext = line.match(/^ {0,3}(=+|-{2,})[ \t]*$/);
    if (setext && paragraph.length > 0 && list === null) {
      const text = paragraph.join("\n").trim();
      paragraph = [];
      blocks.push({ type: "heading", text, level: setext[1]?.startsWith("=") ? 1 : 2 });
      continue;
    }

    const listItem = line.match(/^ {0,3}(?:-(?:[ \t]+(.*)|[ \t]*)|(\d{1,9})[.)](?:[ \t]+(.*)|[ \t]*))$/);
    if (listItem) {
      const ordered = listItem[2] !== undefined;
      const start = ordered ? Number(listItem[2]) : 1;
      const item = (ordered ? listItem[3] : listItem[1]) ?? "";
      if (paragraph.length > 0 && (item === "" || (ordered && start !== 1))) {
        paragraph.push(line);
        continue;
      }
      flushParagraph();
      if (list && list.ordered !== ordered) flushList();
      if (!list) list = { type: "list", items: [], ordered, start };
      list.items.push(item);
      continue;
    }

    if (list && list.items.length > 0) {
      const index = list.items.length - 1;
      list.items[index] = `${list.items[index] ?? ""}\n${line.replace(/^[ \t]+/, "")}`;
      continue;
    }
    paragraph.push(line);
  }

  if (code !== null) {
    paragraph.push("```" + language, ...code);
  }
  flushParagraph();
  flushList();
  return blocks;
}

function renderMarkdownBlock(
  block: MarkdownBlock,
  context: MarkdownRenderContext,
  headingIDs: Map<string, number>,
): HTMLElement {
  if (block.type === "code") return renderMarkdownCodeBlock(block.text, block.language);
  if (block.type === "list") return renderMarkdownList(block, context);
  if (block.type === "alert") return renderMarkdownAlert(block, context, headingIDs);
  if (block.type === "heading") return renderMarkdownHeading(block.text, block.level, context, headingIDs);
  return renderMarkdownParagraph(block.text, context);
}

function renderMarkdownAlert(
  block: Extract<MarkdownBlock, { type: "alert" }>,
  context: MarkdownRenderContext,
  headingIDs: Map<string, number>,
): HTMLElement {
  const alert = document.createElement("aside");
  alert.className = `markdown-alert markdown-alert-${block.kind}`;
  alert.setAttribute("aria-label", block.kind);
  const title = textElement("p", block.kind.toUpperCase(), "markdown-alert-title");
  alert.append(title, ...block.blocks.map((child) => renderMarkdownBlock(child, context, headingIDs)));
  return alert;
}

function renderMarkdownParagraph(input: string, context: MarkdownRenderContext): HTMLParagraphElement {
  const paragraph = document.createElement("p");
  paragraph.append(renderMarkdownInline(normalizeMarkdownInlineBreaks(input), context));
  return paragraph;
}

function normalizeMarkdownInlineBreaks(input: string): string {
  const lines = input.split(/\r?\n/);
  let output = "";
  lines.forEach((line, index) => {
    const trailingBackslashes = line.match(/\\+$/)?.[0].length ?? 0;
    let content = line.replace(/[ \t]+$/, "");
    if (index < lines.length - 1 && trailingBackslashes % 2 === 1) content = content.slice(0, -1);
    if (index > 0) output += hardBreakForPreviousLine(lines[index - 1] ?? "") ? "\n" : " ";
    output += index > 0 ? content.replace(/^[ \t]+/, "") : content;
  });
  return output;
}

function hardBreakForPreviousLine(line: string): boolean {
  const trailingSpaces = line.match(/ +$/)?.[0].length ?? 0;
  const trailingBackslashes = line.match(/\\+$/)?.[0].length ?? 0;
  return trailingSpaces >= 2 || trailingBackslashes % 2 === 1;
}

function renderMarkdownCodeBlock(input: string, language: string): HTMLPreElement {
  const pre = document.createElement("pre");
  const code = textElement("code", input);
  if (language) code.className = `language-${language}`;
  if (language === "json" || language === "hjson" || language === "hujson") {
    renderHuJSONHighlight(code, input);
  } else if (language === "rulev1") {
    renderRuleV1Highlight(code, input);
  } else if (language === "spacetable") {
    renderSpaceTableHighlight(code, input);
  }
  pre.append(code);
  return pre;
}

function renderMarkdownList(
  block: Extract<MarkdownBlock, { type: "list" }>,
  context: MarkdownRenderContext,
): HTMLOListElement | HTMLUListElement {
  const list = document.createElement(block.ordered ? "ol" : "ul");
  if (block.ordered && block.start !== 1) list.setAttribute("start", String(block.start));
  for (const item of block.items) {
    const element = document.createElement("li");
    element.append(renderMarkdownInline(normalizeMarkdownInlineBreaks(item), context));
    list.append(element);
  }
  return list;
}

function renderMarkdownHeading(
  input: string,
  level: number,
  context: MarkdownRenderContext,
  headingIDs: Map<string, number>,
): HTMLElement {
  if (context.mode === "panel") {
    const paragraph = document.createElement("p");
    const strong = document.createElement("strong");
    strong.append(renderMarkdownInline(normalizeMarkdownInlineBreaks(input), context));
    paragraph.append(strong);
    return paragraph;
  }

  const heading = document.createElement(`h${level}`);
  heading.append(renderMarkdownInline(normalizeMarkdownInlineBreaks(input), context));
  const baseID = markdownHeadingID(heading.textContent ?? "");
  const duplicateIndex = headingIDs.get(baseID) ?? 0;
  headingIDs.set(baseID, duplicateIndex + 1);
  heading.id = duplicateIndex === 0 ? baseID : `${baseID}-${duplicateIndex}`;
  return heading;
}

function markdownHeadingID(input: string): string {
  return input
    .normalize("NFKD")
    .toLowerCase()
    .replace(/[^\p{Letter}\p{Number}\s-]/gu, "")
    .trim()
    .replace(/[\s-]+/g, "-") || "section";
}

function renderMarkdownInline(input: string, context: MarkdownRenderContext): DocumentFragment {
  const root = document.createDocumentFragment();
  const parts = input.split(/(!?\[[^\]\n]*\]\([^\n]*?\)|\*\*[^*]+\*\*|\*[^*\s][^*]*\*|_[^_\s][^_]*_|`[^`]+`)/g);
  for (const part of parts) {
    if (!part) continue;
    const reference = parseMarkdownInlineReference(part);
    if (reference?.image) {
      root.append(renderMarkdownImage(reference.label, reference.destination, reference.title));
      continue;
    }
    if (reference) {
      root.append(renderMarkdownLink(reference.label, reference.destination, reference.title, context));
      continue;
    }
    if (part.startsWith("**") && part.endsWith("**")) {
      root.append(markdownInlineElement("strong", part.slice(2, -2)));
      continue;
    }
    if (
      (part.startsWith("*") && part.endsWith("*")) ||
      (part.startsWith("_") && part.endsWith("_"))
    ) {
      root.append(markdownInlineElement("em", part.slice(1, -1)));
      continue;
    }
    if (part.startsWith("`") && part.endsWith("`")) {
      root.append(markdownInlineElement("code", part.slice(1, -1)));
      continue;
    }
    root.append(...markdownInlineText(part));
  }
  return root;
}

type MarkdownInlineReference = {
  image: boolean;
  label: string;
  destination: string;
  title: string;
};

function parseMarkdownInlineReference(input: string): MarkdownInlineReference | null {
  const match = input.match(
    /^(!?)\[([^\]]*)\]\([ \t]*(?:<([^<>\n]*)>|([^ \t\n]+?))(?:[ \t]+(?:"([^"\n]*)"|'([^'\n]*)'|\(([^)\n]*)\)))?[ \t]*\)$/,
  );
  if (!match) return null;
  return {
    image: match[1] === "!",
    label: match[2] ?? "",
    destination: match[3] ?? match[4] ?? "",
    title: match[5] ?? match[6] ?? match[7] ?? "",
  };
}

function markdownInlineElement(tag: "strong" | "em" | "code", text: string): HTMLElement {
  const element = document.createElement(tag);
  element.append(...markdownInlineText(text));
  return element;
}

function markdownInlineText(text: string): Node[] {
  const nodes: Node[] = [];
  text.split("\n").forEach((part, index) => {
    if (index > 0) nodes.push(document.createElement("br"));
    nodes.push(document.createTextNode(part));
  });
  return nodes;
}

type MarkdownCustomLinkRenderer = (
  label: string,
  destination: string,
  title: string,
  context: MarkdownRenderContext,
) => Node;

const markdownCustomLinkRenderers: Readonly<Record<string, MarkdownCustomLinkRenderer>> = {
  docs: renderMarkdownDocumentationLink,
  port: renderMarkdownPortLink,
};

function renderMarkdownLink(
  label: string,
  href: string,
  title: string,
  context: MarkdownRenderContext,
): Node {
  const custom = href.match(/^@([a-z][a-z0-9-]*)\/(.*)$/);
  if (custom) {
    const renderer = markdownCustomLinkRenderers[custom[1] ?? ""];
    if (renderer) return renderer(label, custom[2] ?? "", title, context);
    return document.createTextNode(label);
  }

  const anchor = document.createElement("a");
  anchor.append(...markdownInlineText(label));
  const url = safeMarkdownURL(href, true);
  if (url) anchor.href = url;
  if (title) anchor.title = title;
  anchor.rel = "noopener noreferrer";
  return anchor;
}

function renderMarkdownDocumentationLink(
  label: string,
  destination: string,
  title: string,
  context: MarkdownRenderContext,
): Node {
  if (!validMarkdownDocumentationID(destination)) return document.createTextNode(label);

  const anchor = document.createElement("a");
  anchor.append(...markdownInlineText(label));
  anchor.className = "markdown-link-docs";
  anchor.href = context.documentationURL
    ? context.documentationURL(destination)
    : context.mode === "docs"
    ? relativeMarkdownDocumentationURL(context.documentID, destination)
    : `./docs/${encodeMarkdownDocumentationID(destination)}`;
  if (context.mode === "panel") anchor.target = "_blank";
  if (title) anchor.title = title;
  anchor.rel = "noopener noreferrer";
  return anchor;
}

function renderMarkdownPortLink(
  label: string,
  destination: string,
  title: string,
  context: MarkdownRenderContext,
): Node {
  const match = destination.match(/^(left|right)\/([^\u0000-\u001f\u007f]{1,256})$/);
  if (!match) return document.createTextNode(label);

  const side = match[1] as "left" | "right";
  const name = match[2] ?? "";
  const strong = document.createElement("strong");
  strong.append(...markdownInlineText(label));
  strong.className = "markdown-link-port";
  strong.dataset.portSide = side;
  strong.dataset.portName = name;
  if (title) strong.title = title;
  if (context.onPortHighlight) {
    strong.addEventListener("pointerenter", () => context.onPortHighlight?.(side, name, true));
    strong.addEventListener("pointerleave", () => context.onPortHighlight?.(side, name, false));
  }
  return strong;
}

function validMarkdownDocumentationID(value: string): boolean {
  return value.length <= 256
    && /^[A-Za-z0-9][A-Za-z0-9_.:/-]*$/.test(value)
    && !value.includes("..");
}

function encodeMarkdownDocumentationID(value: string): string {
  return value.split("/").map(encodeURIComponent).join("/");
}

function relativeMarkdownDocumentationURL(current: string, destination: string): string {
  const from = current.split("/").slice(0, -1);
  const to = destination.split("/");
  while (from.length > 0 && to.length > 0 && from[0] === to[0]) {
    from.shift();
    to.shift();
  }
  const relative = [...from.map(() => ".."), ...to.map(encodeURIComponent)].join("/");
  return relative.startsWith("../") ? relative : `./${relative}`;
}

function renderMarkdownImage(alt: string, src: string, title: string): HTMLImageElement {
  const image = document.createElement("img");
  image.alt = alt;
  const url = safeMarkdownURL(src, false);
  if (url) image.src = url;
  if (title) image.title = title;
  image.loading = "lazy";
  return image;
}

function safeMarkdownURL(input: string, allowMailto: boolean): string | null {
  try {
    const url = new URL(input, window.location.href);
    if (url.protocol === "http:" || url.protocol === "https:" || (allowMailto && url.protocol === "mailto:")) {
      return url.href;
    }
  } catch {
    // Leave the URL attribute unset when the input is not a valid safe URL.
  }
  return null;
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
  item.className = `ps-worker ps-worker-${worker.kind}`;
  item.title = worker.kind === "sandbox"
    ? t("processes.worker.sandbox_hint", "Sandbox process")
    : t("processes.worker.go_hint", "Built-in Go worker");
  if (worker.error) item.classList.add("ps-worker-failed");
  const age = textElement("time", t("processes.worker.age", "age {age}", { age: formatWorkerAge(worker.started_at, now) }), "ps-worker-age");
  age.dateTime = worker.started_at;
  age.dataset.startedAt = worker.started_at;
  item.append(
    textElement("strong", t("processes.worker.title", "worker {id}", { id: worker.id })),
    iconSeparator(),
    textElement("span", worker.worker_name || t("processes.node.unnamed", "unnamed")),
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
  if (Number.isNaN(started.getTime())) return t("common.unknown", "unknown");
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
