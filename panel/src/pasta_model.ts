export type ID = number;

export type NodePopupType = "info" | "ward" | "err";

export type NodeClassSnapshot = {
  class: string;
  short_description: string;
  unique: boolean;
  primary_type: string;
  initial_ports: Array<{ direction: string; name: string; types: string[] }>;
};

export type NodePopup = {
  id: ID;
  type: NodePopupType;
  text: string;
};

export type NodeSnapshot = {
  class: string;
  name: string;
  primary_type: string;
  label: string;
  position: string;
  popups?: NodePopup[] | null;
  placeholder: boolean;
  root: boolean;
  has_root_path: boolean;
  left_ports: ID[];
  right_ports: ID[];
};

export type PortSnapshot = {
  direction: "left" | "right";
  node: ID;
  name: string;
  types: string[];
  links: ID[];
};

export type LinkSnapshot = {
  type: string;
  placeholder: boolean;
  left_port: ID;
  left_port_node: ID;
  right_port: ID;
  right_port_node: ID;
};

export type WorkerSnapshot = {
  id: ID;
  node: ID;
  class: string;
  name: string;
  worker_name?: string;
  started_at: string;
  orphan: boolean;
  error?: string;
};

export type WorkspaceSnapshot = {
  // Classes are ordered by the daemon and must stay as an array so UI palettes
  // can render exactly the server-provided order.
  classes: NodeClassSnapshot[];
  nodes: Record<string, NodeSnapshot>;
  ports: Record<string, PortSnapshot>;
  links: Record<string, LinkSnapshot>;
  workers: Record<string, WorkerSnapshot>;
};

// Graph snapshots from the workspace API do not include engine workers. The
// panel keeps workers from the independent worker notification stream when the
// field is absent.
export type WorkspaceSnapshotUpdate = Omit<WorkspaceSnapshot, "workers"> & {
  workers?: Record<string, WorkerSnapshot>;
};

export type WorkspaceNotificationKind =
  | "workspace_snapshot"
  | "workspace_stopped"
  | "node_class_added"
  | "node_class_removed"
  | "node_added"
  | "node_updated"
  | "node_removed"
  | "nodes_removed"
  | "nodes_updated"
  | "port_added"
  | "port_updated"
  | "port_removed"
  | "link_added"
  | "link_updated"
  | "link_removed"
  | "worker_spawned"
  | "worker_stopped"
  | "worker_failed"
  | "node_menu";

export type WorkspaceNotification = {
  kind: WorkspaceNotificationKind;
  id?: ID;
  class_name?: string;
  snapshot?: WorkspaceSnapshotUpdate;
  node_class?: NodeClassSnapshot;
  node?: NodeSnapshot;
  nodes?: Record<string, NodeSnapshot>;
  port?: PortSnapshot;
  link?: LinkSnapshot;
  worker?: WorkerSnapshot;
  formular?: unknown;
};

export type NodePositionChange = {
  id: ID;
  position: string;
};

export type WorkerNotification = {
  kind: "worker_snapshot" | "worker_spawned" | "worker_stopped" | "worker_failed";
  id?: ID;
  snapshot?: Record<string, WorkerSnapshot>;
  worker?: WorkerSnapshot;
};

export type TypePalette = {
  type?: string;
  text?: string;
  // Accept old daemon field names at the transport boundary.
  color?: string;
  textColor?: string;
};

export type EngineNotification =
  | { kind: "colormap"; colormap: Record<string, TypePalette> }
  | { kind: "worker"; workspace_id: string; worker_notification: WorkerNotification }
  | { kind: "workspace"; workspace_id: string; workspace_notification: WorkspaceNotification };

export type EngineNotificationEvent = {
  subscription_id: number;
  notification: EngineNotification;
};

type ClipboardNode = {
  position?: unknown;
  [key: string]: unknown;
};

type ClipboardPayload = {
  version: 1;
  nodes: ClipboardNode[];
  [key: string]: unknown;
};

export const DEFAULT_LINK_COLOR = "#9A9";
export const DEFAULT_NODE_TITLE_COLOR = "#89b4fa";
export const DEFAULT_NODE_TEXT_COLOR = "#f8f9ff";

export type TypeColor = {
  color: string;
  textColor: string;
};

const loadedTypeColors: Record<string, TypeColor> = {};

export function emptySnapshot(): WorkspaceSnapshot {
  return { classes: [], nodes: {}, ports: {}, links: {}, workers: {} };
}

// Engine data controls graph labels, menu messages, canvas state, and CSS
// colors. Decode the complete shape before it reaches those consumers.
export function parseEngineNotificationEvent(input: unknown): EngineNotificationEvent | null {
  if (!isRecord(input) || !isID(input.subscription_id)) return null;
  const notification = parseEngineNotification(input.notification);
  return notification ? { subscription_id: input.subscription_id, notification } : null;
}

export function parseWorkspaceSnapshot(input: unknown): WorkspaceSnapshotUpdate | null {
  if (!isRecord(input) || !Array.isArray(input.classes)) return null;
  const classes = parseArray(input.classes, parseNodeClassSnapshot);
  const nodes = parseIDRecord(input.nodes, parseNodeSnapshot);
  const ports = parseIDRecord(input.ports, parsePortSnapshot);
  const links = parseIDRecord(input.links, parseLinkSnapshot);
  if (!classes || !nodes || !ports || !links) return null;
  if (input.workers === undefined) return { classes, nodes, ports, links };
  const workers = parseIDRecord(input.workers, parseWorkerSnapshot);
  return workers ? { classes, nodes, ports, links, workers } : null;
}

export function parseWorkspaceSnapshotResponse(input: unknown): WorkspaceSnapshotUpdate | null {
  return isRecord(input) ? parseWorkspaceSnapshot(input.snapshot) : null;
}

export function parseTypePaletteMap(input: unknown): Record<string, TypePalette> | null {
  return parseStringRecord(input, parseTypePalette);
}

export function cloneSnapshot(snapshot: WorkspaceSnapshot): WorkspaceSnapshot {
  const cloned = structuredClone(snapshot);
  cloned.classes ??= [];
  cloned.workers ??= {};
  return cloned;
}

// nodeClassByName provides key-style lookup without converting the ordered API
// class list into an object that would hide the daemon's display order.
export function nodeClassByName(snapshot: WorkspaceSnapshot, className: string): NodeClassSnapshot | undefined {
  return snapshot.classes.find((cls) => cls.class === className);
}

export function applyNotification(
  current: WorkspaceSnapshot,
  notification: WorkspaceNotification,
): WorkspaceSnapshot {
  if (notification.kind === "workspace_snapshot" && notification.snapshot) {
    return applyWorkspaceSnapshot(current, notification.snapshot);
  }
  const next = cloneSnapshot(current);
  const id = notification.id ? String(notification.id) : "";
  switch (notification.kind) {
    case "node_class_added":
      if (notification.class_name && notification.node_class) {
        next.classes = next.classes.filter((cls) => cls.class !== notification.class_name);
        next.classes.push(notification.node_class);
      }
      break;
    case "node_class_removed":
      if (notification.class_name) next.classes = next.classes.filter((cls) => cls.class !== notification.class_name);
      break;
    case "node_added":
    case "node_updated":
      if (id && notification.node) next.nodes[id] = notification.node;
      break;
    case "node_removed":
      if (id) delete next.nodes[id];
      break;
    case "nodes_removed":
      if (notification.snapshot) {
        return applyWorkspaceSnapshot(next, notification.snapshot);
      }
      for (const nodeID of Object.keys(notification.nodes ?? {})) {
        delete next.nodes[nodeID];
      }
      break;
    case "nodes_updated":
      Object.assign(next.nodes, notification.nodes ?? {});
      break;
    case "port_added":
    case "port_updated":
      if (id && notification.port) next.ports[id] = notification.port;
      break;
    case "port_removed":
      if (id) delete next.ports[id];
      break;
    case "link_added":
    case "link_updated":
      if (id && notification.link) next.links[id] = notification.link;
      break;
    case "link_removed":
      if (id) delete next.links[id];
      break;
    case "worker_spawned":
      if (id && notification.worker) next.workers[id] = notification.worker;
      break;
    case "worker_stopped":
    case "worker_failed":
      if (id) delete next.workers[id];
      break;
  }
  return next;
}

export function applyWorkspaceSnapshot(
  current: WorkspaceSnapshot,
  incoming: WorkspaceSnapshotUpdate,
): WorkspaceSnapshot {
  return {
    classes: structuredClone(incoming.classes),
    nodes: structuredClone(incoming.nodes),
    ports: structuredClone(incoming.ports),
    links: structuredClone(incoming.links),
    workers: structuredClone(incoming.workers ?? current.workers),
  };
}

export function applyWorkerNotification(
  current: WorkspaceSnapshot,
  notification: WorkerNotification,
): WorkspaceSnapshot {
  const next = cloneSnapshot(current);
  const id = notification.id ? String(notification.id) : "";
  switch (notification.kind) {
    case "worker_snapshot":
      next.workers = { ...(notification.snapshot ?? {}) };
      break;
    case "worker_spawned":
      if (id && notification.worker) next.workers[id] = notification.worker;
      break;
    case "worker_stopped":
    case "worker_failed":
      if (id) delete next.workers[id];
      break;
  }
  return next;
}

function parseEngineNotification(input: unknown): EngineNotification | null {
  if (!isRecord(input)) return null;
  switch (input.kind) {
    case "colormap": {
      const colormap = parseTypePaletteMap(input.colormap);
      return colormap ? { kind: "colormap", colormap } : null;
    }
    case "worker": {
      if (typeof input.workspace_id !== "string") return null;
      const worker = parseWorkerNotification(input.worker_notification);
      return worker
        ? { kind: "worker", workspace_id: input.workspace_id, worker_notification: worker }
        : null;
    }
    case "workspace": {
      if (typeof input.workspace_id !== "string") return null;
      const workspace = parseWorkspaceNotification(input.workspace_notification);
      return workspace
        ? { kind: "workspace", workspace_id: input.workspace_id, workspace_notification: workspace }
        : null;
    }
    default:
      return null;
  }
}

function parseWorkspaceNotification(input: unknown): WorkspaceNotification | null {
  if (!isRecord(input) || !isWorkspaceNotificationKind(input.kind)) return null;
  const id = input.id === undefined ? undefined : isID(input.id) ? input.id : null;
  const className = input.class_name === undefined
    ? undefined
    : typeof input.class_name === "string" ? input.class_name : null;
  const snapshot = input.snapshot === undefined ? undefined : parseWorkspaceSnapshot(input.snapshot);
  const nodeClass = input.node_class === undefined ? undefined : parseNodeClassSnapshot(input.node_class);
  const node = input.node === undefined ? undefined : parseNodeSnapshot(input.node);
  const nodes = input.nodes === undefined ? undefined : parseIDRecord(input.nodes, parseNodeSnapshot);
  const port = input.port === undefined ? undefined : parsePortSnapshot(input.port);
  const link = input.link === undefined ? undefined : parseLinkSnapshot(input.link);
  const worker = input.worker === undefined ? undefined : parseWorkerSnapshot(input.worker);
  if (id === null || className === null || snapshot === null || nodeClass === null
    || node === null || nodes === null || port === null || link === null || worker === null) return null;

  const notification: WorkspaceNotification = {
    kind: input.kind,
    ...(id === undefined ? {} : { id }),
    ...(className === undefined ? {} : { class_name: className }),
    ...(snapshot === undefined ? {} : { snapshot }),
    ...(nodeClass === undefined ? {} : { node_class: nodeClass }),
    ...(node === undefined ? {} : { node }),
    ...(nodes === undefined ? {} : { nodes }),
    ...(port === undefined ? {} : { port }),
    ...(link === undefined ? {} : { link }),
    ...(worker === undefined ? {} : { worker }),
    ...(input.formular === undefined ? {} : { formular: input.formular }),
  };

  switch (notification.kind) {
    case "workspace_snapshot":
      return notification.snapshot ? notification : null;
    case "workspace_stopped":
      return notification;
    case "node_class_added":
      return notification.class_name !== undefined && notification.node_class ? notification : null;
    case "node_class_removed":
      return notification.class_name !== undefined ? notification : null;
    case "node_added":
    case "node_updated":
      return notification.id !== undefined && notification.node ? notification : null;
    case "node_removed":
      return notification.id !== undefined ? notification : null;
    case "nodes_removed":
    case "nodes_updated":
      return notification.nodes ? notification : null;
    case "port_added":
    case "port_updated":
      return notification.id !== undefined && notification.port ? notification : null;
    case "port_removed":
      return notification.id !== undefined ? notification : null;
    case "link_added":
    case "link_updated":
      return notification.id !== undefined && notification.link ? notification : null;
    case "link_removed":
      return notification.id !== undefined ? notification : null;
    case "worker_spawned":
      return notification.id !== undefined && notification.worker ? notification : null;
    case "worker_stopped":
    case "worker_failed":
      return notification.id !== undefined ? notification : null;
    case "node_menu":
      return notification.id !== undefined && isRecord(notification.formular) ? notification : null;
  }
}

function parseWorkerNotification(input: unknown): WorkerNotification | null {
  if (!isRecord(input)) return null;
  switch (input.kind) {
    case "worker_snapshot": {
      const snapshot = parseIDRecord(input.snapshot, parseWorkerSnapshot);
      return snapshot ? { kind: input.kind, snapshot } : null;
    }
    case "worker_spawned": {
      const worker = parseWorkerSnapshot(input.worker);
      if (!isID(input.id) || !worker) return null;
      return { kind: input.kind, id: input.id, worker };
    }
    case "worker_stopped":
    case "worker_failed":
      return isID(input.id) ? { kind: input.kind, id: input.id } : null;
    default:
      return null;
  }
}

function parseNodeClassSnapshot(input: unknown): NodeClassSnapshot | null {
  if (!isRecord(input)
    || typeof input.class !== "string"
    || typeof input.short_description !== "string"
    || typeof input.unique !== "boolean"
    || typeof input.primary_type !== "string"
    || !Array.isArray(input.initial_ports)) return null;
  const initialPorts = parseArray(input.initial_ports, parseNodeClassPortSnapshot);
  return initialPorts ? {
    class: input.class,
    short_description: input.short_description,
    unique: input.unique,
    primary_type: input.primary_type,
    initial_ports: initialPorts,
  } : null;
}

function parseNodeClassPortSnapshot(input: unknown): NodeClassSnapshot["initial_ports"][number] | null {
  if (!isRecord(input)
    || typeof input.direction !== "string"
    || typeof input.name !== "string") return null;
  const types = parseStringArray(input.types);
  if (!types) return null;
  return { direction: input.direction, name: input.name, types };
}

function parseNodeSnapshot(input: unknown): NodeSnapshot | null {
  if (!isRecord(input)) return null;
  const leftPorts = parseIDArray(input.left_ports);
  const rightPorts = parseIDArray(input.right_ports);
  if (typeof input.class !== "string"
    || typeof input.name !== "string"
    || typeof input.primary_type !== "string"
    || typeof input.label !== "string"
    || typeof input.position !== "string"
    || typeof input.placeholder !== "boolean"
    || typeof input.root !== "boolean"
    || typeof input.has_root_path !== "boolean"
    || !leftPorts
    || !rightPorts) return null;
  const popups = parseNodePopups(input.popups);
  if (popups === undefined && input.popups !== undefined) return null;
  return {
    class: input.class,
    name: input.name,
    primary_type: input.primary_type,
    label: input.label,
    position: input.position,
    ...(popups === undefined ? {} : { popups }),
    placeholder: input.placeholder,
    root: input.root,
    has_root_path: input.has_root_path,
    left_ports: leftPorts,
    right_ports: rightPorts,
  };
}

function parseNodePopups(input: unknown): NodePopup[] | null | undefined {
  if (input === undefined || input === null) return input;
  if (!Array.isArray(input)) return undefined;
  return parseArray(input, parseNodePopup) ?? undefined;
}

function parseNodePopup(input: unknown): NodePopup | null {
  if (!isRecord(input) || !isID(input.id) || !isNodePopupType(input.type) || typeof input.text !== "string") {
    return null;
  }
  return { id: input.id, type: input.type, text: input.text };
}

function parsePortSnapshot(input: unknown): PortSnapshot | null {
  if (!isRecord(input)) return null;
  const types = parseStringArray(input.types);
  const links = parseIDArray(input.links);
  if ((input.direction !== "left" && input.direction !== "right")
    || !isID(input.node)
    || typeof input.name !== "string"
    || !types
    || !links) return null;
  return {
    direction: input.direction,
    node: input.node,
    name: input.name,
    types,
    links,
  };
}

function parseLinkSnapshot(input: unknown): LinkSnapshot | null {
  if (!isRecord(input)
    || typeof input.type !== "string"
    || typeof input.placeholder !== "boolean"
    || !isID(input.left_port)
    || !isID(input.left_port_node)
    || !isID(input.right_port)
    || !isID(input.right_port_node)) return null;
  return {
    type: input.type,
    placeholder: input.placeholder,
    left_port: input.left_port,
    left_port_node: input.left_port_node,
    right_port: input.right_port,
    right_port_node: input.right_port_node,
  };
}

function parseWorkerSnapshot(input: unknown): WorkerSnapshot | null {
  if (!isRecord(input)
    || !isID(input.id)
    || !isID(input.node)
    || typeof input.class !== "string"
    || typeof input.name !== "string"
    || !isOptionalString(input.worker_name)
    || typeof input.started_at !== "string"
    || typeof input.orphan !== "boolean"
    || !isOptionalString(input.error)) return null;
  return {
    id: input.id,
    node: input.node,
    class: input.class,
    name: input.name,
    ...(input.worker_name === undefined ? {} : { worker_name: input.worker_name }),
    started_at: input.started_at,
    orphan: input.orphan,
    ...(input.error === undefined ? {} : { error: input.error }),
  };
}

function parseTypePalette(input: unknown): TypePalette | null {
  if (!isRecord(input)
    || !isOptionalString(input.type)
    || !isOptionalString(input.text)
    || !isOptionalString(input.color)
    || !isOptionalString(input.textColor)) return null;
  return {
    ...(input.type === undefined ? {} : { type: input.type }),
    ...(input.text === undefined ? {} : { text: input.text }),
    ...(input.color === undefined ? {} : { color: input.color }),
    ...(input.textColor === undefined ? {} : { textColor: input.textColor }),
  };
}

function parseArray<T>(input: unknown[], parse: (value: unknown) => T | null): T[] | null {
  const parsed = input.map(parse);
  return parsed.every((value): value is T => value !== null) ? parsed : null;
}

function parseIDRecord<T>(input: unknown, parse: (value: unknown) => T | null): Record<string, T> | null {
  if (!isRecord(input)) return null;
  const parsed: Record<string, T> = {};
  for (const [key, value] of Object.entries(input)) {
    const item = parse(value);
    if (!isIDKey(key) || item === null) return null;
    parsed[key] = item;
  }
  return parsed;
}

function parseStringRecord<T>(input: unknown, parse: (value: unknown) => T | null): Record<string, T> | null {
  if (!isRecord(input)) return null;
  const parsed: Record<string, T> = {};
  for (const [key, value] of Object.entries(input)) {
    const item = parse(value);
    if (isUnsafeRecordKey(key) || item === null) return null;
    parsed[key] = item;
  }
  return parsed;
}

function isUnsafeRecordKey(key: string): boolean {
  return key === "__proto__" || key === "prototype" || key === "constructor";
}

function isWorkspaceNotificationKind(input: unknown): input is WorkspaceNotificationKind {
  switch (input) {
    case "workspace_snapshot":
    case "workspace_stopped":
    case "node_class_added":
    case "node_class_removed":
    case "node_added":
    case "node_updated":
    case "node_removed":
    case "nodes_removed":
    case "nodes_updated":
    case "port_added":
    case "port_updated":
    case "port_removed":
    case "link_added":
    case "link_updated":
    case "link_removed":
    case "worker_spawned":
    case "worker_stopped":
    case "worker_failed":
    case "node_menu":
      return true;
    default:
      return false;
  }
}

function isNodePopupType(input: unknown): input is NodePopupType {
  return input === "info" || input === "ward" || input === "err";
}

function isID(input: unknown): input is ID {
  return typeof input === "number" && Number.isSafeInteger(input) && input >= 0;
}

function isIDKey(input: string): boolean {
  return /^(?:0|[1-9]\d*)$/.test(input) && isID(Number(input));
}

function parseIDArray(input: unknown): ID[] | null {
  if (input === null) return [];
  return Array.isArray(input) && input.every(isID) ? [...input] : null;
}

function parseStringArray(input: unknown): string[] | null {
  if (input === null) return [];
  return Array.isArray(input) && input.every((value): value is string => typeof value === "string")
    ? [...input]
    : null;
}

function isOptionalString(input: unknown): input is string | undefined {
  return input === undefined || typeof input === "string";
}

export function nodeMenuID(nodeID: ID): string {
  return `NODE${nodeID}MENU`;
}

export function parsePosition(raw: string, fallbackX: number, fallbackY: number): [number, number] {
  if (!raw) return [fallbackX, fallbackY];
  try {
    const value = JSON.parse(raw) as { x?: unknown; y?: unknown };
    if (typeof value.x === "number" && typeof value.y === "number") {
      return [value.x, value.y];
    }
  } catch {
    // Positions are deliberately opaque to Pasta; bad frontend state falls back.
  }
  return [fallbackX, fallbackY];
}

export function formatPosition(pos: [number, number]): string {
  return JSON.stringify({ x: Math.round(pos[0]), y: Math.round(pos[1]) });
}

export function isPastaClipboard(data: string): boolean {
  return parsePastaClipboard(data) !== null;
}

export function positionClipboardAt(data: string, target: [number, number]): string {
  const payload = parsePastaClipboard(data);
  if (!payload) return data;
  const positions = payload.nodes
    .map((node) => parseClipboardNodePosition(node.position))
    .filter((position): position is [number, number] => position !== null);
  if (!positions.length) return data;

  const bounds = positions.reduce((box, [x, y]) => ({
    minX: Math.min(box.minX, x),
    minY: Math.min(box.minY, y),
    maxX: Math.max(box.maxX, x),
    maxY: Math.max(box.maxY, y),
  }), {
    minX: Number.POSITIVE_INFINITY,
    minY: Number.POSITIVE_INFINITY,
    maxX: Number.NEGATIVE_INFINITY,
    maxY: Number.NEGATIVE_INFINITY,
  });
  const centerX = bounds.minX + (bounds.maxX - bounds.minX) * 0.5;
  const centerY = bounds.minY + (bounds.maxY - bounds.minY) * 0.5;
  const dx = target[0] - centerX;
  const dy = target[1] - centerY;

  for (const node of payload.nodes) {
    const position = parseClipboardNodePosition(node.position);
    if (!position) continue;
    node.position = formatPosition([position[0] + dx, position[1] + dy]);
  }
  return JSON.stringify(payload);
}

function parsePastaClipboard(data: string): ClipboardPayload | null {
  try {
    const payload: unknown = JSON.parse(data);
    if (!isRecord(payload)) return null;
    if (payload.version !== 1 || !Array.isArray(payload.nodes)) return null;
    if (!payload.nodes.every(isRecord)) return null;
    return { ...payload, version: 1, nodes: payload.nodes };
  } catch {
    return null;
  }
}

function parseClipboardNodePosition(raw: unknown): [number, number] | null {
  if (typeof raw !== "string" || !raw) return null;
  try {
    const position: unknown = JSON.parse(raw);
    if (!isRecord(position)) return null;
    if (typeof position.x !== "number" || typeof position.y !== "number") return null;
    if (!Number.isFinite(position.x) || !Number.isFinite(position.y)) return null;
    return [position.x, position.y];
  } catch {
    return null;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function nodePositionMap(changes: NodePositionChange[]): Record<string, string> {
  const positions: Record<string, string> = {};
  for (const change of changes) {
    if (Number.isFinite(change.id)) positions[String(change.id)] = change.position;
  }
  return positions;
}

export function selectedNodeIDs(selected: Record<string, { id: number }> | null | undefined): number[] {
  if (!selected) return [];
  return Object.values(selected).map((node) => node.id).filter((id) => Number.isFinite(id));
}

export function isKeyboardShortcut(
  event: { ctrlKey: boolean; metaKey: boolean; code: string; key: string },
  code: string,
  key: string,
): boolean {
  return (event.ctrlKey || event.metaKey) && (event.code === code || event.key.toLowerCase() === key);
}

export function typeColor(type: string): TypeColor | null {
  return loadedTypeColors[type] ?? null;
}

export function linkColor(type: string): string {
  return typeColor(type)?.color ?? DEFAULT_LINK_COLOR;
}

export function setTypeColors(colors: Record<string, TypeColor>): void {
  for (const key of Object.keys(loadedTypeColors)) {
    delete loadedTypeColors[key];
  }
  Object.assign(loadedTypeColors, colors);
}

export function latestPriorityPopup(popups: NodePopup[] | null | undefined): NodePopup | null {
  if (!popups?.length) return null;
  let selected = popups[0]!;
  for (const popup of popups.slice(1)) {
    if (popupPriority(popup.type) >= popupPriority(selected.type)) {
      selected = popup;
    }
  }
  return selected;
}

export function trimPopupText(text: string, maxLength = 50): string {
  const chars = Array.from(text);
  if (chars.length <= maxLength) return text;
  if (maxLength <= 3) return chars.slice(0, maxLength).join("");
  return `${chars.slice(0, maxLength - 3).join("")}...`;
}

export function popupPriority(type: string): number {
  switch (type) {
    case "err":
      return 3;
    case "ward":
      return 2;
    case "info":
      return 1;
    default:
      return 0;
  }
}
