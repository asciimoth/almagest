import { text as localizedText } from "./localization.js";

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

export type DNSRecordInput = {
  name: string;
  type: string;
  class: string;
  ttl: number;
  value: string;
};

export type DNSRecordSection = {
  title: string;
  records: DNSDisplayRecord[];
};

export type DNSDisplayRecord = {
  name: string;
  type: string;
  class: string;
  ttl?: number;
  value?: string;
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
export const DNS_RECORD_TYPES = ["A", "AAAA", "CNAME", "NS", "PTR", "MX", "TXT", "SRV"] as const;
export const DNS_RECORD_CLASSES = ["IN", "CH", "HS"] as const;

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

export function formatDNSCacheKey(key: string): string {
  const record = dnsQuestionFromCacheKey(key);
  return record ? `${record.name} ${record.class} ${record.type}` : key;
}

export function dnsQuestionFromCacheKey(key: string): DNSDisplayRecord | null {
  const parts = key.split("|");
  if (parts.length !== 3) return null;
  const type = parseDNSInteger(parts[1]);
  const cls = parseDNSInteger(parts[2]);
  if (type === null || cls === null) return null;
  return {
    name: parts[0] ?? "",
    type: dnsTypeName(type),
    class: dnsClassName(cls),
  };
}

export function dnsRecordsFromValue(value: DNSValue): DNSRecordSection[] | null {
  const text = value.json ?? "";
  if (!text) return null;
  const entry = parseDNSCacheEntry(text);
  if (!entry || !entry.message) return null;
  const msg = entry.message;
  const sections: DNSRecordSection[] = [];
  appendDNSSection(sections, localizedText("dns.section.questions", "Questions"), (msg.questions ?? []).map(dnsQuestionToDisplayRecord));
  appendDNSSection(sections, localizedText("dns.section.answers", "Answers"), (msg.answers ?? []).map(dnsResourceToDisplayRecord));
  appendDNSSection(sections, localizedText("dns.section.authorities", "Authorities"), (msg.authorities ?? []).map(dnsResourceToDisplayRecord));
  appendDNSSection(sections, localizedText("dns.section.additionals", "Additionals"), (msg.additionals ?? []).map(dnsResourceToDisplayRecord));
  return sections;
}

export function defaultDNSRecordInput(): DNSRecordInput {
  return {
    name: "",
    type: "A",
    class: "IN",
    ttl: 60,
    value: "",
  };
}

export function editableDNSRecordFromValue(key: string, value: DNSValue | undefined): DNSRecordInput {
  const fallback = dnsQuestionFromCacheKey(key);
  const sections = value ? dnsRecordsFromValue(value) : null;
  const answer = sections?.find((section) => section.title === localizedText("dns.section.answers", "Answers"))?.records[0];
  const question = sections?.find((section) => section.title === localizedText("dns.section.questions", "Questions"))?.records[0];
  return {
    name: answer?.name || question?.name || fallback?.name || "",
    type: supportedDNSRecordType(answer?.type || question?.type || fallback?.type),
    class: supportedDNSRecordClass(answer?.class || question?.class || fallback?.class),
    ttl: answer?.ttl && answer.ttl > 0 ? answer.ttl : 60,
    value: answer?.value ?? "",
  };
}

export function formatDNSByteSize(size: number | undefined): string {
  if (!Number.isFinite(size) || size === undefined || size < 0) return localizedText("common.unknown_size", "unknown size");
  if (size < 1024) return `${size} B`;
  const kib = size / 1024;
  if (kib < 1024) return `${formatFixed(kib)} KiB`;
  const mib = kib / 1024;
  return `${formatFixed(mib)} MiB`;
}

function formatFixed(value: number): string {
  return value >= 10 ? value.toFixed(0) : value.toFixed(1);
}

type DNSCacheEntryJSON = {
  message?: DNSMessageJSON;
};

type DNSMessageJSON = {
  questions?: DNSQuestionJSON[];
  answers?: DNSResourceJSON[];
  authorities?: DNSResourceJSON[];
  additionals?: DNSResourceJSON[];
};

type DNSQuestionJSON = {
  name?: unknown;
  type?: unknown;
  class?: unknown;
};

type DNSResourceJSON = DNSQuestionJSON & {
  ttl?: unknown;
  data?: unknown;
};

function parseDNSCacheEntry(text: string): DNSCacheEntryJSON | null {
  try {
    const parsed = JSON.parse(text) as unknown;
    if (!isRecord(parsed)) return null;
    const message = isRecord(parsed.message) ? parseDNSMessage(parsed.message) : undefined;
    return message === undefined ? {} : { message };
  } catch {
    return null;
  }
}

function parseDNSMessage(input: Record<string, unknown>): DNSMessageJSON {
  return {
    questions: parseDNSArray(input.questions),
    answers: parseDNSArray(input.answers),
    authorities: parseDNSArray(input.authorities),
    additionals: parseDNSArray(input.additionals),
  };
}

function parseDNSArray(input: unknown): DNSResourceJSON[] {
  return Array.isArray(input) ? input.filter(isRecord) : [];
}

function dnsQuestionToDisplayRecord(q: DNSQuestionJSON): DNSDisplayRecord {
  const type = parseDNSInteger(q.type);
  const cls = parseDNSInteger(q.class);
  return {
    name: stringValue(q.name),
    type: type === null ? "TYPE ?" : dnsTypeName(type),
    class: cls === null ? "CLASS ?" : dnsClassName(cls),
  };
}

function dnsResourceToDisplayRecord(rr: DNSResourceJSON): DNSDisplayRecord {
  const type = parseDNSInteger(rr.type);
  const cls = parseDNSInteger(rr.class);
  const data = typeof rr.data === "string" ? decodeBase64(rr.data) : null;
  const ttl = parseDNSInteger(rr.ttl);
  return {
    name: stringValue(rr.name),
    type: type === null ? "TYPE ?" : dnsTypeName(type),
    class: cls === null ? "CLASS ?" : dnsClassName(cls),
    ...(ttl === null ? {} : { ttl }),
    value: type === null || data === null ? "" : dnsResourceValue(type, data),
  };
}

function appendDNSSection(sections: DNSRecordSection[], title: string, records: DNSDisplayRecord[]): void {
  if (records.length) sections.push({ title, records });
}

function dnsResourceValue(type: number, data: number[]): string {
  switch (type) {
    case 1:
      if (data.length === 4) return data.join(".");
      break;
    case 28:
      if (data.length === 16) return formatIPv6(data);
      break;
    case 5:
    case 2:
    case 12:
    case 16:
      return bytesToText(data);
    case 15:
      if (data.length >= 2) return `${uint16(data, 0)} ${bytesToText(data.slice(2))}`;
      break;
    case 33:
      if (data.length >= 6) return `${uint16(data, 0)} ${uint16(data, 2)} ${uint16(data, 4)} ${bytesToText(data.slice(6))}`;
      break;
  }
  return `0x${data.map((byte) => byte.toString(16).padStart(2, "0")).join("")}`;
}

function dnsTypeName(type: number): string {
  switch (type) {
    case 1:
      return "A";
    case 28:
      return "AAAA";
    case 5:
      return "CNAME";
    case 2:
      return "NS";
    case 12:
      return "PTR";
    case 15:
      return "MX";
    case 16:
      return "TXT";
    case 33:
      return "SRV";
    default:
      return `TYPE ${type}`;
  }
}

function dnsClassName(cls: number): string {
  switch (cls) {
    case 1:
      return "IN";
    case 3:
      return "CH";
    case 4:
      return "HS";
    default:
      return `CLASS ${cls}`;
  }
}

function supportedDNSRecordType(value: string | undefined): string {
  return value && DNS_RECORD_TYPES.includes(value as (typeof DNS_RECORD_TYPES)[number]) ? value : "A";
}

function supportedDNSRecordClass(value: string | undefined): string {
  return value && DNS_RECORD_CLASSES.includes(value as (typeof DNS_RECORD_CLASSES)[number]) ? value : "IN";
}

function formatIPv6(data: number[]): string {
  const words: number[] = [];
  for (let i = 0; i < data.length; i += 2) {
    words.push(uint16(data, i));
  }

  let bestStart = -1;
  let bestLength = 0;
  for (let i = 0; i < words.length;) {
    if (words[i] !== 0) {
      i++;
      continue;
    }
    let end = i + 1;
    while (end < words.length && words[end] === 0) end++;
    const length = end - i;
    if (length > bestLength && length >= 2) {
      bestStart = i;
      bestLength = length;
    }
    i = end;
  }

  if (bestStart < 0) return words.map((word) => word.toString(16)).join(":");
  const left = words.slice(0, bestStart).map((word) => word.toString(16)).join(":");
  const right = words.slice(bestStart + bestLength).map((word) => word.toString(16)).join(":");
  if (!left && !right) return "::";
  if (!left) return `::${right}`;
  if (!right) return `${left}::`;
  return `${left}::${right}`;
}

function decodeBase64(input: string): number[] | null {
  try {
    const decoded = globalThis.atob(input);
    const out: number[] = [];
    for (let i = 0; i < decoded.length; i++) out.push(decoded.charCodeAt(i));
    return out;
  } catch {
    return null;
  }
}

function bytesToText(data: number[]): string {
  return new TextDecoder().decode(new Uint8Array(data));
}

function uint16(data: number[], offset: number): number {
  return (data[offset] ?? 0) * 256 + (data[offset + 1] ?? 0);
}

function parseDNSInteger(value: unknown): number | null {
  if (typeof value === "number" && Number.isInteger(value) && value >= 0) return value;
  if (typeof value !== "string" || !/^\d+$/.test(value)) return null;
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) ? parsed : null;
}

function stringValue(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}
