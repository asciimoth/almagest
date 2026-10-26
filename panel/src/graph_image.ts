import {
  renderGraphPreviewCanvas,
  type GraphPreviewClass,
  type GraphPreviewRenderOptions,
} from "./graph_preview.js";
import { parseNodeClassSnapshot, parseTypePaletteMap } from "./pasta_model.js";

type RenderInput = {
  source: string;
  options: GraphPreviewRenderOptions;
};

type NodeClassCache = {
  colormap: Record<string, import("./pasta_model.js").TypePalette>;
  classes: Record<string, unknown>;
};

const host = document.querySelector<HTMLElement>("#graph");
if (!host) throw new Error("Graph image host is missing");

try {
  const [input, cache] = await Promise.all([loadInput(), loadNodeClasses()]);
  await renderGraphPreviewCanvas(host, input.source, async (className) => classInfo(cache, className), input.options);
  document.documentElement.dataset.renderState = "complete";
  await fetch("/done", { method: "POST" });
} catch (error) {
  const message = error instanceof Error ? error.message : String(error);
  document.documentElement.dataset.renderState = "error";
  document.body.textContent = `Graph image rendering failed: ${message}`;
  await fetch(`/done?error=${encodeURIComponent(message)}`, { method: "POST" });
}

async function loadInput(): Promise<RenderInput> {
  const value = await fetchJSON("/input");
  if (!isRecord(value) || typeof value.source !== "string" || !isRecord(value.options)) {
    throw new Error("Invalid graph image input");
  }
  return { source: value.source, options: value.options as GraphPreviewRenderOptions };
}

async function loadNodeClasses(): Promise<NodeClassCache> {
  const value = await fetchJSON("/node-classes");
  if (!isRecord(value) || value.version !== 1 || !isRecord(value.classes)) {
    throw new Error("Invalid node class cache");
  }
  const colormap = parseTypePaletteMap(value.colormap);
  if (!colormap) throw new Error("Invalid node class color map");
  return { colormap, classes: value.classes };
}

function classInfo(cache: NodeClassCache, className: string): GraphPreviewClass | null {
  const value = cache.classes[className];
  if (!isRecord(value)) return null;
  const snapshot = parseNodeClassSnapshot({
    class: className,
    short_description: "",
    unique: false,
    primary_type: value.primary_type,
    initial_ports: value.initial_ports,
    experimental: false,
  });
  return snapshot ? { snapshot, colormap: cache.colormap } : null;
}

async function fetchJSON(path: string): Promise<unknown> {
  const response = await fetch(path);
  if (!response.ok) throw new Error(`${path} returned ${response.status}`);
  return await response.json() as unknown;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === "object" && !Array.isArray(value);
}
