import type { DNSValueSnapshot, DNSKeysSnapshot, DNSCacheInfo } from "./dns_model.js";
import {
  parseDNSCachesSnapshot,
  parseDNSKeysSnapshot,
  parseDNSValueSnapshot,
  parseKVEntriesSnapshot,
  parseKVStoresSnapshot,
  parseKVValueSnapshot,
  parseLogEntry,
  parseProjectVersionStatus,
  parseSecretEntry,
  parseSecretsSnapshot,
  parseTLSBuiltinCAInfo,
  parseTLSCertEntry,
  parseTLSPolicyEntry,
  parseTLSSnapshot,
  type LogEntry,
  type ProjectVersionStatus,
} from "./event_model.js";
import type { KVEntriesSnapshot, KVStoreInfo, KVValueSnapshot } from "./kv_model.js";
import type { SecretEntry, SecretsSnapshot } from "./secrets_model.js";
import type { SnapshotInfo } from "./snapshots_panel.js";
import type { TLSBuiltinCAInfo, TLSCertEntry, TLSPolicyEntry, TLSSnapshot } from "./tls_model.js";

export type HelloResponse = {
  version: string;
  daemon?: string;
  execution_mode: "direct" | "isolation" | "isolation-development";
  isolation?: IsolationStatus;
  project_version_status: ProjectVersionStatus;
  surfaces?: Record<string, unknown>;
};

export type IsolationStatus = {
  protocol_version: number;
  restrictions: Record<string, string[]>;
  roles: Record<string, string>;
  capabilities: Record<string, boolean>;
  recovery_state: string;
};

export type SubscribeLogsResponse = {
  subscription_id: number;
  cache_size: number;
  entries: LogEntry[];
};

export type DNSCachesSnapshot = {
  caches?: DNSCacheInfo[];
};

export type DNSSubscribeCachesResponse = SubscriptionResponse<DNSCachesSnapshot>;
export type DNSSubscribeKeysResponse = SubscriptionResponse<DNSKeysSnapshot>;
export type DNSSubscribeValueResponse = SubscriptionResponse<DNSValueSnapshot>;

export type DNSSetResponse = {
  key: string;
  snapshot: DNSValueSnapshot;
};

export type KVStoresSnapshot = {
  stores: KVStoreInfo[];
};

export type KVSubscribeStoresResponse = SubscriptionResponse<KVStoresSnapshot>;
export type KVSubscribeEntriesResponse = SubscriptionResponse<KVEntriesSnapshot>;
export type KVSubscribeValueResponse = SubscriptionResponse<KVValueSnapshot>;
export type SecretsSubscribeResponse = SubscriptionResponse<SecretsSnapshot>;
export type TLSSubscribeResponse = SubscriptionResponse<TLSSnapshot>;

export type ConfigSnapshot = {
  text: string;
  revision: string;
  read_only: boolean;
};

export type ConfigChanges = {
  added?: string[];
  removed?: string[];
  reinitialized?: string[];
  positions_updated?: string[];
  primary_types_updated?: string[];
  links_added?: string[];
  links_removed?: string[];
};

export type ConfigUpdateResponse = {
  snapshot: ConfigSnapshot;
  changes: ConfigChanges;
};

export type SnapshotsListResponse = {
  snapshots: SnapshotInfo[];
};

export type SnapshotExportResponse = {
  snapshot: SnapshotInfo;
  archive: string;
};

export type SnapshotRestoreResponse = {
  snapshot: SnapshotInfo;
  config_written: boolean;
  config_write_error?: string;
};

type SubscriptionResponse<T> = {
  subscription_id: number;
  snapshot: T;
};

export function parseHelloResponse(input: unknown): HelloResponse | null {
  if (!isRecord(input) || typeof input.version !== "string" || !isOptionalString(input.daemon)
    || !isExecutionMode(input.execution_mode)) return null;
  const status = parseProjectVersionStatus(input.project_version_status);
  const isolation = input.isolation === undefined ? undefined : parseIsolationStatus(input.isolation);
  if (!status || (input.surfaces !== undefined && !isRecord(input.surfaces))
    || (input.isolation !== undefined && !isolation)
    || (input.execution_mode === "direct") !== (isolation === undefined)) return null;
  return {
    version: input.version,
    ...(input.daemon === undefined ? {} : { daemon: input.daemon }),
    execution_mode: input.execution_mode,
    project_version_status: status,
    ...(isolation ? { isolation } : {}),
    ...(input.surfaces === undefined ? {} : { surfaces: { ...input.surfaces } }),
  };
}

function isExecutionMode(input: unknown): input is HelloResponse["execution_mode"] {
  return input === "direct" || input === "isolation" || input === "isolation-development";
}

function parseIsolationStatus(input: unknown): IsolationStatus | null {
  if (!isRecord(input) || !Number.isSafeInteger(input.protocol_version) || Number(input.protocol_version) <= 0
    || !isRecord(input.restrictions) || !isRecord(input.roles) || !isRecord(input.capabilities)
    || typeof input.recovery_state !== "string" || input.recovery_state.length === 0) return null;
  const restrictions: Record<string, string[]> = {};
  for (const [role, values] of Object.entries(input.restrictions)) {
    if (!Array.isArray(values) || !values.every((value) => typeof value === "string")) return null;
    restrictions[role] = [...values];
  }
  const roles: Record<string, string> = {};
  for (const [role, value] of Object.entries(input.roles)) {
    if (typeof value !== "string") return null;
    roles[role] = value;
  }
  const capabilities: Record<string, boolean> = {};
  for (const [name, value] of Object.entries(input.capabilities)) {
    if (typeof value !== "boolean") return null;
    capabilities[name] = value;
  }
  return { protocol_version: Number(input.protocol_version), restrictions, roles, capabilities,
    recovery_state: input.recovery_state };
}

export function parseSubscribeLogsResponse(input: unknown): SubscribeLogsResponse | null {
  if (!isRecord(input)
    || !isID(input.subscription_id)
    || !isNonNegativeInteger(input.cache_size)
    || !Array.isArray(input.entries)) return null;
  const entries = input.entries.map(parseLogEntry);
  return entries.every((entry): entry is LogEntry => entry !== null)
    ? { subscription_id: input.subscription_id, cache_size: input.cache_size, entries }
    : null;
}

export function parseSubscriptionIDResponse(input: unknown): { subscription_id: number } | null {
  return isRecord(input) && isID(input.subscription_id)
    ? { subscription_id: input.subscription_id }
    : null;
}

export function parseDNSSubscribeCachesResponse(input: unknown): DNSSubscribeCachesResponse | null {
  return parseSubscriptionResponse(input, parseDNSCachesSnapshot);
}

export function parseDNSSubscribeKeysResponse(input: unknown): DNSSubscribeKeysResponse | null {
  return parseSubscriptionResponse(input, parseDNSKeysSnapshot);
}

export function parseDNSSubscribeValueResponse(input: unknown): DNSSubscribeValueResponse | null {
  return parseSubscriptionResponse(input, parseDNSValueSnapshot);
}

export function parseDNSSetResponse(input: unknown): DNSSetResponse | null {
  if (!isRecord(input) || typeof input.key !== "string") return null;
  const snapshot = parseDNSValueSnapshot(input.snapshot);
  return snapshot ? { key: input.key, snapshot } : null;
}

export function parseKVSubscribeStoresResponse(input: unknown): KVSubscribeStoresResponse | null {
  return parseSubscriptionResponse(input, parseKVStoresSnapshot);
}

export function parseKVSubscribeEntriesResponse(input: unknown): KVSubscribeEntriesResponse | null {
  return parseSubscriptionResponse(input, parseKVEntriesSnapshot);
}

export function parseKVSubscribeValueResponse(input: unknown): KVSubscribeValueResponse | null {
  return parseSubscriptionResponse(input, parseKVValueSnapshot);
}

export function parseSecretsSubscribeResponse(input: unknown): SecretsSubscribeResponse | null {
  return parseSubscriptionResponse(input, parseSecretsSnapshot);
}

export function parseTLSSubscribeResponse(input: unknown): TLSSubscribeResponse | null {
  return parseSubscriptionResponse(input, parseTLSSnapshot);
}

export function parseSecretEntryResponse(input: unknown): SecretEntry | null {
  return parseSecretEntry(input);
}

export function parseTLSBuiltinCAResponse(input: unknown): TLSBuiltinCAInfo | null {
  return parseTLSBuiltinCAInfo(input);
}

export function parseTLSCertResponse(input: unknown): TLSCertEntry | null {
  return parseTLSCertEntry(input);
}

export function parseTLSPolicyResponse(input: unknown): TLSPolicyEntry | null {
  return parseTLSPolicyEntry(input);
}

export function parseTLSSnapshotResponse(input: unknown): TLSSnapshot | null {
  return parseTLSSnapshot(input);
}

export function parseConfigSnapshot(input: unknown): ConfigSnapshot | null {
  if (!isRecord(input)
    || typeof input.text !== "string"
    || typeof input.revision !== "string"
    || typeof input.read_only !== "boolean") return null;
  return { text: input.text, revision: input.revision, read_only: input.read_only };
}

export function parseConfigUpdateResponse(input: unknown): ConfigUpdateResponse | null {
  if (!isRecord(input)) return null;
  const snapshot = parseConfigSnapshot(input.snapshot);
  const changes = parseConfigChanges(input.changes);
  return snapshot && changes ? { snapshot, changes } : null;
}

export function parseSnapshotsListResponse(input: unknown): SnapshotsListResponse | null {
  if (!isRecord(input) || !Array.isArray(input.snapshots)) return null;
  const snapshots = input.snapshots.map(parseSnapshotInfo);
  return snapshots.every((snapshot): snapshot is SnapshotInfo => snapshot !== null) ? { snapshots } : null;
}

export function parseSnapshotInfoResponse(input: unknown): SnapshotInfo | null {
  return parseSnapshotInfo(input);
}

export function parseSnapshotExportResponse(input: unknown): SnapshotExportResponse | null {
  if (!isRecord(input) || typeof input.archive !== "string") return null;
  const snapshot = parseSnapshotInfo(input.snapshot);
  return snapshot ? { snapshot, archive: input.archive } : null;
}

export function parseSnapshotRestoreResponse(input: unknown): SnapshotRestoreResponse | null {
  if (!isRecord(input)
    || typeof input.config_written !== "boolean"
    || !isOptionalString(input.config_write_error)) return null;
  const snapshot = parseSnapshotInfo(input.snapshot);
  if (!snapshot) return null;
  return {
    snapshot,
    config_written: input.config_written,
    ...(input.config_write_error === undefined ? {} : { config_write_error: input.config_write_error }),
  };
}

export function parseDescriptionResponse(input: unknown): string | null {
  return isRecord(input) && typeof input.description === "string" ? input.description : null;
}

export function parseIDResponse(input: unknown): number | null {
  return isRecord(input) && isID(input.id) ? input.id : null;
}

export function parseDataResponse(input: unknown): string | null {
  return isRecord(input) && typeof input.data === "string" ? input.data : null;
}

export function parseIDsResponse(input: unknown): number[] | null {
  return isRecord(input) && Array.isArray(input.ids) && input.ids.every(isID) ? [...input.ids] : null;
}

function parseSubscriptionResponse<T>(
  input: unknown,
  parseSnapshot: (value: unknown) => T | null,
): SubscriptionResponse<T> | null {
  if (!isRecord(input) || !isID(input.subscription_id)) return null;
  const snapshot = parseSnapshot(input.snapshot);
  return snapshot === null ? null : { subscription_id: input.subscription_id, snapshot };
}

function parseConfigChanges(input: unknown): ConfigChanges | null {
  if (!isRecord(input)) return null;
  const fields = [
    "added",
    "removed",
    "reinitialized",
    "positions_updated",
    "primary_types_updated",
    "links_added",
    "links_removed",
  ] as const;
  const changes: ConfigChanges = {};
  for (const field of fields) {
    const value = input[field];
    if (value === undefined) continue;
    if (!isStringArray(value)) return null;
    changes[field] = [...value];
  }
  return changes;
}

function parseSnapshotInfo(input: unknown): SnapshotInfo | null {
  if (!isRecord(input)
    || !isUUID(input.uuid)
    || !isFiniteNumber(input.timestamp)
    || !isOptionalString(input.name)) return null;
  return {
    uuid: input.uuid,
    timestamp: input.timestamp,
    ...(input.name === undefined ? {} : { name: input.name }),
  };
}

function isUUID(input: unknown): input is string {
  return typeof input === "string"
    && /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(input);
}

function isID(input: unknown): input is number {
  return typeof input === "number" && Number.isSafeInteger(input) && input >= 0;
}

function isNonNegativeInteger(input: unknown): input is number {
  return typeof input === "number" && Number.isSafeInteger(input) && input >= 0;
}

function isFiniteNumber(input: unknown): input is number {
  return typeof input === "number" && Number.isFinite(input);
}

function isStringArray(input: unknown): input is string[] {
  return Array.isArray(input) && input.every((value): value is string => typeof value === "string");
}

function isOptionalString(input: unknown): input is string | undefined {
  return input === undefined || typeof input === "string";
}

function isRecord(input: unknown): input is Record<string, unknown> {
  return typeof input === "object" && input !== null && !Array.isArray(input);
}
