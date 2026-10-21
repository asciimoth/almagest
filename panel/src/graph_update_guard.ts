import type { NodeSnapshot } from "./pasta_model.js";

type LiteGraphNode = {
  id?: unknown;
};

type LiteGraphGroup = {
  _nodes?: LiteGraphNode[];
};

type LiteGraphInteractionState = {
  pointer_is_down?: boolean;
  node_dragged?: LiteGraphNode | null;
  resizing_node?: LiteGraphNode | null;
  connecting_node?: LiteGraphNode | null;
  node_capturing_input?: LiteGraphNode | null;
  node_widget?: [LiteGraphNode?] | null;
  selected_nodes?: Record<string, LiteGraphNode>;
  selected_group?: LiteGraphGroup | null;
};

/**
 * Reports whether replacing graph objects would interrupt an active pointer
 * action. A removed interaction node is the exception: LiteGraph must stop
 * using that stale object immediately.
 */
export function shouldDeferGraphUpdate(
  canvas: LiteGraphInteractionState | null | undefined,
  currentNodeIDs: Iterable<string>,
): boolean {
  if (!canvas || !hasPointerInteraction(canvas)) return false;

  const available = new Set(currentNodeIDs);
  for (const id of interactionNodeIDs(canvas)) {
    if (!available.has(id)) return false;
  }
  return true;
}

/** Keep a local move authoritative until the daemon acknowledges its position. */
export function preservePendingNodePosition(
  id: number,
  node: NodeSnapshot,
  pending: Map<number, string>,
): NodeSnapshot {
  const position = pending.get(id);
  if (position === undefined) return node;
  if (node.position === position) {
    pending.delete(id);
    return node;
  }
  return { ...node, position };
}

/**
 * Applies local positions to a partial or complete node update. For a complete
 * update, pending entries for deleted nodes are also discarded.
 */
export function preservePendingNodePositions(
  nodes: Record<string, NodeSnapshot>,
  pending: Map<number, string>,
  complete = false,
): Record<string, NodeSnapshot> {
  let result: Record<string, NodeSnapshot> | null = null;
  for (const [idText, node] of Object.entries(nodes)) {
    const id = Number(idText);
    if (!Number.isFinite(id)) continue;
    const preserved = preservePendingNodePosition(id, node, pending);
    if (preserved === node) continue;
    result ??= { ...nodes };
    result[idText] = preserved;
  }

  if (complete) {
    const received = new Set(Object.keys(nodes));
    for (const id of pending.keys()) {
      if (!received.has(String(id))) pending.delete(id);
    }
  }
  return result ?? nodes;
}

function hasPointerInteraction(canvas: LiteGraphInteractionState): boolean {
  return Boolean(
    canvas.pointer_is_down
    || canvas.node_dragged
    || canvas.resizing_node
    || canvas.connecting_node
    || canvas.node_capturing_input
    || canvas.node_widget
    || canvas.selected_group,
  );
}

function interactionNodeIDs(canvas: LiteGraphInteractionState): Set<string> {
  const ids = new Set<string>();
  addNodeID(ids, canvas.node_dragged);
  addNodeID(ids, canvas.resizing_node);
  addNodeID(ids, canvas.connecting_node);
  addNodeID(ids, canvas.node_capturing_input);
  addNodeID(ids, canvas.node_widget?.[0]);
  for (const node of canvas.selected_group?._nodes ?? []) addNodeID(ids, node);

  // LiteGraph moves every selected node when node_dragged is set.
  if (canvas.node_dragged) {
    for (const node of Object.values(canvas.selected_nodes ?? {})) addNodeID(ids, node);
  }
  return ids;
}

function addNodeID(ids: Set<string>, node: LiteGraphNode | null | undefined): void {
  if (!node) return;
  if (typeof node.id === "number" && Number.isFinite(node.id)) ids.add(String(node.id));
  if (typeof node.id === "string" && node.id) ids.add(node.id);
}
