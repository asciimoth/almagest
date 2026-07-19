export type KVStoreKind = "default" | "plugin";

export type KVStoreSelector = {
  kind?: KVStoreKind;
  name?: string;
};

export type KVStoreInfo = {
  kind: KVStoreKind;
  name?: string;
};

export type KVEntryKind = "bucket" | "value";

export type KVEntryInfo = {
  kind: KVEntryKind;
  name: string;
  size?: number;
  hidden?: boolean;
  string?: boolean;
};

export type KVEntriesSnapshot = {
  exists: boolean;
  entries?: KVEntryInfo[];
};

export type KVValue = {
  data?: string;
  encoding?: string;
  size?: number;
  hidden?: boolean;
  string?: boolean;
};

export type KVValueSnapshot = {
  found: boolean;
  value?: KVValue;
};

export type KVNodeRef = {
  store: KVStoreInfo;
  path: string[];
};

export const KV_TEXT_RENDER_LIMIT = 32 * 1024;

// Map keys must not depend on path delimiters because plugin names, subtree
// names, and keys are user/plugin-controlled text. JSON tuple keys keep the
// identity reversible without inventing an escaping scheme.
export function kvNodeKey(store: KVStoreSelector, path: string[] = []): string {
  const normalized = normalizeKVStore(store);
  return JSON.stringify([normalized.kind, normalized.name ?? "", path]);
}

export function parseKVNodeKey(key: string): KVNodeRef | null {
  try {
    const parsed = JSON.parse(key) as unknown;
    if (!Array.isArray(parsed) || parsed.length !== 3) return null;
    const [kind, name, path] = parsed;
    if ((kind !== "default" && kind !== "plugin") || typeof name !== "string" || !Array.isArray(path)) return null;
    if (!path.every((segment) => typeof segment === "string")) return null;
    return {
      store: kind === "plugin" ? { kind, name } : { kind: "default" },
      path: [...path],
    };
  } catch {
    return null;
  }
}

export function normalizeKVStore(store: KVStoreSelector): KVStoreInfo {
  if (store.kind === "plugin") return { kind: "plugin", name: store.name ?? "" };
  return { kind: "default" };
}

export function sameKVStore(left: KVStoreSelector, right: KVStoreSelector): boolean {
  const a = normalizeKVStore(left);
  const b = normalizeKVStore(right);
  return a.kind === b.kind && (a.name ?? "") === (b.name ?? "");
}

export function kvNodeDescendsFrom(descendantKey: string, ancestorKey: string): boolean {
  const descendant = parseKVNodeKey(descendantKey);
  const ancestor = parseKVNodeKey(ancestorKey);
  if (!descendant || !ancestor || !sameKVStore(descendant.store, ancestor.store)) return false;
  if (descendant.path.length < ancestor.path.length) return false;
  return ancestor.path.every((segment, index) => descendant.path[index] === segment);
}

export function childKVNodeKey(parentKey: string, name: string): string | null {
  const parent = parseKVNodeKey(parentKey);
  if (!parent) return null;
  return kvNodeKey(parent.store, [...parent.path, name]);
}

// The panel subscribes to entries only for opened tree nodes whose ancestors
// are also opened and present in the cached snapshots. This preserves expanded
// descendants while their parent is folded, but keeps the live backend scope as
// narrow as the content currently visible to the user.
export function desiredKVEntrySubscriptionKeys(
  stores: KVStoreInfo[],
  expanded: ReadonlySet<string>,
  entriesByNode: ReadonlyMap<string, KVEntriesSnapshot>,
): Set<string> {
  const desired = new Set<string>();
  for (const store of stores) {
    collectVisibleExpandedNode(kvNodeKey(store), expanded, entriesByNode, desired);
  }
  return desired;
}

function collectVisibleExpandedNode(
  nodeKey: string,
  expanded: ReadonlySet<string>,
  entriesByNode: ReadonlyMap<string, KVEntriesSnapshot>,
  desired: Set<string>,
): void {
  if (!expanded.has(nodeKey)) return;
  desired.add(nodeKey);
  const snapshot = entriesByNode.get(nodeKey);
  if (!snapshot?.exists) return;
  for (const entry of snapshot.entries ?? []) {
    if (entry.kind !== "bucket") continue;
    const childKey = childKVNodeKey(nodeKey, entry.name);
    if (childKey) collectVisibleExpandedNode(childKey, expanded, entriesByNode, desired);
  }
}

export function formatKVByteSize(size: number | undefined): string {
  if (!Number.isFinite(size) || size === undefined || size < 0) return "unknown size";
  if (size < 1024) return `${size} B`;
  const kib = size / 1024;
  if (kib < 1024) return `${formatFixed(kib)} KiB`;
  const mib = kib / 1024;
  return `${formatFixed(mib)} MiB`;
}

function formatFixed(value: number): string {
  return value >= 10 ? value.toFixed(0) : value.toFixed(1);
}
