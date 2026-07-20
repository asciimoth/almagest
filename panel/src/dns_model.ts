export type DNSCacheInfo = {
  name: string;
};

export type DNSKeyInfo = {
  key: string;
  size?: number;
  expires_at?: string;
};

export type DNSKeysSnapshot = {
  exists: boolean;
  keys?: DNSKeyInfo[];
};

export type DNSValue = {
  json?: string;
  size?: number;
  expires_at?: string;
};

export type DNSValueSnapshot = {
  found: boolean;
  value?: DNSValue;
};

export type DNSCacheChange = {
  name: string;
  deleted?: boolean;
};

export type DNSKeyChange = {
  key: string;
  deleted?: boolean;
  entry?: DNSKeyInfo;
};

export type DNSCacheNodeRef = {
  cache: string;
};

export const DNS_JSON_RENDER_LIMIT = 64 * 1024;

// Cache names come from node/runtime configuration, so use a reversible JSON
// tuple for tree keys instead of delimiter parsing. This mirrors the KV panel's
// key discipline and keeps frontend subscription state robust for odd names.
export function dnsCacheNodeKey(cache: DNSCacheInfo | string): string {
  return JSON.stringify([typeof cache === "string" ? cache : cache.name]);
}

export function parseDNSCacheNodeKey(key: string): DNSCacheNodeRef | null {
  try {
    const parsed = JSON.parse(key) as unknown;
    if (!Array.isArray(parsed) || parsed.length !== 1 || typeof parsed[0] !== "string") return null;
    return { cache: parsed[0] };
  } catch {
    return null;
  }
}

// Only expanded cache rows need live key subscriptions. Folded cache snapshots
// remain cached for quick reopening, but their backend subscriptions are
// released by the tab controller.
export function desiredDNSKeySubscriptionKeys(
  caches: DNSCacheInfo[],
  expanded: ReadonlySet<string>,
): Set<string> {
  const desired = new Set<string>();
  for (const cache of caches) {
    const nodeKey = dnsCacheNodeKey(cache);
    if (expanded.has(nodeKey)) desired.add(nodeKey);
  }
  return desired;
}

export function applyDNSCacheChange(caches: DNSCacheInfo[], change: DNSCacheChange): DNSCacheInfo[] {
  const next = caches.filter((cache) => cache.name !== change.name);
  if (!change.deleted) next.push({ name: change.name });
  return sortDNSCaches(next);
}

export function sortDNSCaches(caches: DNSCacheInfo[]): DNSCacheInfo[] {
  return [...caches].sort((left, right) => left.name.localeCompare(right.name));
}

export function applyDNSKeyChange(snapshot: DNSKeysSnapshot, change: DNSKeyChange): DNSKeysSnapshot {
  const keys = (snapshot.keys ?? []).filter((key) => key.key !== change.key);
  if (!change.deleted && change.entry) keys.push(change.entry);
  return {
    exists: snapshot.exists,
    keys: sortDNSKeys(keys),
  };
}

export function sortDNSKeys(keys: DNSKeyInfo[]): DNSKeyInfo[] {
  return [...keys].sort((left, right) => left.key.localeCompare(right.key));
}

export function formatDNSByteSize(size: number | undefined): string {
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
