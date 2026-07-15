import {
  applyNotification,
  applyWorkspaceSnapshot,
  applyWorkerNotification,
  DEFAULT_LINK_COLOR,
  DEFAULT_NODE_TEXT_COLOR,
  DEFAULT_NODE_TITLE_COLOR,
  emptySnapshot,
  formatPosition,
  linkColor,
  latestPriorityPopup,
  nodeMenuID,
  nodePositionMap,
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
  type WorkerSnapshot,
  type WorkerNotification,
  type WorkspaceNotification,
  type WorkspaceSnapshot,
} from "./pasta_model.js";
import {
  createClassButtonContent,
  createEmptyLogItem,
  createEmptyWorkerItem,
  createLogItem,
  createNodeNameForm,
  createNotificationToast,
  createWorkerNodeBlock,
  renderNodeDescription,
  shortClass,
  textElement,
  updateWorkerAgeElements,
  type WorkerNodeBlock,
  type ClassPreviewPort,
} from "./safe_dom.js";

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

type LogEntry = {
  at: string;
  source: string;
  level: string;
  message: string;
  node_id?: number;
  node_class?: string;
};
type SubscribeLogsResponse = {
  subscription_id: number;
  cache_size: number;
  entries: LogEntry[];
};
type Surface = "admin" | "engine" | "log";
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
  kind: "workspace" | "colormap" | "worker" | string;
  workspace_id?: string;
  workspace_notification?: WorkspaceNotification;
  worker_notification?: WorkerNotification;
  colormap?: Record<string, TypePalette>;
};
type EngineNotificationEvent = {
  subscription_id: number;
  notification: EngineNotification;
};
type ProjectVersionHighlight = "none" | "warn" | "critical";
type ProjectVersionStatus = {
  version: string;
  highlight: ProjectVersionHighlight;
  hover_message?: string;
};
type HelloResponse = {
  version: string;
  daemon?: string;
  project_version_status: ProjectVersionStatus;
  surfaces?: Record<string, unknown>;
};
type GraphViewportState = {
  scale: number;
  offset: [number, number];
};

declare global {
  interface Window {
    __almagestPanel?: {
      call<T>(method: string, params?: Record<string, unknown>): Promise<T | null>;
      snapshot(): WorkspaceSnapshot;
      selectNode(id: number): void;
      selectNodes(ids: number[]): void;
      workers(): Record<string, WorkerSnapshot>;
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
let workerAgeTimer = 0;
let graphResizeObserver: ResizeObserver | null = null;
let localClipboard = "";
let socket: WebSocket | null = null;
let nextRequestID = 1;
let notificationSubscriptionID = 0;
let logSubscriptionID = 0;
const frontendLogs: LogEntry[] = [];
let nextToastID = 1;
let liteGraphDrawGuardInstalled = false;
let liteGraphWidgetSelectionGuardInstalled = false;
let logStoreLimit = 0;
const liteLinkToPasta = new Map<number, number>();
const CLASS_DRAG_MIME = "application/x-almagest-node-class";
const GRAPH_VIEWPORT_STORAGE_KEY = "almagest.panel.graph.viewport.v1";
const PASTA_TRIGGER_CLASS = "pasta/Trigger";
const debugClipboard = true;
let graphViewportSaveTimeout = 0;

const el = {
  status: document.querySelector<HTMLElement>("#backend-status")!,
  projectVersion: document.querySelector<HTMLElement>("#project-version")!,
  tabs: [...document.querySelectorAll<HTMLButtonElement>("[data-tab]")],
  panels: [...document.querySelectorAll<HTMLElement>("[data-panel]")],
  graphCanvas: document.querySelector<HTMLCanvasElement>("#graph-canvas")!,
  sidekick: document.querySelector<HTMLElement>("#sidekick")!,
  psPanel: document.querySelector<HTMLElement>("[data-panel='ps']")!,
  ps: document.querySelector<HTMLElement>("#ps-list")!,
  logsPanel: document.querySelector<HTMLElement>("[data-panel='logs']")!,
  logs: document.querySelector<HTMLElement>("#logs-list")!,
  notifications: document.querySelector<HTMLElement>("#notifications"),
};

function log(source: string, level: string, text: string): void {
  appendLog({
    at: new Date().toISOString(),
    source,
    level,
    message: text,
  });
}

function debugLog(message: string, details: Record<string, unknown> = {}): void {
  if (!debugClipboard) return;
  console.log(`[almagest-panel] ${message}`, details);
}

function isFiniteNumber(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value);
}

function readGraphViewportState(): GraphViewportState | null {
  try {
    const raw = window.localStorage.getItem(GRAPH_VIEWPORT_STORAGE_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as { scale?: unknown; offset?: unknown };
    if (!isFiniteNumber(parsed.scale) || !Array.isArray(parsed.offset)) return null;
    const [x, y] = parsed.offset;
    if (!isFiniteNumber(x) || !isFiniteNumber(y)) return null;
    return { scale: parsed.scale, offset: [x, y] };
  } catch (error) {
    log("frontend", "warn", `graph viewport restore failed: ${(error as Error).message}`);
    return null;
  }
}

function applyGraphViewportState(): void {
  if (!canvas?.ds) return;
  const state = readGraphViewportState();
  if (!state) return;
  const minScale = isFiniteNumber(canvas.ds.min_scale) ? canvas.ds.min_scale : 0.1;
  const maxScale = isFiniteNumber(canvas.ds.max_scale) ? canvas.ds.max_scale : 10;
  canvas.ds.scale = clamp(state.scale, minScale, maxScale);
  canvas.ds.offset[0] = state.offset[0];
  canvas.ds.offset[1] = state.offset[1];
  canvas.setDirty(true, true);
}

function currentGraphViewportState(): GraphViewportState | null {
  if (!canvas?.ds || !isFiniteNumber(canvas.ds.scale)) return null;
  const [x, y] = canvas.ds.offset ?? [];
  if (!isFiniteNumber(x) || !isFiniteNumber(y)) return null;
  return { scale: canvas.ds.scale, offset: [x, y] };
}

function saveGraphViewportState(): void {
  window.clearTimeout(graphViewportSaveTimeout);
  graphViewportSaveTimeout = 0;
  const state = currentGraphViewportState();
  if (!state) return;
  try {
    window.localStorage.setItem(GRAPH_VIEWPORT_STORAGE_KEY, JSON.stringify(state));
  } catch (error) {
    log("frontend", "warn", `graph viewport save failed: ${(error as Error).message}`);
  }
}

function scheduleGraphViewportSave(): void {
  window.clearTimeout(graphViewportSaveTimeout);
  graphViewportSaveTimeout = window.setTimeout(saveGraphViewportState, 100);
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
      el.status.style.color = "#ff0000";
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
    return;
  }
  if (msg.kind === "event" && msg.surface === "admin" && msg.method === "project_version_status") {
    applyProjectVersionStatus(msg.payload as ProjectVersionStatus);
    return;
  }
  if (msg.kind === "event" && msg.surface === "log" && msg.method === "entry") {
    appendLog(msg.payload as LogEntry);
  }
}

function applyProjectVersionStatus(status: ProjectVersionStatus): void {
  el.projectVersion.hidden = !status.version;
  el.projectVersion.textContent = status.version;
  el.projectVersion.dataset.highlight = status.highlight;
  if (status.hover_message) {
    el.projectVersion.title = status.hover_message;
  } else {
    el.projectVersion.removeAttribute("title");
  }
}

function handleEngineNotification(event: EngineNotificationEvent): void {
  const notification = event.notification;
  if (notification.kind === "colormap" && notification.colormap) {
    applyTypePalette(notification.colormap);
    renderGraph();
    renderWorkers();
    updateWorkerAgeTimer();
    updateSidekick();
    return;
  }
  if (notification.kind === "worker" && notification.workspace_id === "main" && notification.worker_notification) {
    snapshot = applyWorkerNotification(snapshot, notification.worker_notification);
    renderWorkers();
    updateWorkerAgeTimer();
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
    renderWorkers();
    updateWorkerAgeTimer();
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
  installLiteGraphTypeColors();
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
    case "subscribeLogs": {
      const result = await request<SubscribeLogsResponse>("log", "subscribe");
      logSubscriptionID = result.subscription_id;
      logStoreLimit = Math.max(result.cache_size * 2, result.entries.length);
      frontendLogs.splice(0, frontendLogs.length, ...result.entries);
      renderLogs();
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
    case "moveNodes":
      return request<T>("engine", "move_nodes", { workspace: "main", positions: params.positions });
    case "setNodeName":
      await request("engine", "set_node_name", { workspace: "main", id: params.id, name: params.name });
      return true as T;
    case "removeNodePopup":
      return request<T>("engine", "remove_node_popup", { workspace: "main", node: params.id, popup: params.popupId });
    case "removeNodePopups":
      return request<T>("engine", "remove_node_popups", { workspace: "main", node: params.id });
    case "removeNodes": {
      const ids = (params.ids as number[] | undefined) ?? [];
      if (ids.length === 1) {
        await request("engine", "remove_node", { workspace: "main", id: ids[0] });
      } else if (ids.length > 1) {
        await request("engine", "remove_nodes", { workspace: "main", ids });
      }
      return {} as T;
    }
    case "triggerNode":
      return request<T>("engine", "trigger_node", { workspace: "main", id: params.id });
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
  installLiteGraphDrawGuard();
  installLiteGraphWidgetSelectionGuard();
  registerLiteGraphNode();
  graph = new LGraph();
  canvas = new LGraphCanvas("#graph-canvas", graph);
  canvas.node_title_color = DEFAULT_NODE_TEXT_COLOR;
  canvas.onNodeMoved = flushNodePositions;
  canvas.onSelectionChange = scheduleSidekickUpdate;
  installPastaPalette();
  applyGraphViewportState();
  resizeGraphCanvas();
  graphResizeObserver = new ResizeObserver(resizeGraphCanvas);
  graphResizeObserver.observe(el.graphCanvas);
  graph.start();
  el.graphCanvas.addEventListener("mouseup", () => {
    flushNodePositions();
    scheduleGraphViewportSave();
  });
  el.graphCanvas.addEventListener("wheel", scheduleGraphViewportSave);
  el.graphCanvas.addEventListener("mousewheel", scheduleGraphViewportSave);
  el.graphCanvas.addEventListener("dragover", handleGraphCanvasDragOver);
  el.graphCanvas.addEventListener("drop", handleGraphCanvasDrop);
  document.addEventListener("pointerup", () => {
    flushNodePositions();
    scheduleGraphViewportSave();
  }, { capture: true });
  document.addEventListener("mouseup", scheduleGraphViewportSave, { capture: true });
  window.addEventListener("blur", () => {
    flushNodePositions();
    saveGraphViewportState();
  });
  window.addEventListener("pagehide", saveGraphViewportState);
  el.graphCanvas.addEventListener("keydown", (event) => {
    graphKeydown(event);
    scheduleGraphViewportSave();
  }, { capture: true });
  el.graphCanvas.tabIndex = 0;
  requestAnimationFrame(resizeGraphCanvas);
}

function installLiteGraphDrawGuard(): void {
  if (liteGraphDrawGuardInstalled || !LGraphCanvas?.prototype?.drawFrontCanvas) return;
  const drawFrontCanvas = LGraphCanvas.prototype.drawFrontCanvas;
  LGraphCanvas.prototype.drawFrontCanvas = function (): void {
    sanitizeLiteGraphConnectionState(this);
    return drawFrontCanvas.call(this);
  };
  liteGraphDrawGuardInstalled = true;
}

function installLiteGraphWidgetSelectionGuard(): void {
  if (liteGraphWidgetSelectionGuardInstalled || !LGraphCanvas?.prototype?.processMouseDown) return;
  const processMouseDown = LGraphCanvas.prototype.processMouseDown;
  LGraphCanvas.prototype.processMouseDown = function (event: MouseEvent): unknown {
    const selectedBefore = selectedNodeIDs(this.selected_nodes);
    const result = processMouseDown.call(this, event);
    const [node, widget] = this.node_widget ?? [];
    if (widget?.options?.preserveSelection && node && event.type === `${LiteGraph.pointerevents_method}down`) {
      restoreGraphSelection(this, selectedBefore);
    }
    return result;
  };
  liteGraphWidgetSelectionGuardInstalled = true;
}

function sanitizeLiteGraphConnectionState(targetCanvas: any): void {
  if (!targetCanvas?.connecting_pos) return;
  if (targetCanvas.connecting_node && (targetCanvas.connecting_output || targetCanvas.connecting_input)) return;
  cancelLiteGraphConnectionState(targetCanvas);
}

function cancelLiteGraphConnectionState(targetCanvas: any): void {
  if (!targetCanvas) return;
  targetCanvas.connecting_output = null;
  targetCanvas.connecting_input = null;
  targetCanvas.connecting_pos = null;
  targetCanvas.connecting_node = null;
  targetCanvas.connecting_slot = -1;
  targetCanvas._highlight_input = null;
  targetCanvas._highlight_input_slot = null;
  targetCanvas._highlight_output = null;
  targetCanvas.dirty_canvas = true;
  targetCanvas.dirty_bgcanvas = true;
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

function installLiteGraphTypeColors(): void {
  if (!canvas) return;
  const byType: Record<string, string> = {};
  for (const port of Object.values(snapshot.ports)) {
    const primaryType = liteGraphPortType(port.types);
    const color = portTypeColor(port.types);
    if (primaryType && color) byType[primaryType] = color;
  }
  if (canvas.default_connection_color_byType) {
    Object.assign(canvas.default_connection_color_byType, byType);
  }
  if (canvas.default_connection_color_byTypeOff) {
    Object.assign(canvas.default_connection_color_byTypeOff, byType);
  }
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

function previewPorts(cls: NodeClassSnapshot, side: "left" | "right"): ClassPreviewPort[] {
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
  cancelLiteGraphConnectionState(canvas);
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
    addNodeWidgets(node, id, nodeSnapshot);
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

function restoreGraphSelection(targetCanvas: any, ids: number[]): void {
  const nodes = ids.map((id) => targetCanvas.graph?.getNodeById(id)).filter(Boolean);
  if (nodes.length) {
    targetCanvas.selectNodes(nodes);
  } else {
    targetCanvas.deselectAllNodes();
  }
  updateSidekick();
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

function addNodeWidgets(node: any, id: number, nodeSnapshot: NodeSnapshot): void {
  if (nodeSnapshot.class !== PASTA_TRIGGER_CLASS || nodeSnapshot.placeholder) return;
  node.addWidget("button", "Trigger", null, () => {
    void tryBackend("triggerNode", { id });
  }, { preserveSelection: true });
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
  const color = portTypeColor(port.types);
  const extra = {
    pastaPortId: portID,
    label: port.name,
    pastaTypes: port.types,
    color_on: color ?? undefined,
    color_off: color ?? undefined,
  };
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

function portTypeColor(types: string[]): string | null {
  for (const type of types) {
    const color = typeColor(type);
    if (color) return color.color;
  }
  return null;
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
  if (changes.length === 1) {
    tryBackend("setNodePosition", changes[0]);
  } else if (changes.length > 1) {
    tryBackend("moveNodes", { positions: nodePositionMap(changes) });
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
  const list = document.createElement("div");
  list.className = "class-list";
  el.sidekick.replaceChildren(
    textElement("div", selectedCount > 1 ? `${selectedCount} nodes selected` : "Create node", "panel-title"),
    list,
  );
  for (const cls of classes) {
    const primaryColor = typeColor(cls.primary_type);
    const leftPorts = previewPorts(cls, "left");
    const rightPorts = previewPorts(cls, "right");
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
    button.append(...createClassButtonContent(cls, leftPorts, rightPorts));
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
  const tabs = document.createElement("div");
  tabs.className = "node-panel-tabs";
  const menuTab = textElement("button", "Node menu");
  menuTab.className = "active";
  menuTab.dataset.nodeTab = "menu";
  menuTab.type = "button";
  const docsTab = textElement("button", "Description");
  docsTab.dataset.nodeTab = "docs";
  docsTab.type = "button";
  tabs.append(menuTab, docsTab);

  const menuPanel = document.createElement("div");
  menuPanel.className = "node-panel-section";
  menuPanel.dataset.nodePanel = "menu";
  const menu = document.createElement("div");
  menu.id = "node-menu";
  const popups = document.createElement("div");
  popups.id = "node-popups";
  menuPanel.append(menu, popups);

  const docsPanel = document.createElement("div");
  docsPanel.className = "node-panel-section hidden";
  docsPanel.dataset.nodePanel = "docs";
  const descriptionPanel = document.createElement("div");
  descriptionPanel.className = "node-description";
  descriptionPanel.dataset.nodeDescription = "";
  renderNodeDescription(descriptionPanel, description);
  docsPanel.append(textElement("h2", shortClass(node.class)), descriptionPanel);
  el.sidekick.replaceChildren(createNodeNameForm(node.name), tabs, menuPanel, docsPanel);
  void tryBackend<string>("classDescription", { class: node.class }).then((text) => {
    const descriptionPanel = el.sidekick.querySelector<HTMLElement>("[data-node-description]");
    if (
      descriptionPanel &&
      el.sidekick.dataset.nodeId === String(id) &&
      el.sidekick.dataset.nodeClass === node.class
    ) {
      renderNodeDescription(descriptionPanel, text ?? "");
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
  const section = document.createElement("div");
  section.className = "node-panel-section";
  section.append(
    textElement("h2", shortClass(node.class)),
    textElement("p", "This node is a placeholder. Its implementation is unavailable now, so almagest preserves its ports and links as placeholders in case implementation will become available later."),
  );
  el.sidekick.replaceChildren(createNodeNameForm(node.name), section);
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
  root.replaceChildren();
  const header = document.createElement("div");
  header.className = "popup-panel-header";
  header.append(textElement("h2", "Popups"));
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
    snapshot = applyWorkspaceSnapshot(snapshot, next);
    renderGraph();
    renderWorkers();
    updateWorkerAgeTimer();
  }
}

function renderLogs(): void {
  el.logs.replaceChildren();
  if (!frontendLogs.length) {
    el.logs.append(createEmptyLogItem());
    return;
  }
  const rows = [...frontendLogs].reverse();
  for (const row of rows) {
    el.logs.append(createLogItem(row));
  }
}

type PsScrollAnchor = {
  nodeID: string;
  offsetTop: number;
  previousHeight: number;
  previousTop: number;
  pinToTop: boolean;
};

function renderWorkers(): void {
  const anchor = capturePsScrollAnchor();
  el.ps.replaceChildren();
  const blocks = workerNodeBlocks();
  if (!blocks.length) {
    el.ps.append(createEmptyWorkerItem());
  } else {
    const now = new Date();
    for (const block of blocks) {
      el.ps.append(createWorkerNodeBlock(block, now));
    }
  }
  restorePsScrollAnchor(anchor);
}

function updateWorkerAges(): void {
  updateWorkerAgeElements(el.ps, new Date());
}

function workerNodeBlocks(): WorkerNodeBlock[] {
  const byNode = new Map<number, WorkerSnapshot[]>();
  for (const worker of Object.values(snapshot.workers ?? {})) {
    const workers = byNode.get(worker.node) ?? [];
    workers.push(worker);
    byNode.set(worker.node, workers);
  }

  return [...byNode.entries()]
    .sort(([left], [right]) => left - right)
    .map(([nodeID, workers]) => {
      workers.sort((left, right) => left.id - right.id);
      const node = snapshot.nodes[String(nodeID)];
      const fallback = workers[0];
      const primaryType = node?.primary_type || snapshot.classes[fallback.class]?.primary_type || "";
      const color = primaryType ? typeColor(primaryType) : null;
      return {
        nodeID,
        node,
        fallbackClass: fallback.class,
        fallbackName: fallback.name,
        workers,
        color: color?.color,
        textColor: color?.textColor,
      };
    });
}

// Preserve the first visible node block across replacement renders. This keeps
// the Ps list from jumping when worker notifications insert or remove blocks
// above the current viewport.
function capturePsScrollAnchor(): PsScrollAnchor {
  const panel = el.psPanel;
  const panelTop = panel.getBoundingClientRect().top;
  for (const block of el.ps.querySelectorAll<HTMLElement>(".ps-node-block")) {
    const rect = block.getBoundingClientRect();
    if (rect.bottom >= panelTop) {
      return {
        nodeID: block.dataset.nodeId ?? "",
        offsetTop: rect.top - panelTop,
        previousHeight: panel.scrollHeight,
        previousTop: panel.scrollTop,
        pinToTop: panel.scrollTop <= 2,
      };
    }
  }
  return {
    nodeID: "",
    offsetTop: 0,
    previousHeight: panel.scrollHeight,
    previousTop: panel.scrollTop,
    pinToTop: panel.scrollTop <= 2,
  };
}

function restorePsScrollAnchor(anchor: PsScrollAnchor): void {
  const panel = el.psPanel;
  if (anchor.pinToTop) {
    panel.scrollTop = 0;
    return;
  }
  const block = anchor.nodeID ? el.ps.querySelector<HTMLElement>(`.ps-node-block[data-node-id="${anchor.nodeID}"]`) : null;
  if (block) {
    const nextOffset = block.getBoundingClientRect().top - panel.getBoundingClientRect().top;
    panel.scrollTop += nextOffset - anchor.offsetTop;
    return;
  }
  panel.scrollTop = anchor.previousTop + (panel.scrollHeight - anchor.previousHeight);
}

function updateWorkerAgeTimer(): void {
  const shouldRun = activeTab === "ps" && Object.keys(snapshot.workers ?? {}).length > 0;
  if (shouldRun && !workerAgeTimer) {
    workerAgeTimer = window.setInterval(updateWorkerAges, 1000);
  } else if (!shouldRun && workerAgeTimer) {
    window.clearInterval(workerAgeTimer);
    workerAgeTimer = 0;
  }
}

function appendLog(entry: LogEntry): void {
  if (isConfigSaveFailure(entry)) {
    notifyError(entry.message);
  }
  const panel = el.logsPanel;
  const previousHeight = panel.scrollHeight;
  const previousTop = panel.scrollTop;
  const pinToTop = previousTop <= 2;
  const hadEmptyState = !frontendLogs.length;
  frontendLogs.push(entry);
  trimLogStore();
  if (hadEmptyState) {
    renderLogs();
  } else {
    el.logs.prepend(createLogItem(entry));
    const overflow = el.logs.children.length - frontendLogs.length;
    for (let i = 0; i < overflow; i++) {
      el.logs.lastElementChild?.remove();
    }
  }
  if (!pinToTop) {
    panel.scrollTop = previousTop + (panel.scrollHeight - previousHeight);
  }
}

function isConfigSaveFailure(entry: LogEntry): boolean {
  return entry.source === "daemon" && entry.level === "error" && entry.message.startsWith("config save failed:");
}

function notifyError(message: string): void {
  const toast = createNotificationToast(message);
  const id = nextToastID++;
  toast.dataset.toastId = String(id);
  const close = () => {
    toast.remove();
  };
  toast.querySelector("button")?.addEventListener("click", close);
  el.notifications?.append(toast);
  window.setTimeout(() => {
    if (toast.dataset.toastId === String(id)) close();
  }, 12000);
}

function trimLogStore(): void {
  if (logStoreLimit <= 0 || frontendLogs.length <= logStoreLimit) return;
  frontendLogs.splice(0, frontendLogs.length - logStoreLimit);
}

function setTab(name: string): void {
  activeTab = name;
  for (const tab of el.tabs) tab.classList.toggle("active", tab.dataset.tab === name);
  for (const panel of el.panels) panel.classList.toggle("hidden", panel.dataset.panel !== name);
  if (name === "logs") renderLogs();
  if (name === "ps") renderWorkers();
  if (name === "graph") setTimeout(resizeGraphCanvas, 0);
  updateWorkerAgeTimer();
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
    workers: () => snapshot.workers,
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
  const hello = await backend<HelloResponse>("hello");
  applyProjectVersionStatus(hello.project_version_status);
  const colormap = await backend<Record<string, TypePalette>>("colormap");
  applyTypePalette(colormap);
  await backend("subscribeLogs");
  await backend("subscribeNotifications");
  await requestSnapshot();
  el.status.textContent = "Admin API connected";
  setTab("graph");
}

boot().catch((error) => {
  el.status.textContent = `Panel failed: ${(error as Error).message}`;
  log("frontend", "error", (error as Error).message);
});
