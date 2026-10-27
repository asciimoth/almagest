import {
  fitGraphRectangles,
  type GraphRectangle,
  type GraphViewportPadding,
} from "./graph_viewport.js";
import {
  DEFAULT_LINK_COLOR,
  DEFAULT_NODE_TEXT_COLOR,
  DEFAULT_NODE_TITLE_COLOR,
  parsePosition,
  type NodeClassSnapshot,
  type TypePalette,
} from "./pasta_model.js";

declare const LiteGraph: any;
declare const LGraph: any;
declare const LGraphCanvas: any;

const PREVIEW_NODE_TYPE = "almagest/docs-preview";
const MAX_SOURCE_LENGTH = 256 * 1024;
const MAX_NODES = 256;
const MAX_LINKS = 1024;
const MAX_TEXT_LENGTH = 4096;

export type GraphPreviewNode = {
  name: string;
  className: string;
  primaryType: string;
  position: [number, number];
  label: string;
  links: GraphPreviewLink[];
};

export type GraphPreviewLink = {
  sourceNode: string;
  sourcePort: string;
  targetNode: string;
  targetPort: string;
};

export type GraphPreviewConfig = { nodes: GraphPreviewNode[] };

export type GraphPreviewClass = {
  snapshot: NodeClassSnapshot;
  colormap: Record<string, TypePalette>;
};

export type GraphPreviewClassResolver = (className: string) => Promise<GraphPreviewClass | null>;

export type GraphPreviewRenderOptions = {
  width?: number;
  height?: number;
  zoom?: number;
  padding?: number | Partial<GraphViewportPadding>;
};

type PreviewPort = { name: string; types: string[] };
type PreviewNodePorts = { left: PreviewPort[]; right: PreviewPort[] };

/** Parse the HuJSON-compatible node-map syntax accepted by graph fences. */
export function parseGraphPreviewConfig(source: string): GraphPreviewConfig {
  if (source.length > MAX_SOURCE_LENGTH) throw new Error("Graph preview source is too large");
  const parsed = JSON.parse(stripHuJSONExtensions(source)) as unknown;
  const root = record(parsed, "Graph preview must be an object");
  const rawNodes = Object.hasOwn(root, "nodes") ? record(root.nodes, "nodes must be an object") : root;
  const entries = Object.entries(rawNodes);
  if (entries.length > MAX_NODES) throw new Error(`Graph preview has more than ${MAX_NODES} nodes`);

  let linkCount = 0;
  const nodes = entries.map(([name, rawNode], index): GraphPreviewNode => {
    if (!name || name.includes("[") || name.includes("]") || name.length > MAX_TEXT_LENGTH) {
      throw new Error("Graph preview contains an invalid node name");
    }
    const value = record(rawNode, `Node ${name} must be an object`);
    const className = requiredText(value.Class, `Node ${name} Class must be a non-empty string`);
    if (value.Primary !== undefined && typeof value.Primary !== "string") {
      throw new Error(`Node ${name} Primary must be a string`);
    }
    if (value.Pos !== undefined && typeof value.Pos !== "string") {
      throw new Error(`Node ${name} Pos must be a string`);
    }
    const position = typeof value.Pos === "string"
      ? parsePosition(value.Pos, fallbackPosition(index)[0], fallbackPosition(index)[1])
      : fallbackPosition(index);
    const labelValue = value.Label ?? value.label;
    if (labelValue !== undefined && typeof labelValue !== "string") {
      throw new Error(`Node ${name} Label must be a string`);
    }
    const rawLinks = value.Links ?? [];
    if (!Array.isArray(rawLinks) || !rawLinks.every((item) => typeof item === "string")) {
      throw new Error(`Node ${name} Links must be an array of strings`);
    }
    linkCount += rawLinks.length;
    if (linkCount > MAX_LINKS) throw new Error(`Graph preview has more than ${MAX_LINKS} links`);
    return {
      name,
      className,
      primaryType: boundedText(value.Primary ?? "", `Node ${name} Primary is too long`),
      position,
      label: boundedText(labelValue ?? "", `Node ${name} Label is too long`),
      links: rawLinks.map((link) => parseGraphLink(name, link)),
    };
  });

  const names = new Set(nodes.map((node) => node.name));
  for (const node of nodes) {
    for (const link of node.links) {
      if (!names.has(link.targetNode)) throw new Error(`Link target ${link.targetNode} does not exist`);
    }
  }
  return { nodes };
}

/** Render one inert LiteGraph frame, then detach the graph and canvas. */
export async function renderGraphPreview(
  host: HTMLElement,
  source: string,
  resolveClass: GraphPreviewClassResolver,
  options: GraphPreviewRenderOptions = {},
): Promise<void> {
  try {
    await renderGraphPreviewCanvas(host, source, resolveClass, options);
  } catch (error) {
    const message = document.createElement("p");
    message.className = "graph-preview-error";
    message.textContent = `Graph preview unavailable: ${error instanceof Error ? error.message : String(error)}`;
    host.replaceChildren(message);
  }
}

/** Render one graph frame and reject when the input cannot be rendered. */
export async function renderGraphPreviewCanvas(
  host: HTMLElement,
  source: string,
  resolveClass: GraphPreviewClassResolver,
  options: GraphPreviewRenderOptions = {},
): Promise<HTMLCanvasElement> {
  validateRenderOptions(options);
  const config = parseGraphPreviewConfig(source);
  if (!config.nodes.length) throw new Error("Graph preview has no nodes");
  const classNames = [...new Set(config.nodes.map((node) => node.className))];
  const resolved = await Promise.all(classNames.map(async (className) => {
    try {
      return [className, await resolveClass(className)] as const;
    } catch {
      return [className, null] as const;
    }
  }));
  return drawGraphPreview(host, config, new Map(resolved), options);
}

export function graphPreviewPorts(
  config: GraphPreviewConfig,
  classes: ReadonlyMap<string, GraphPreviewClass | null>,
): Map<string, PreviewNodePorts> {
  const ports = new Map<string, PreviewNodePorts>();
  for (const node of config.nodes) {
    const value: PreviewNodePorts = { left: [], right: [] };
    for (const port of classes.get(node.className)?.snapshot.initial_ports ?? []) {
      const side = portSide(port.direction);
      if (side) addPreviewPort(value[side], port.name, port.types);
    }
    ports.set(node.name, value);
  }
  for (const node of config.nodes) {
    for (const link of node.links) {
      const source = ports.get(link.sourceNode);
      const target = ports.get(link.targetNode);
      if (!source || !target) continue;
      const sourceTypes = source.right.find((port) => port.name === link.sourcePort)?.types ?? [];
      const targetTypes = target.left.find((port) => port.name === link.targetPort)?.types ?? [];
      addPreviewPort(source.right, link.sourcePort, sourceTypes.length ? sourceTypes : targetTypes);
      addPreviewPort(target.left, link.targetPort, targetTypes.length ? targetTypes : sourceTypes);
    }
  }

  // LiteGraph permits only one connection on an input slot. Almagest ports do
  // not have that restriction, so give each incoming link its own visual slot.
  // Keep at least one slot for an input with no links, as the graph tab does.
  const incomingLinks = countIncomingLinks(config);
  for (const [nodeName, value] of ports) {
    value.left = value.left.flatMap((port) => Array.from(
      { length: Math.max(1, incomingLinks.get(nodeName)?.get(port.name) ?? 0) },
      () => ({ name: port.name, types: [...port.types] }),
    ));
  }
  return ports;
}

function drawGraphPreview(
  host: HTMLElement,
  config: GraphPreviewConfig,
  classes: ReadonlyMap<string, GraphPreviewClass | null>,
  options: GraphPreviewRenderOptions,
): HTMLCanvasElement {
  LiteGraph.allow_scripts = false;
  registerPreviewNode();
  const graph = new LGraph();
  const nodePorts = graphPreviewPorts(config, classes);
  const graphNodes = new Map<string, any>();

  for (const model of config.nodes) {
    const classInfo = classes.get(model.className) ?? null;
    const node = LiteGraph.createNode(PREVIEW_NODE_TYPE);
    node.title = model.name;
    node.pos = [...model.position];
    node.properties.previewLabel = model.label;
    const palette = paletteForType(classInfo?.colormap, model.primaryType || classInfo?.snapshot.primary_type || "");
    node.color = palette?.type ?? palette?.color ?? DEFAULT_NODE_TITLE_COLOR;
    node.bgcolor = "#1e1e2e";
    node.boxcolor = classInfo ? "#45475a" : "#f38ba8";
    const inputOccurrences = new Map<string, number>();
    for (const port of nodePorts.get(model.name)?.left ?? []) {
      const occurrence = inputOccurrences.get(port.name) ?? 0;
      addLiteGraphPort(node, port, "left", classInfo?.colormap, occurrence);
      inputOccurrences.set(port.name, occurrence + 1);
    }
    for (const port of nodePorts.get(model.name)?.right ?? []) addLiteGraphPort(node, port, "right", classInfo?.colormap);
    node.size = node.computeSize();
    fitPreviewLabel(node, model.label);
    graph.add(node);
    graphNodes.set(model.name, node);
  }

  const connectedInputs = new Map<string, Map<string, number>>();
  for (const model of config.nodes) {
    for (const link of model.links) {
      const source = graphNodes.get(link.sourceNode);
      const target = graphNodes.get(link.targetNode);
      const output = findSlot(source?.outputs, link.sourcePort);
      const occurrence = takeNextInputOccurrence(connectedInputs, link.targetNode, link.targetPort);
      const input = findSlot(target?.inputs, link.targetPort, occurrence);
      if (source && target && output >= 0 && input >= 0) source.connect(output, target, input);
    }
  }

  const canvasElement = document.createElement("canvas");
  canvasElement.className = "graph-preview-canvas";
  canvasElement.setAttribute("aria-label", "Node graph preview");
  canvasElement.setAttribute("role", "img");
  host.replaceChildren(canvasElement);
  const width = options.width === undefined
    ? Math.max(320, Math.round(host.getBoundingClientRect().width || host.clientWidth || 900))
    : positiveDimension(options.width, "Graph preview width");
  const height = options.height === undefined
    ? Math.min(520, Math.max(260, Math.round(width * 0.5)))
    : positiveDimension(options.height, "Graph preview height");
  canvasElement.width = width;
  canvasElement.height = height;

  const canvas = new LGraphCanvas(canvasElement, graph, { skip_render: true, skip_events: true });
  canvas.clear_background_color = "#11111b";
  canvas.background_image = null;
  canvas.render_canvas_border = false;
  canvas.show_info = false;
  canvas.node_title_color = DEFAULT_NODE_TEXT_COLOR;
  canvas.allow_interaction = false;
  centerPreview(canvas, graph, width, height, options);
  canvas.draw(true, true);
  canvas.stopRendering();
  canvas.setGraph(null, true);
  graph.stop();
  return canvasElement;
}

function registerPreviewNode(): void {
  if (LiteGraph.registered_node_types?.[PREVIEW_NODE_TYPE]) return;
  function PreviewNode(this: any) {
    this.properties = { previewLabel: "" };
    this.resizable = false;
    this.clonable = false;
    this.removable = false;
  }
  PreviewNode.title = "Preview";
  PreviewNode.prototype.onDrawForeground = function (ctx: CanvasRenderingContext2D): void {
    const label = this.properties?.previewLabel;
    if (!label || this.flags?.collapsed) return;
    ctx.save();
    ctx.font = "12px Arial";
    ctx.fillStyle = DEFAULT_NODE_TEXT_COLOR;
    ctx.textAlign = "center";
    ctx.textBaseline = "bottom";
    ctx.fillText(String(label), this.size[0] * 0.5, this.size[1] - 7, this.size[0] - 18);
    ctx.restore();
  };
  LiteGraph.registerNodeType(PREVIEW_NODE_TYPE, PreviewNode);
}

function centerPreview(
  canvas: any,
  graph: any,
  width: number,
  height: number,
  options: GraphPreviewRenderOptions,
): void {
  const rectangles = (graph._nodes as any[]).map((node): GraphRectangle => {
    const bounds = node.getBounding(undefined, true) as ArrayLike<number>;
    return [bounds[0] ?? 0, bounds[1] ?? 0, bounds[2] ?? 0, bounds[3] ?? 0];
  });
  const padding = previewPadding(options.padding);
  const zoom = options.zoom === undefined ? undefined : positiveNumber(options.zoom, "Graph preview zoom");
  const viewport = fitGraphRectangles(rectangles, width, height, padding, 1, zoom);
  if (!viewport) return;
  canvas.ds.scale = viewport.scale;
  canvas.ds.offset[0] = viewport.offset[0];
  canvas.ds.offset[1] = viewport.offset[1];
}

function previewPadding(value: GraphPreviewRenderOptions["padding"]): GraphViewportPadding {
  if (typeof value === "number") {
    const padding = nonNegativeNumber(value, "Graph preview padding");
    return { top: padding, right: padding, bottom: padding, left: padding };
  }
  if (value !== undefined && (!value || typeof value !== "object" || Array.isArray(value))) {
    throw new Error("Graph preview padding must be a number or an object");
  }
  return {
    top: nonNegativeNumber(value?.top ?? 32, "Graph preview top padding"),
    right: nonNegativeNumber(value?.right ?? 32, "Graph preview right padding"),
    bottom: nonNegativeNumber(value?.bottom ?? 32, "Graph preview bottom padding"),
    left: nonNegativeNumber(value?.left ?? 32, "Graph preview left padding"),
  };
}

function validateRenderOptions(options: GraphPreviewRenderOptions): void {
  if (options.width !== undefined) positiveDimension(options.width, "Graph preview width");
  if (options.height !== undefined) positiveDimension(options.height, "Graph preview height");
  if (options.zoom !== undefined) positiveNumber(options.zoom, "Graph preview zoom");
  previewPadding(options.padding);
}

function positiveDimension(value: number, name: string): number {
  const result = positiveNumber(value, name);
  if (!Number.isInteger(result)) throw new Error(`${name} must be an integer`);
  return result;
}

function positiveNumber(value: number, name: string): number {
  if (!Number.isFinite(value) || value <= 0) throw new Error(`${name} must be a positive number`);
  return value;
}

function nonNegativeNumber(value: number, name: string): number {
  if (!Number.isFinite(value) || value < 0) throw new Error(`${name} must be a non-negative number`);
  return value;
}

function addLiteGraphPort(
  node: any,
  port: PreviewPort,
  side: "left" | "right",
  colormap: Record<string, TypePalette> | undefined,
  occurrence = 0,
): void {
  const type = port.types.includes("any/any") ? "" : port.types.join(",");
  const color = port.types.map((name) => paletteForType(colormap, name)?.type ?? paletteForType(colormap, name)?.color)
    .find((value): value is string => Boolean(value)) ?? DEFAULT_LINK_COLOR;
  const options = { label: port.name, color_on: color, color_off: color };
  if (side === "left") {
    // Use a distinct internal name for each slot. The label keeps all copies
    // visually associated with the same Almagest port.
    const name = occurrence === 0 ? port.name : `${port.name} ${occurrence + 1}`;
    node.addInput(name, type, options);
  }
  else node.addOutput(port.name, type, options);
}

function paletteForType(colormap: Record<string, TypePalette> | undefined, type: string): TypePalette | undefined {
  return colormap?.[type];
}

function fitPreviewLabel(node: any, label: string): void {
  if (!label) return;
  node.size[0] = Math.max(node.size[0], Math.min(360, 32 + label.length * 7));
  node.size[1] += 24;
}

function findSlot(
  slots: Array<{ name?: string; label?: string }> | undefined,
  name: string,
  occurrence = 0,
): number {
  if (!slots) return -1;
  let matches = 0;
  return slots.findIndex((slot) => {
    if (slot.name !== name && slot.label !== name) return false;
    if (matches === occurrence) return true;
    matches += 1;
    return false;
  });
}

function countIncomingLinks(config: GraphPreviewConfig): Map<string, Map<string, number>> {
  const counts = new Map<string, Map<string, number>>();
  for (const node of config.nodes) {
    for (const link of node.links) {
      const nodeCounts = counts.get(link.targetNode) ?? new Map<string, number>();
      nodeCounts.set(link.targetPort, (nodeCounts.get(link.targetPort) ?? 0) + 1);
      counts.set(link.targetNode, nodeCounts);
    }
  }
  return counts;
}

function takeNextInputOccurrence(
  occurrences: Map<string, Map<string, number>>,
  nodeName: string,
  portName: string,
): number {
  const nodeOccurrences = occurrences.get(nodeName) ?? new Map<string, number>();
  const occurrence = nodeOccurrences.get(portName) ?? 0;
  nodeOccurrences.set(portName, occurrence + 1);
  occurrences.set(nodeName, nodeOccurrences);
  return occurrence;
}

function addPreviewPort(ports: PreviewPort[], name: string, types: readonly string[]): void {
  const existing = ports.find((port) => port.name === name);
  if (existing) {
    if (!existing.types.length && types.length) existing.types = [...types];
    return;
  }
  ports.push({ name, types: [...types] });
}

function portSide(direction: string): "left" | "right" | null {
  switch (direction.toLowerCase()) {
    case "left":
    case "in":
    case "input": return "left";
    case "right":
    case "out":
    case "output": return "right";
    default: return null;
  }
}

function parseGraphLink(sourceNode: string, raw: string): GraphPreviewLink {
  boundedText(raw, `Node ${sourceNode} link is too long`);
  const marker = " -> [";
  const split = raw.indexOf(marker);
  const close = split < 0 ? -1 : raw.indexOf("] ", split + marker.length);
  if (split <= 0 || close < 0) throw new Error(`Node ${sourceNode} has an invalid link`);
  const sourcePort = raw.slice(0, split);
  const targetNode = raw.slice(split + marker.length, close);
  const targetPort = raw.slice(close + 2);
  if (!sourcePort || !targetNode || !targetPort || targetNode.includes("[") || targetNode.includes("]")) {
    throw new Error(`Node ${sourceNode} has an invalid link`);
  }
  return { sourceNode, sourcePort, targetNode, targetPort };
}

function fallbackPosition(index: number): [number, number] {
  return [60 + (index % 4) * 240, 60 + Math.floor(index / 4) * 180];
}

function record(value: unknown, message: string): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error(message);
  return value as Record<string, unknown>;
}

function requiredText(value: unknown, message: string): string {
  if (typeof value !== "string" || !value) throw new Error(message);
  return boundedText(value, message);
}

function boundedText(value: string, message: string): string {
  if (value.length > MAX_TEXT_LENGTH) throw new Error(message);
  return value;
}

function stripHuJSONExtensions(source: string): string {
  let output = "";
  let inString = false;
  let escaped = false;
  for (let index = 0; index < source.length; index += 1) {
    const char = source[index] ?? "";
    const next = source[index + 1] ?? "";
    if (inString) {
      output += char;
      if (escaped) escaped = false;
      else if (char === "\\") escaped = true;
      else if (char === "\"") inString = false;
      continue;
    }
    if (char === "\"") {
      inString = true;
      output += char;
      continue;
    }
    if (char === "/" && next === "/") {
      while (index < source.length && source[index] !== "\n" && source[index] !== "\r") index += 1;
      output += source[index] ?? "";
      continue;
    }
    if (char === "/" && next === "*") {
      const close = source.indexOf("*/", index + 2);
      if (close < 0) throw new Error("Graph preview has an unterminated comment");
      output += source.slice(index, close + 2).replace(/[^\r\n]/g, " ");
      index = close + 1;
      continue;
    }
    output += char;
  }
  if (inString) throw new Error("Graph preview has an unterminated string");
  return removeTrailingCommas(output);
}

function removeTrailingCommas(source: string): string {
  let output = "";
  let inString = false;
  let escaped = false;
  for (let index = 0; index < source.length; index += 1) {
    const char = source[index] ?? "";
    if (inString) {
      output += char;
      if (escaped) escaped = false;
      else if (char === "\\") escaped = true;
      else if (char === "\"") inString = false;
      continue;
    }
    if (char === "\"") inString = true;
    if (char === ",") {
      let next = index + 1;
      while (/\s/.test(source[next] ?? "")) next += 1;
      if (source[next] === "}" || source[next] === "]") continue;
    }
    output += char;
  }
  return output;
}
