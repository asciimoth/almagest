export type ID = number;

export type NodeClassSnapshot = {
  class: string;
  short_description: string;
  unique: boolean;
  primary_type: string;
  initial_ports: Array<{ direction: string; name: string; types: string[] }>;
};

export type NodePopup = {
  id: ID;
  type: "info" | "ward" | "err" | string;
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
  classes: Record<string, NodeClassSnapshot>;
  nodes: Record<string, NodeSnapshot>;
  ports: Record<string, PortSnapshot>;
  links: Record<string, LinkSnapshot>;
  workers: Record<string, WorkerSnapshot>;
};

export type WorkspaceNotification = {
  kind: string;
  id?: ID;
  class_name?: string;
  snapshot?: WorkspaceSnapshot;
  node_class?: NodeClassSnapshot;
  node?: NodeSnapshot;
  port?: PortSnapshot;
  link?: LinkSnapshot;
  worker?: WorkerSnapshot;
  formular?: unknown;
};

export type WorkerNotification = {
  kind: "worker_snapshot" | "worker_spawned" | "worker_stopped" | "worker_failed" | string;
  id?: ID;
  snapshot?: Record<string, WorkerSnapshot>;
  worker?: WorkerSnapshot;
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
  return { classes: {}, nodes: {}, ports: {}, links: {}, workers: {} };
}

export function cloneSnapshot(snapshot: WorkspaceSnapshot): WorkspaceSnapshot {
  const cloned = JSON.parse(JSON.stringify(snapshot)) as WorkspaceSnapshot;
  cloned.workers ??= {};
  return cloned;
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
        next.classes[notification.class_name] = notification.node_class;
      }
      break;
    case "node_class_removed":
      if (notification.class_name) delete next.classes[notification.class_name];
      break;
    case "node_added":
    case "node_updated":
      if (id && notification.node) next.nodes[id] = notification.node;
      break;
    case "node_removed":
      if (id) delete next.nodes[id];
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
  incoming: WorkspaceSnapshot,
): WorkspaceSnapshot {
  const next = cloneSnapshot(incoming);
  if (!Object.prototype.hasOwnProperty.call(incoming, "workers")) {
    next.workers = cloneSnapshot(current).workers;
  }
  return next;
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
  let selected = popups[0];
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
