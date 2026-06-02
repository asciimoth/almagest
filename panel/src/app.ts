import {
  applyNotification,
  DEFAULT_LINK_COLOR,
  DEFAULT_NODE_TEXT_COLOR,
  DEFAULT_NODE_TITLE_COLOR,
  emptySnapshot,
  formatPosition,
  linkColor,
  latestPriorityPopup,
  nodeMenuID,
  parsePosition,
  selectedNodeIDs,
  isKeyboardShortcut,
  setTypeColors,
  trimPopupText,
  typeColor,
  type ID,
  type LinkSnapshot,
  type NodeClassSnapshot,
  type NodePopup,
  type NodeSnapshot,
  type PortSnapshot,
  type WorkspaceNotification,
  type WorkspaceSnapshot,
} from "./pasta_model.js";

declare const LiteGraph: any;
declare const LGraph: any;
declare const LGraphCanvas: any;
type FormularMenuInstance = {
  feed(message: unknown): void;
  destroy(): void;
};
type FormularMenuConstructor = new (
  container: string | HTMLElement,
  menuID: string,
  send: (message: unknown) => void,
) => FormularMenuInstance;
// The built module runs from public/build, while vendored browser dependencies
// are served from vendor at the panel root.
// @ts-expect-error static browser vendor path
const { FormularMenu } = (await import("../../vendor/formular-menu/formular-menu.js")) as {
  FormularMenu: FormularMenuConstructor;
};

type LogEntry = { at: string; source: string; level: string; text: string };
type Surface = "admin" | "engine";
type APIMessage = {
  version: "v1";
  kind: "request" | "response" | "event";
  id?: string;
  surface?: Surface;
  method?: string;
  payload?: unknown;
  result?: unknown;
  error?: { code: string; message: string; details?: unknown };
};
type TypePalette = { type?: string; text?: string; color?: string; textColor?: string };
type EngineNotification = {
  kind: "workspace" | "colormap" | string;
  workspace_id?: string;
  workspace_notification?: WorkspaceNotification;
  colormap?: Record<string, TypePalette>;
};
type EngineNotificationEvent = {
  subscription_id: number;
  notification: EngineNotification;
};

declare global {
  interface Window {
    __almagestPanel?: {
      call<T>(method: string, params?: Record<string, unknown>): Promise<T | null>;
      snapshot(): WorkspaceSnapshot;
      selectNode(id: number): void;
      selectNodes(ids: number[]): void;
      renameNode(id: number, name: string): void;
      moveNode(id: number, x: number, y: number): void;
      canConnect(from: number, to: number): boolean;
      nodeScreenPosition(id: number): { x: number; y: number } | null;
      portScreenPosition(port: number): { x: number; y: number } | null;
      highlightedInputPort(): number | null;
    };
  }
}

let snapshot: WorkspaceSnapshot = emptySnapshot();
let graph: any;
let canvas: any;
let rendering = false;
let selectedMenu: { node: ID; menu: FormularMenuInstance } | null = null;
let activeTab = "graph";
let sidekickUpdate = 0;
let graphResizeObserver: ResizeObserver | null = null;
let localClipboard = "";
let socket: WebSocket | null = null;
let nextRequestID = 1;
let notificationSubscriptionID = 0;
const frontendLogs: LogEntry[] = [];
const liteLinkToPasta = new Map<number, number>();
const CLASS_DRAG_MIME = "application/x-almagest-node-class";
const debugClipboard = true;

const el = {
  status: document.querySelector<HTMLElement>("#backend-status")!,
  tabs: [...document.querySelectorAll<HTMLButtonElement>("[data-tab]")],
  panels: [...document.querySelectorAll<HTMLElement>("[data-panel]")],
  graphCanvas: document.querySelector<HTMLCanvasElement>("#graph-canvas")!,
  sidekick: document.querySelector<HTMLElement>("#sidekick")!,
  logs: document.querySelector<HTMLElement>("#logs-list")!,
};

function log(source: string, level: string, text: string): void {
  const entry = { at: new Date().toLocaleTimeString("en-US", { hour12: false }), source, level, text };
  frontendLogs.push(entry);
  if (frontendLogs.length > 200) frontendLogs.shift();
  renderLogs();
}

function debugLog(message: string, details: Record<string, unknown> = {}): void {
  if (!debugClipboard) return;
  console.log(`[almagest-panel] ${message}`, details);
}

const pendingRequests = new Map<string, {
  resolve(value: unknown): void;
  reject(error: Error): void;
}>();

function websocketURL(): string {
  const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${protocol}//${window.location.host}/api/ws`;
}

function connectAPI(): Promise<void> {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(websocketURL());
    socket = ws;
    ws.addEventListener("open", () => resolve(), { once: true });
    ws.addEventListener("error", () => reject(new Error("WebSocket connection failed")), { once: true });
    ws.addEventListener("message", (event) => handleAPIMessage(JSON.parse(String(event.data)) as APIMessage));
    ws.addEventListener("close", () => {
      el.status.textContent = "Admin API disconnected";
      for (const { reject: rejectRequest } of pendingRequests.values()) {
        rejectRequest(new Error("Admin API disconnected"));
      }
      pendingRequests.clear();
    });
  });
}

function request<T>(surface: Surface, method: string, payload: Record<string, unknown> = {}): Promise<T> {
  if (!socket || socket.readyState !== WebSocket.OPEN) {
    return Promise.reject(new Error("Admin API is not connected"));
  }
  const id = String(nextRequestID++);
  const msg: APIMessage = { version: "v1", kind: "request", id, surface, method, payload };
  return new Promise<T>((resolve, reject) => {
    pendingRequests.set(id, { resolve: (value) => resolve(value as T), reject });
    socket?.send(JSON.stringify(msg));
  });
}

function handleAPIMessage(msg: APIMessage): void {
  if (msg.kind === "response" && msg.id) {
    const pending = pendingRequests.get(msg.id);
    if (!pending) return;
    pendingRequests.delete(msg.id);
    if (msg.error) pending.reject(new Error(`${msg.error.code}: ${msg.error.message}`));
    else pending.resolve(msg.result ?? {});
    return;
  }
  if (msg.kind === "event" && msg.surface === "engine" && msg.method === "notification") {
    handleEngineNotification(msg.payload as EngineNotificationEvent);
  }
}

function handleEngineNotification(event: EngineNotificationEvent): void {
  const notification = event.notification;
  if (notification.kind === "colormap" && notification.colormap) {
    applyTypePalette(notification.colormap);
    renderGraph();
    updateSidekick();
    return;
  }
  if (notification.kind !== "workspace" || notification.workspace_id !== "main" || !notification.workspace_notification) {
    return;
  }
  const workspaceNotification = notification.workspace_notification;
  if (workspaceNotification.kind === "node_menu") {
    feedMenu(workspaceNotification);
  } else {
    snapshot = applyNotification(snapshot, workspaceNotification);
    renderGraph();
  }
}

function applyTypePalette(colormap: Record<string, TypePalette>): void {
  const colors: Record<string, { color: string; textColor: string }> = {};
  for (const [type, palette] of Object.entries(colormap)) {
    const color = palette.type ?? palette.color;
    const textColor = palette.text ?? palette.textColor ?? "#ffffff";
    if (color) colors[type] = { color, textColor };
  }
  setTypeColors(colors);
}

async function backend<T>(method: string, params: Record<string, unknown> = {}): Promise<T> {
  switch (method) {
    case "hello":
      return request<T>("admin", "hello", { client_name: "almagest-panel", version: "v1" });
    case "colormap":
      return request<T>("admin", "colormap");
    case "subscribeNotifications": {
      const result = await request<{ subscription_id: number }>("engine", "subscribe_notifications", { workspace: "main" });
      notificationSubscriptionID = result.subscription_id;
      return result as T;
    }
    case "snapshot": {
      const result = await request<{ snapshot: WorkspaceSnapshot }>("engine", "workspace_snapshot", { workspace: "main" });
      return result.snapshot as T;
    }
    case "classDescription": {
      const result = await request<{ description: string }>("engine", "node_class_long_description", {
        workspace: "main",
        class: params.class,
      });
      return result.description as T;
    }
    case "addNode": {
      const result = await request<{ id: number }>("engine", "add_node", { workspace: "main", class: params.class });
      if (typeof params.position === "string") {
        await request("engine", "set_node_position", { workspace: "main", id: result.id, position: params.position });
      }
      return result.id as T;
    }
    case "addLink": {
      const result = await request<{ id: number }>("engine", "add_link", {
        workspace: "main",
        port_a: params.from,
        port_b: params.to,
      });
      return result.id as T;
    }
    case "removeLink":
      return request<T>("engine", "remove_link", { workspace: "main", id: params.id });
    case "setNodePosition":
      return request<T>("engine", "set_node_position", { workspace: "main", id: params.id, position: params.position });
    case "setNodeName":
      await request("engine", "set_node_name", { workspace: "main", id: params.id, name: params.name });
      return true as T;
    case "removeNodePopup":
      return request<T>("engine", "remove_node_popup", { workspace: "main", node: params.id, popup: params.popupId });
    case "removeNodePopups":
      return request<T>("engine", "remove_node_popups", { workspace: "main", node: params.id });
    case "removeNodes":
      for (const id of (params.ids as number[] | undefined) ?? []) {
        await request("engine", "remove_node", { workspace: "main", id });
      }
      return {} as T;
    case "copy": {
      const result = await request<{ data: string }>("engine", "copy", { workspace: "main", ids: params.ids });
      return result.data as T;
    }
    case "paste": {
      const result = await request<{ ids: number[] }>("engine", "paste", { workspace: "main", data: params.data });
      return result.ids as T;
    }
    case "undo":
    case "redo":
      return request<T>("engine", method, { workspace: "main" });
    case "subscribeNodeMenu":
      return request<T>("engine", "subscribe_node_menu", {
        workspace: "main",
        node: params.id,
        subscription_id: notificationSubscriptionID,
      });
    case "unsubscribeNodeMenu":
      return request<T>("engine", "unsubscribe_node_menu", {
        workspace: "main",
        node: params.id,
        subscription_id: notificationSubscriptionID,
      });
    case "formular":
      return request<T>("engine", "send_node_formular_msg", {
        workspace: "main",
        node: params.id,
        message: params.message,
      });
    default:
      throw new Error(`unknown frontend method ${method}`);
  }
}

async function tryBackend<T>(method: string, params: Record<string, unknown> = {}): Promise<T | null> {
  try {
    return await backend<T>(method, params);
  } catch (error) {
    log("api", "warn", `${method}: ${(error as Error).message}`);
    return null;
  }
}

function registerLiteGraphNode(): void {
  if (LiteGraph.registered_node_types?.["pasta/view"]) return;
  function PastaViewNode(this: any) {
    this.properties = { pastaId: 0 };
    this.resizable = false;
    this.clonable = false;
    this.removable = false;
  }
  PastaViewNode.title = "Pasta";
  PastaViewNode.prototype.onConnectInput = function (
    inputIndex: number,
    _outputType: string,
    outputSlot: { pastaPortId?: number },
    outputNode: any,
    outputIndex: number,
  ): boolean {
    if (rendering) return true;
    const from = outputSlot?.pastaPortId ?? outputNode.outputs?.[outputIndex]?.pastaPortId;
    const to = this.inputs?.[inputIndex]?.pastaPortId;
    if (from && to) tryBackend("addLink", { from, to });
    return false;
  };
  PastaViewNode.prototype.onConnectionsChange = function (
    _type: number,
    _slot: number,
    isConnected: boolean,
    link: { id?: number; data?: { pastaId?: number } },
  ): void {
    if (rendering || isConnected || !link) return;
    const id = link.data?.pastaId ?? (link.id ? liteLinkToPasta.get(link.id) : undefined);
    if (id) tryBackend("removeLink", { id });
  };
  PastaViewNode.prototype.onDrawForeground = function (ctx: CanvasRenderingContext2D): void {
    drawNodePopup(ctx, this);
    drawNodeLabel(ctx, this);
  };
  PastaViewNode.prototype.onSelected = scheduleSidekickUpdate;
  PastaViewNode.prototype.onDeselected = scheduleSidekickUpdate;
  LiteGraph.registerNodeType("pasta/view", PastaViewNode);
}

function initGraph(): void {
  registerLiteGraphNode();
  graph = new LGraph();
  canvas = new LGraphCanvas("#graph-canvas", graph);
  canvas.node_title_color = DEFAULT_NODE_TEXT_COLOR;
  canvas.onNodeMoved = flushNodePositions;
  canvas.onSelectionChange = scheduleSidekickUpdate;
  installPastaPalette();
  resizeGraphCanvas();
  graphResizeObserver = new ResizeObserver(resizeGraphCanvas);
  graphResizeObserver.observe(el.graphCanvas);
  graph.start();
  el.graphCanvas.addEventListener("mouseup", flushNodePositions);
  el.graphCanvas.addEventListener("dragover", handleGraphCanvasDragOver);
  el.graphCanvas.addEventListener("drop", handleGraphCanvasDrop);
  document.addEventListener("pointerup", flushNodePositions, { capture: true });
  window.addEventListener("blur", flushNodePositions);
  el.graphCanvas.addEventListener("keydown", graphKeydown, { capture: true });
  el.graphCanvas.tabIndex = 0;
  requestAnimationFrame(resizeGraphCanvas);
}

function installPastaPalette(): void {
  if (!canvas) return;
  canvas.getMenuOptions = () => [
    {
      content: "Add Node",
      has_submenu: true,
      callback: (_value: unknown, _options: unknown, event: MouseEvent, menu: unknown) => {
        showPastaAddNodeMenu(event, menu);
        return false;
      },
    },
  ];
  canvas.onSearchBox = (_helper: HTMLElement, query: string) => {
    const normalized = query.trim().toLowerCase();
    return paletteClasses()
      .filter((cls) => paletteLabel(cls).toLowerCase().includes(normalized) || cls.class.toLowerCase().includes(normalized))
      .map(paletteLabel);
  };
  canvas.onSearchBoxSelection = (label: string, event: MouseEvent) => {
    const cls = paletteClasses().find((candidate) => paletteLabel(candidate) === label || candidate.class === label);
    if (cls) addPastaNodeAt(cls.class, canvasPositionFromEvent(event));
  };
}

function showPastaAddNodeMenu(event: MouseEvent, parentMenu: unknown): void {
  const entries = paletteClasses().map((cls) => ({
    content: paletteLabel(cls),
    value: cls.class,
    callback: (entry: { value: string }) => {
      addPastaNodeAt(entry.value, canvasPositionFromEvent(event));
      return false;
    },
  }));
  new LiteGraph.ContextMenu(entries, { event, parentMenu }, canvas.getCanvasWindow());
}

function paletteClasses(): NodeClassSnapshot[] {
  return Object.values(snapshot.classes).sort((a, b) => paletteLabel(a).localeCompare(paletteLabel(b)));
}

function paletteLabel(cls: NodeClassSnapshot): string {
  return shortClass(cls.class);
}

function previewPorts(cls: NodeClassSnapshot, side: "left" | "right"): Array<{ name: string; color: string }> {
  return cls.initial_ports
    .filter((port) => previewPortSide(port.direction) === side)
    .map((port) => ({
      name: port.name,
      color: previewPortColor(port.types),
    }));
}

function previewPortSide(direction: string): "left" | "right" | null {
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

function previewPortColor(types: string[]): string {
  for (const type of types) {
    const color = typeColor(type);
    if (color) return color.color;
  }
  return DEFAULT_LINK_COLOR;
}

function renderClassPreviewPorts(ports: Array<{ name: string; color: string }>, side: "left" | "right"): string {
  if (!ports.length) return "";
  return ports.map((port) => `
    <div class="class-preview-port class-preview-port-${side}">
      ${side === "left" ? `<span class="class-preview-port-dot" style="--class-preview-port-color: ${escapeAttr(port.color)}"></span>` : ""}
      <span class="class-preview-port-name">${escapeHTML(port.name)}</span>
      ${side === "right" ? `<span class="class-preview-port-dot" style="--class-preview-port-color: ${escapeAttr(port.color)}"></span>` : ""}
    </div>
  `).join("");
}

function renderClassPreview(cls: NodeClassSnapshot): string {
  const leftPorts = previewPorts(cls, "left");
  const rightPorts = previewPorts(cls, "right");
  return `
    <div class="class-preview" aria-hidden="true">
      <div class="class-preview-node">
        <div class="class-preview-title">${escapeHTML(shortClass(cls.class))}</div>
        <div class="class-preview-body">
          <div class="class-preview-column class-preview-column-left">${renderClassPreviewPorts(leftPorts, "left")}</div>
          <div class="class-preview-column class-preview-column-right">${renderClassPreviewPorts(rightPorts, "right")}</div>
        </div>
      </div>
    </div>
  `;
}

function resizeGraphCanvas(): void {
  if (!canvas) return;
  const rect = el.graphCanvas.getBoundingClientRect();
  const width = Math.max(1, Math.round(rect.width));
  const height = Math.max(1, Math.round(rect.height));
  canvas.resize(width, height);
}

function renderGraph(): void {
  if (!graph) return;
  rendering = true;
  liteLinkToPasta.clear();
  const selected = new Set(selectedNodeIDs(canvas?.selected_nodes));
  graph.clear();

  for (const [idText, nodeSnapshot] of Object.entries(snapshot.nodes)) {
    const id = Number(idText);
    const node = LiteGraph.createNode("pasta/view");
    node.id = id;
    node.properties.pastaId = id;
    node.properties.pastaLabel = nodeSnapshot.label;
    node.properties.pastaPopups = nodeSnapshot.popups ?? [];
    node.title = nodeTitle(nodeSnapshot);
    const primaryColor = typeColor(nodeSnapshot.primary_type);
    node.color = nodeSnapshot.placeholder ? "#f38ba8" : primaryColor?.color ?? DEFAULT_NODE_TITLE_COLOR;
    node.bgcolor = nodeSnapshot.has_root_path ? "#313244" : "#1e1e2e";
    node.boxcolor = nodeSnapshot.root ? "#a6e3a1" : "#45475a";
    node.pos = parsePosition(nodeSnapshot.position, 80 + graph._nodes.length * 24, 80 + graph._nodes.length * 24);
    for (const portID of nodeSnapshot.left_ports) addPort(node, snapshot.ports[String(portID)], portID);
    for (const portID of nodeSnapshot.right_ports) addPort(node, snapshot.ports[String(portID)], portID);
    node.size = node.computeSize();
    fitLabelInNode(node, nodeSnapshot);
    graph.add(node);
  }

  for (const [idText, link] of Object.entries(snapshot.links)) {
    connectLink(Number(idText), link);
  }

  graph.setDirtyCanvas(true, true);
  if (selected.size) {
    selectGraphNodes([...selected]);
  }
  rendering = false;
  updateSidekick();
}

function selectGraphNodes(ids: number[]): void {
  if (!canvas || !graph) return;
  const nodes = ids.map((id) => graph.getNodeById(id)).filter(Boolean);
  if (nodes.length) canvas.selectNodes(nodes);
}

function nodeTitle(node: NodeSnapshot): string {
  return node.name;
}

function fitLabelInNode(node: any, snapshot: NodeSnapshot): void {
  if (!snapshot.label) return;
  const labelWidth = Math.min(280, 32 + snapshot.label.length * 7);
  node.size[0] = Math.max(node.size[0], labelWidth);
  node.size[1] += 24;
}

function drawNodeLabel(ctx: CanvasRenderingContext2D, node: any): void {
  const label = node.properties?.pastaLabel;
  if (!label || node.flags?.collapsed) return;
  ctx.save();
  ctx.font = "12px Arial";
  ctx.fillStyle = "#cdd6f4";
  ctx.textAlign = "center";
  ctx.textBaseline = "bottom";
  ctx.fillText(String(label), node.size[0] * 0.5, node.size[1] - 7, node.size[0] - 18);
  ctx.restore();
}

function drawNodePopup(ctx: CanvasRenderingContext2D, node: any): void {
  const popup = latestPriorityPopup(node.properties?.pastaPopups);
  if (!popup) return;
  const text = trimPopupText(popup.text, 50);
  const palette = popupPalette(popup.type);
  ctx.save();
  ctx.font = "12px Arial";
  const paddingX = 8;
  const width = Math.max(54, ctx.measureText(text).width + paddingX * 2);
  const height = 22;
  const x = (node.size[0] - width) * 0.5;
  const y = -54;
  ctx.fillStyle = palette.bg;
  ctx.strokeStyle = palette.border;
  ctx.lineWidth = 1;
  roundRect(ctx, x, y, width, height, 5);
  ctx.fill();
  ctx.stroke();
  ctx.fillStyle = palette.text;
  ctx.textAlign = "center";
  ctx.textBaseline = "middle";
  ctx.fillText(text, x + width * 0.5, y + height * 0.5, width - paddingX * 2);
  ctx.restore();
}

function roundRect(ctx: CanvasRenderingContext2D, x: number, y: number, width: number, height: number, radius: number): void {
  const r = Math.min(radius, width * 0.5, height * 0.5);
  ctx.beginPath();
  ctx.moveTo(x + r, y);
  ctx.arcTo(x + width, y, x + width, y + height, r);
  ctx.arcTo(x + width, y + height, x, y + height, r);
  ctx.arcTo(x, y + height, x, y, r);
  ctx.arcTo(x, y, x + width, y, r);
  ctx.closePath();
}

function popupPalette(type: string): { bg: string; border: string; text: string } {
  switch (type) {
    case "err":
      return { bg: "#4a1f2a", border: "#f38ba8", text: "#f8d4de" };
    case "ward":
      return { bg: "#473817", border: "#f9e2af", text: "#fff1c7" };
    case "info":
      return { bg: "#18304b", border: "#89b4fa", text: "#d7e5ff" };
    default:
      return { bg: "#313244", border: "#a6adc8", text: "#f8f9ff" };
  }
}

function addPort(node: any, port: PortSnapshot | undefined, portID: number): void {
  if (!port) return;
  const type = liteGraphPortType(port.types);
  const extra = { pastaPortId: portID, label: port.name, pastaTypes: port.types };
  if (port.direction === "left") {
    const linkIDs = port.links.length > 1 ? port.links : [undefined];
    for (let i = 0; i < linkIDs.length; i += 1) {
      const name = i === 0 ? port.name : `${port.name} ${i + 1}`;
      node.addInput(name, type, { ...extra, pastaLinkId: linkIDs[i] });
    }
  } else {
    node.addOutput(port.name, type, extra);
  }
}

function liteGraphPortType(types: string[]): string {
  if (!types.length || types.includes("any/any")) return "";
  return types.join(",");
}

function connectLink(id: number, link: LinkSnapshot): void {
  const outNode = graph.getNodeById(link.right_port_node);
  const inNode = graph.getNodeById(link.left_port_node);
  if (!outNode || !inNode) return;
  const outIndex = findPortIndex(snapshot.nodes[String(link.right_port_node)]?.right_ports, link.right_port);
  const inIndex = findInputSlotIndex(inNode, link.left_port, id);
  if (outIndex < 0 || inIndex < 0) return;
  const liteLink = outNode.connect(outIndex, inNode, inIndex);
  if (liteLink) {
    liteLink.color = linkColor(link.type);
    liteLink.data = { pastaId: id };
    liteLinkToPasta.set(liteLink.id, id);
  }
}

function findPortIndex(ports: number[] | undefined, id: number): number {
  return ports ? ports.indexOf(id) : -1;
}

function findInputSlotIndex(node: any, portID: number, linkID: number): number {
  if (!node.inputs) return -1;
  const exact = node.inputs.findIndex((input: { pastaPortId?: number; pastaLinkId?: number }) => (
    input.pastaPortId === portID && input.pastaLinkId === linkID
  ));
  if (exact >= 0) return exact;
  return node.inputs.findIndex((input: { pastaPortId?: number }) => input.pastaPortId === portID);
}

function flushNodePositions(): void {
  if (rendering || !graph) return;
  const changes: Array<{ id: number; position: string }> = [];
  for (const node of graph._nodes as any[]) {
    const pastaNode = snapshot.nodes[String(node.id)];
    if (!pastaNode) continue;
    const next = formatPosition(node.pos);
    if (next !== pastaNode.position) {
      changes.push({ id: node.id, position: next });
    }
  }
  for (const change of changes) {
    tryBackend("setNodePosition", change);
  }
}

function currentSelectedNodeIDs(): number[] {
  return selectedNodeIDs(canvas?.selected_nodes).filter((id) => graph?.getNodeById(id)?.is_selected);
}

function updateSidekick(): void {
  const ids = currentSelectedNodeIDs();
  if (ids.length === 1) renderNodePanel(ids[0]);
  else renderClassPanel(ids.length);
}

function scheduleSidekickUpdate(): void {
  window.clearTimeout(sidekickUpdate);
  sidekickUpdate = window.setTimeout(updateSidekick, 0);
}

function renderClassPanel(selectedCount: number): void {
  destroyMenu();
  el.sidekick.dataset.nodeId = "";
  el.sidekick.dataset.nodeClass = "";
  const classes = Object.values(snapshot.classes).sort((a, b) => a.class.localeCompare(b.class));
  el.sidekick.innerHTML = `
    <div class="panel-title">${selectedCount > 1 ? `${selectedCount} nodes selected` : "Create node"}</div>
    <div class="class-list"></div>
  `;
  const list = el.sidekick.querySelector<HTMLElement>(".class-list")!;
  for (const cls of classes) {
    const primaryColor = typeColor(cls.primary_type);
    const button = document.createElement("button");
    button.className = "class-button";
    button.classList.toggle("class-button-primary", Boolean(primaryColor));
    button.type = "button";
    button.draggable = true;
    button.setAttribute("aria-label", `Create ${shortClass(cls.class)} node`);
    if (primaryColor) {
      button.style.setProperty("--class-primary-color", primaryColor.color);
      button.style.setProperty("--class-primary-text-color", primaryColor.textColor);
    }
    button.innerHTML = `
      <div class="class-button-copy">
        <strong>${escapeHTML(shortClass(cls.class))}</strong>
        <span>${escapeHTML(cls.short_description)}</span>
      </div>
      ${renderClassPreview(cls)}
    `;
    button.addEventListener("dragstart", (event) => {
      if (!event.dataTransfer) return;
      event.dataTransfer.effectAllowed = "copy";
      event.dataTransfer.setData(CLASS_DRAG_MIME, cls.class);
      event.dataTransfer.setData("text/plain", cls.class);
    });
    button.addEventListener("click", () => {
      addPastaNodeAt(cls.class, visibleCanvasSpawnPosition());
    });
    list.append(button);
  }
}

function addPastaNodeAt(className: string, pos: [number, number]): void {
  void tryBackend<number>("addNode", { class: className, position: formatPosition(pos) });
}

function visibleCanvasSpawnPosition(): [number, number] {
  if (!canvas?.convertCanvasToOffset) return [160, 120];
  const rect = el.graphCanvas.getBoundingClientRect();
  const x = clamp(rect.width * 0.5, 80, Math.max(80, rect.width - 160));
  const y = clamp(rect.height * 0.35, 80, Math.max(80, rect.height - 120));
  return canvas.convertCanvasToOffset([x, y]) as [number, number];
}

function canvasPositionFromEvent(event: MouseEvent): [number, number] {
  if (!canvas?.convertCanvasToOffset) return visibleCanvasSpawnPosition();
  const rect = el.graphCanvas.getBoundingClientRect();
  const x = clamp(event.clientX - rect.left, 0, rect.width);
  const y = clamp(event.clientY - rect.top, 0, rect.height);
  return canvas.convertCanvasToOffset([x, y]) as [number, number];
}

function canvasPositionFromDragEvent(event: DragEvent): [number, number] {
  if (!canvas?.convertCanvasToOffset) return visibleCanvasSpawnPosition();
  const rect = el.graphCanvas.getBoundingClientRect();
  const x = clamp(event.clientX - rect.left, 0, rect.width);
  const y = clamp(event.clientY - rect.top, 0, rect.height);
  return canvas.convertCanvasToOffset([x, y]) as [number, number];
}

function dragEventClassName(event: DragEvent): string {
  const className = event.dataTransfer?.getData(CLASS_DRAG_MIME) || event.dataTransfer?.getData("text/plain") || "";
  return snapshot.classes[className] ? className : "";
}

function handleGraphCanvasDragOver(event: DragEvent): void {
  if (!dragEventClassName(event)) return;
  event.preventDefault();
  if (event.dataTransfer) event.dataTransfer.dropEffect = "copy";
}

function handleGraphCanvasDrop(event: DragEvent): void {
  const className = dragEventClassName(event);
  if (!className) return;
  event.preventDefault();
  addPastaNodeAt(className, canvasPositionFromDragEvent(event));
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(Math.max(value, min), max);
}

function renderNodePanel(id: number): void {
  const node = snapshot.nodes[String(id)];
  if (!node) return;
  if (node.placeholder) {
    renderPlaceholderNodePanel(id, node);
    return;
  }
  if (
    el.sidekick.dataset.nodeId === String(id) &&
    el.sidekick.dataset.nodeClass === node.class &&
    selectedMenu?.node === id &&
    document.getElementById("node-menu")
  ) {
    syncNodeNameEditor(node);
    renderNodePopups(id, node);
    return;
  }
  const description = "";
  destroyMenu();
  el.sidekick.dataset.nodeId = String(id);
  el.sidekick.dataset.nodeClass = node.class;
  el.sidekick.innerHTML = `
    <form class="node-name-form" data-node-name-form>
      <label for="node-name-input">Name</label>
      <div class="node-name-row">
        <input id="node-name-input" name="name" autocomplete="off" spellcheck="false" value="${escapeAttr(node.name)}">
        <button type="submit">Rename</button>
      </div>
    </form>
    <div class="node-panel-tabs">
      <button class="active" data-node-tab="menu" type="button">Node menu</button>
      <button data-node-tab="docs" type="button">Description</button>
    </div>
    <div class="node-panel-section" data-node-panel="menu">
      <div id="node-menu"></div>
      <div id="node-popups"></div>
    </div>
    <div class="node-panel-section hidden" data-node-panel="docs">
      <h2>${escapeHTML(shortClass(node.class))}</h2>
      <p>${escapeHTML(description || "No long description is registered for this node class.")}</p>
    </div>
  `;
  void tryBackend<string>("classDescription", { class: node.class }).then((text) => {
    const descriptionPanel = el.sidekick.querySelector<HTMLElement>("[data-node-panel=\"docs\"] p");
    if (descriptionPanel && el.sidekick.dataset.nodeId === String(id)) {
      descriptionPanel.textContent = text || "No long description is registered for this node class.";
    }
  });
  for (const tab of el.sidekick.querySelectorAll<HTMLButtonElement>("[data-node-tab]")) {
    tab.addEventListener("click", () => setNodePanelTab(tab.dataset.nodeTab ?? "menu"));
  }
  const form = el.sidekick.querySelector<HTMLFormElement>("[data-node-name-form]");
  form?.addEventListener("submit", (event) => {
    event.preventDefault();
    submitNodeName(id);
  });
  form?.querySelector<HTMLInputElement>("input")?.addEventListener("blur", () => submitNodeName(id));
  mountMenu(id);
  renderNodePopups(id, node);
}

function renderPlaceholderNodePanel(id: number, node: NodeSnapshot): void {
  destroyMenu();
  el.sidekick.dataset.nodeId = String(id);
  el.sidekick.dataset.nodeClass = node.class;
  el.sidekick.innerHTML = `
    <form class="node-name-form" data-node-name-form>
      <label for="node-name-input">Name</label>
      <div class="node-name-row">
        <input id="node-name-input" name="name" autocomplete="off" spellcheck="false" value="${escapeAttr(node.name)}">
        <button type="submit">Rename</button>
      </div>
    </form>
    <div class="node-panel-section">
      <h2>${escapeHTML(shortClass(node.class))}</h2>
      <p>This node is a placeholder. Its implementation is unavailable, so the demo preserves its ports and links without opening a node menu.</p>
    </div>
  `;
  const form = el.sidekick.querySelector<HTMLFormElement>("[data-node-name-form]");
  form?.addEventListener("submit", (event) => {
    event.preventDefault();
    submitNodeName(id);
  });
  form?.querySelector<HTMLInputElement>("input")?.addEventListener("blur", () => submitNodeName(id));
}

function syncNodeNameEditor(node: NodeSnapshot): void {
  const input = el.sidekick.querySelector<HTMLInputElement>("#node-name-input");
  if (!input || document.activeElement === input) return;
  input.value = node.name;
}

async function submitNodeName(id: number): Promise<void> {
  const input = el.sidekick.querySelector<HTMLInputElement>("#node-name-input");
  if (!input) return;
  const node = snapshot.nodes[String(id)];
  const name = input.value.trim();
  if (!node || name === node.name) {
    if (node) input.value = node.name;
    return;
  }
  const renamed = await tryBackend<boolean>("setNodeName", { id, name });
  if (!renamed) {
    input.value = node.name;
  }
}

function renderNodePopups(nodeID: number, node: NodeSnapshot): void {
  const root = el.sidekick.querySelector<HTMLElement>("#node-popups");
  if (!root) return;
  const popups = [...(node.popups ?? [])].reverse();
  root.innerHTML = "";
  const header = document.createElement("div");
  header.className = "popup-panel-header";
  header.innerHTML = `<h2>Popups</h2>`;
  if (popups.length) {
    const clear = document.createElement("button");
    clear.type = "button";
    clear.textContent = "Clear all";
    clear.addEventListener("click", () => tryBackend("removeNodePopups", { id: nodeID }));
    header.append(clear);
  }
  root.append(header);
  if (!popups.length) {
    const empty = document.createElement("p");
    empty.className = "popup-empty";
    empty.textContent = "No popups";
    root.append(empty);
    return;
  }
  const list = document.createElement("ol");
  list.className = "popup-list";
  for (const popup of popups) {
    list.append(renderPopupItem(nodeID, popup));
  }
  root.append(list);
}

function renderPopupItem(nodeID: number, popup: NodePopup): HTMLElement {
  const item = document.createElement("li");
  item.className = `popup-item popup-${popup.type}`;
  const type = document.createElement("strong");
  type.textContent = popup.type;
  const text = document.createElement("span");
  text.textContent = popup.text;
  const remove = document.createElement("button");
  remove.type = "button";
  remove.textContent = "Delete";
  remove.addEventListener("click", () => tryBackend("removeNodePopup", { id: nodeID, popupId: popup.id }));
  item.append(type, text, remove);
  return item;
}

function setNodePanelTab(name: string): void {
  for (const tab of el.sidekick.querySelectorAll<HTMLButtonElement>("[data-node-tab]")) {
    tab.classList.toggle("active", tab.dataset.nodeTab === name);
  }
  for (const panel of el.sidekick.querySelectorAll<HTMLElement>("[data-node-panel]")) {
    panel.classList.toggle("hidden", panel.dataset.nodePanel !== name);
  }
}

function mountMenu(nodeID: number): void {
  if (selectedMenu?.node === nodeID && document.getElementById("node-menu")) return;
  destroyMenu();
  selectedMenu = {
    node: nodeID,
    menu: new FormularMenu("node-menu", nodeMenuID(nodeID), (message: unknown) => {
      tryBackend("formular", { id: nodeID, message });
    }),
  };
  tryBackend("subscribeNodeMenu", { id: nodeID });
}

function destroyMenu(): void {
  if (!selectedMenu) return;
  tryBackend("unsubscribeNodeMenu", { id: selectedMenu.node });
  selectedMenu.menu.destroy();
  selectedMenu = null;
}

function feedMenu(notification: WorkspaceNotification): void {
  if (!selectedMenu || notification.id !== selectedMenu.node || !notification.formular) return;
  selectedMenu.menu.feed(notification.formular);
}

async function requestSnapshot(): Promise<void> {
  const next = await tryBackend<WorkspaceSnapshot>("snapshot");
  if (next) {
    snapshot = next;
    renderGraph();
  }
}

function renderLogs(): void {
  const rows = [...frontendLogs].slice(-260);
  el.logs.innerHTML = "";
  if (!rows.length) {
    const item = document.createElement("li");
    item.innerHTML = "<span>Daemon log streaming is not implemented yet.</span>";
    el.logs.append(item);
    return;
  }
  for (const row of rows.reverse()) {
    const item = document.createElement("li");
    item.className = `log-${row.level}`;
    item.innerHTML = `<time>${escapeHTML(row.at)}</time><b>${escapeHTML(row.source)}</b><strong>${escapeHTML(row.level)}</strong><span>${escapeHTML(row.text)}</span>`;
    el.logs.append(item);
  }
}

function setTab(name: string): void {
  activeTab = name;
  for (const tab of el.tabs) tab.classList.toggle("active", tab.dataset.tab === name);
  for (const panel of el.panels) panel.classList.toggle("hidden", panel.dataset.panel !== name);
  if (name === "logs") renderLogs();
  if (name === "graph") setTimeout(resizeGraphCanvas, 0);
}

function graphKeydown(event: KeyboardEvent): void {
  if (activeTab !== "graph" || isEditableTarget(event.target)) return;
  debugLog("graph keydown", {
    key: event.key,
    ctrl: event.ctrlKey,
    meta: event.metaKey,
    shift: event.shiftKey,
    target: event.target instanceof HTMLElement ? event.target.tagName : String(event.target),
    selected: currentSelectedNodeIDs(),
  });
  if (isGraphShortcut(event, "KeyZ", "z")) {
    consumeGraphShortcut(event);
    runHistory(event.shiftKey ? "redo" : "undo");
  } else if (isGraphShortcut(event, "KeyY", "y")) {
    consumeGraphShortcut(event);
    runHistory("redo");
  } else if (isGraphShortcut(event, "KeyC", "c")) {
    consumeGraphShortcut(event);
    copySelection();
  } else if (isGraphShortcut(event, "KeyV", "v")) {
    consumeGraphShortcut(event);
    void pasteSelection();
  } else if (event.key === "Backspace" || event.key === "Delete") {
    const ids = currentSelectedNodeIDs();
    if (!ids.length) return;
    consumeGraphShortcut(event);
    tryBackend("removeNodes", { ids });
  }
}

function isGraphShortcut(event: KeyboardEvent, code: string, key: string): boolean {
  return isKeyboardShortcut(event, code, key);
}

function consumeGraphShortcut(event: KeyboardEvent): void {
  event.preventDefault();
  event.stopImmediatePropagation();
}

function isEditableTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  const tag = target.tagName.toLowerCase();
  return tag === "input" || tag === "textarea" || tag === "select" || target.isContentEditable;
}

async function runHistory(method: "undo" | "redo"): Promise<void> {
  await tryBackend(method);
  await requestSnapshot();
}

async function copySelection(): Promise<void> {
  const ids = currentSelectedNodeIDs();
  debugLog("copy selection requested", { ids });
  if (!ids.length) {
    debugLog("copy selection skipped: no selected backend nodes");
    return;
  }
  const data = await tryBackend<string>("copy", { ids });
  debugLog("backend copy returned", {
    hasData: Boolean(data),
    length: data?.length ?? 0,
    validPastaClipboard: data ? isPastaClipboard(data) : false,
    preview: data ? data.slice(0, 160) : "",
  });
  if (!data) return;
  localClipboard = data;
  void navigator.clipboard?.writeText(data).catch((error) => {
    debugLog("system copy failed", { error: (error as Error).message });
    log("clipboard", "warn", `system copy failed, using local clipboard: ${(error as Error).message}`);
  });
}

async function pasteSelection(): Promise<void> {
  let data = localClipboard;
  debugLog("paste selection requested", {
    localLength: localClipboard.length,
    localValid: isPastaClipboard(localClipboard),
    localPreview: localClipboard.slice(0, 160),
  });
  try {
    const systemClipboard = await navigator.clipboard?.readText();
    debugLog("system clipboard read", {
      hasData: Boolean(systemClipboard),
      length: systemClipboard?.length ?? 0,
      validPastaClipboard: systemClipboard ? isPastaClipboard(systemClipboard) : false,
      preview: systemClipboard ? systemClipboard.slice(0, 160) : "",
    });
    if (systemClipboard && isPastaClipboard(systemClipboard)) {
      data = systemClipboard;
      localClipboard = systemClipboard;
    }
  } catch (error) {
    debugLog("system paste failed", { error: (error as Error).message });
    log("clipboard", "warn", `system paste failed, using local clipboard: ${(error as Error).message}`);
  }
  if (!isPastaClipboard(data)) {
    debugLog("paste skipped: no valid Pasta clipboard data");
    return;
  }
  const offsetData = offsetClipboardPositions(data, 40, 40);
  debugLog("calling backend paste", {
    dataLength: offsetData.length,
    preview: offsetData.slice(0, 160),
  });
  const pasted = await tryBackend<number[]>("paste", { data: offsetData }) ?? [];
  debugLog("backend paste returned", {
    pasted,
    snapshotNodesBeforeRefresh: Object.keys(snapshot.nodes).length,
    snapshotLinksBeforeRefresh: Object.keys(snapshot.links).length,
  });
  requestSnapshot();
  debugLog("snapshot after paste refresh", {
    nodes: Object.keys(snapshot.nodes).length,
    links: Object.keys(snapshot.links).length,
  });
  if (pasted.length) {
    setTimeout(() => selectGraphNodes(pasted), 0);
  }
}

function isPastaClipboard(data: string): boolean {
  try {
    const payload = JSON.parse(data) as { version?: unknown; nodes?: unknown };
    return payload.version === 1 && Array.isArray(payload.nodes);
  } catch (error) {
    return false;
  }
}

function offsetClipboardPositions(data: string, dx: number, dy: number): string {
  try {
    const payload = JSON.parse(data) as { nodes?: Array<{ position?: string }> };
    if (!Array.isArray(payload.nodes)) return data;
    for (const node of payload.nodes) {
      if (!node.position) continue;
      const position = JSON.parse(node.position) as { x?: unknown; y?: unknown };
      if (typeof position.x !== "number" || typeof position.y !== "number") continue;
      node.position = formatPosition([position.x + dx, position.y + dy]);
    }
    return JSON.stringify(payload);
  } catch {
    return data;
  }
}

function escapeHTML(value: string): string {
  return value.replace(/[&<>"']/g, (ch) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[ch]!);
}

function escapeAttr(value: string): string {
  return escapeHTML(value).replace(/`/g, "&#96;");
}

function shortClass(name: string): string {
  return name.split("/").pop() ?? name;
}

function wireUI(): void {
  for (const tab of el.tabs) tab.addEventListener("click", () => setTab(tab.dataset.tab ?? "graph"));
  document.addEventListener("keydown", (event) => {
    graphKeydown(event);
  }, { capture: true });
}

function installTestHooks(): void {
  window.__almagestPanel = {
    call: tryBackend,
    snapshot: () => snapshot,
    selectNode: (id: number) => {
      const node = graph?.getNodeById(id);
      if (node) {
        canvas.selectNode(node);
        updateSidekick();
      }
    },
    selectNodes: (ids: number[]) => {
      selectGraphNodes(ids);
      updateSidekick();
    },
    renameNode: (id: number, name: string) => {
      void tryBackend("setNodeName", { id, name });
    },
    moveNode: (id: number, x: number, y: number) => {
      const node = graph?.getNodeById(id);
      if (!node) return;
      node.pos = [x, y];
      graph.setDirtyCanvas(true, true);
      flushNodePositions();
    },
    canConnect: (from: number, to: number) => {
      const fromPort = snapshot.ports[String(from)];
      const toPort = snapshot.ports[String(to)];
      if (!fromPort || !toPort) return false;
      return LiteGraph.isValidConnection(liteGraphPortType(fromPort.types), liteGraphPortType(toPort.types));
    },
    nodeScreenPosition: (id: number) => {
      const node = graph?.getNodeById(id);
      if (!node || !canvas) return null;
      const rect = el.graphCanvas.getBoundingClientRect();
      return {
        x: rect.left + (node.pos[0] + node.size[0] * 0.5 + canvas.ds.offset[0]) * canvas.ds.scale,
        y: rect.top + (node.pos[1] + node.size[1] * 0.5 + canvas.ds.offset[1]) * canvas.ds.scale,
      };
    },
    portScreenPosition: (port: number) => {
      const portSnapshot = snapshot.ports[String(port)];
      if (!portSnapshot || !graph || !canvas) return null;
      const node = graph.getNodeById(portSnapshot.node);
      if (!node) return null;
      const nodeSnapshot = snapshot.nodes[String(portSnapshot.node)];
      const ids = portSnapshot.direction === "left" ? nodeSnapshot?.left_ports : nodeSnapshot?.right_ports;
      const index = ids?.indexOf(port) ?? -1;
      if (index < 0) return null;
      const pos = node.getConnectionPos(portSnapshot.direction === "left", index);
      const rect = el.graphCanvas.getBoundingClientRect();
      return {
        x: rect.left + (pos[0] + canvas.ds.offset[0]) * canvas.ds.scale,
        y: rect.top + (pos[1] + canvas.ds.offset[1]) * canvas.ds.scale,
      };
    },
    highlightedInputPort: () => canvas?._highlight_input_slot?.pastaPortId ?? null,
  };
}

async function boot(): Promise<void> {
  wireUI();
  initGraph();
  installTestHooks();
  el.status.textContent = "Connecting admin API";
  await connectAPI();
  await backend("hello");
  const colormap = await backend<Record<string, TypePalette>>("colormap");
  applyTypePalette(colormap);
  await backend("subscribeNotifications");
  await requestSnapshot();
  el.status.textContent = "Admin API connected";
  setTab("graph");
}

boot().catch((error) => {
  el.status.textContent = `Panel failed: ${(error as Error).message}`;
  log("frontend", "error", (error as Error).message);
});
