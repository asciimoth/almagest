import type { ID, NodeClassSnapshot, NodeSnapshot, WorkerSnapshot } from "./pasta_model.js";
import { KV_TEXT_RENDER_LIMIT, formatKVByteSize, type KVEntryInfo, type KVStoreInfo, type KVValue } from "./kv_model.js";

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
  copy.append(textElement("strong", shortClass(cls.class)), textElement("span", cls.short_description));
  return [copy, createClassPreview(cls, leftPorts, rightPorts)];
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
  dot.style.setProperty("--class-preview-port-color", port.color);
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

export function createNotificationToast(message: string): HTMLDivElement {
  const toast = document.createElement("div");
  toast.className = "notification notification-error";
  const dismiss = textElement("button", "x");
  dismiss.type = "button";
  dismiss.setAttribute("aria-label", "Dismiss notification");
  toast.append(textElement("strong", "Config save failed"), textElement("span", message), dismiss);
  return toast;
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

export function formatKVEntryMeta(entry: KVEntryInfo): string {
  if (entry.kind === "bucket") return "subtree";
  const parts = [entry.string ? "text" : "bytes", formatKVByteSize(entry.size)];
  if (entry.hidden) parts.push("hidden");
  return parts.join(" · ");
}

export function formatKVValueMeta(value: KVValue): string {
  const parts = [value.string ? "text" : "bytes", formatKVByteSize(value.size)];
  if (value.hidden) parts.push("hidden");
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
    item.style.setProperty("--ps-primary-color", block.color);
    item.style.setProperty("--ps-primary-text-color", block.textColor ?? "#ffffff");
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
      root.append(renderMarkdownLink(link[1], link[2]));
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
