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
  subprogram_sandbox?: SubprogramSandboxStatus;
  project_version_status: ProjectVersionStatus;
  surfaces?: Record<string, unknown>;
};

export type IsolationStatus = {
  protocol_version: number;
  status_schema_version?: number;
  restrictions: Record<string, string[]>;
  roles: Record<string, string>;
  capabilities: Record<string, boolean>;
  recovery_state: string;
  requested_policy?: "strict" | "auto" | "off";
  profile_id?: "linux-full-v1" | "linux-user-v1" | "windows-full-v1";
  profile_revision?: number;
  verification_state?: "verified" | "degraded" | "failed" | "unknown";
  reduced?: boolean;
  mechanisms?: Record<string, Record<string, IsolationMechanism>>;
  credentials?: Record<string, IsolationCredentials>;
  capability_sets?: Record<string, IsolationCapabilitySets>;
  resource_limits?: Record<string, IsolationResourceLimit[]>;
  warnings?: IsolationWarning[];
  observation_time?: string;
  selected_plan_digest?: string;
};

export type IsolationMechanism = { state: "applied" | "unavailable" | "not_requested" | "not_applicable" | "unknown"; reason?: string };
export type IsolationCredentials = {
  real_uid: number; effective_uid: number; saved_uid: number; filesystem_uid: number;
  real_gid: number; effective_gid: number; saved_gid: number; filesystem_gid: number;
  supplementary_groups: number[];
};
export type IsolationCapabilitySets = {
  inheritable: string[]; permitted: string[]; effective: string[]; bounding: string[]; ambient: string[];
};
export type IsolationResourceLimit = {
  name: string; requested: string; effective: string; unit: string; scope: string; source: string; enforcement: string;
};
export type IsolationWarning = { role: "network" | "runtime" | "config_writer" | "command_helper" | "sandbox_supervisor"; code: string; effect: string; feature: string };

export type SubprogramSandboxStatus = {
  mode: "off" | "auto" | "strict";
  platform: string;
  effective_identity: string;
  templates: Record<string, SubprogramTemplateStatus>;
  limits: Record<string, number>;
  active_owners: number;
  orphaned_owners: number;
  quarantined_owners: number;
};

export type SubprogramTemplateStatus = {
  profile: string;
  effective_identity: string;
  strict_satisfied: boolean;
  full_boundary: boolean;
  features: { feature: string; state: "applied" | "omitted-auto" | "excluded-os" | "off"; reason: string }[];
  warnings: string[];
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
  const sandbox = input.subprogram_sandbox === undefined ? undefined : parseSubprogramSandboxStatus(input.subprogram_sandbox);
  if (!status || (input.surfaces !== undefined && !isRecord(input.surfaces))
    || (input.isolation !== undefined && !isolation)
    || (input.subprogram_sandbox !== undefined && !sandbox)
    || (input.execution_mode === "direct") !== (isolation === undefined)) return null;
  return {
    version: input.version,
    ...(input.daemon === undefined ? {} : { daemon: input.daemon }),
    execution_mode: input.execution_mode,
    project_version_status: status,
    ...(isolation ? { isolation } : {}),
    ...(sandbox ? { subprogram_sandbox: sandbox } : {}),
    ...(input.surfaces === undefined ? {} : { surfaces: { ...input.surfaces } }),
  };
}

function parseSubprogramSandboxStatus(input: unknown): SubprogramSandboxStatus | null {
  if (!isRecord(input) || (input.mode !== "off" && input.mode !== "auto" && input.mode !== "strict")
    || typeof input.platform !== "string" || typeof input.effective_identity !== "string"
    || !isRecord(input.templates) || !isRecord(input.limits)
    || !isCount(input.active_owners) || !isCount(input.orphaned_owners) || !isCount(input.quarantined_owners)) return null;
  const limits: Record<string, number> = {};
  const rawLimits = Object.entries(input.limits);
  const rawTemplates = Object.entries(input.templates);
  if (rawLimits.length > 32 || rawTemplates.length > 1024) return null;
  for (const [name, value] of rawLimits) {
    if (!isCount(value)) return null;
    limits[name] = Number(value);
  }
  const templates: Record<string, SubprogramTemplateStatus> = {};
  for (const [name, raw] of rawTemplates) {
    if (!isRecord(raw) || typeof raw.profile !== "string" || typeof raw.effective_identity !== "string" || typeof raw.strict_satisfied !== "boolean"
      || typeof raw.full_boundary !== "boolean" || !Array.isArray(raw.features)) return null;
    const rawWarnings = raw.warnings === null ? [] : raw.warnings;
    if (!Array.isArray(rawWarnings) || raw.features.length > 32 || rawWarnings.length > 32
      || !rawWarnings.every((warning) => typeof warning === "string" && warning.length <= 1024)) return null;
    const features: SubprogramTemplateStatus["features"] = [];
    for (const feature of raw.features) {
      if (!isRecord(feature) || typeof feature.feature !== "string" || typeof feature.reason !== "string"
        || (feature.state !== "applied" && feature.state !== "omitted-auto" && feature.state !== "excluded-os" && feature.state !== "off")) return null;
      features.push({ feature: feature.feature, state: feature.state, reason: feature.reason });
    }
    templates[name] = { profile: raw.profile, effective_identity: raw.effective_identity, strict_satisfied: raw.strict_satisfied,
      full_boundary: raw.full_boundary, features, warnings: [...rawWarnings] };
  }
  return { mode: input.mode, platform: input.platform, effective_identity: input.effective_identity,
    templates, limits, active_owners: Number(input.active_owners), orphaned_owners: Number(input.orphaned_owners),
    quarantined_owners: Number(input.quarantined_owners) };
}

function isCount(input: unknown): boolean {
  return Number.isSafeInteger(input) && Number(input) >= 0;
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
  const result: IsolationStatus = { protocol_version: Number(input.protocol_version), restrictions, roles, capabilities,
    recovery_state: input.recovery_state };
  const hasAdaptive = [input.status_schema_version, input.requested_policy, input.profile_id, input.profile_revision, input.verification_state,
    input.observation_time, input.selected_plan_digest].some((value) => value !== undefined) || input.reduced === true;
  if (!hasAdaptive) {
    if (input.reduced !== undefined && typeof input.reduced !== "boolean") return null;
    return input.reduced === undefined ? result : { ...result, reduced: input.reduced };
  }
  const adaptive = parseAdaptiveIsolationStatus(input);
  return adaptive ? { ...result, ...adaptive } : null;
}

const isolationRoles = new Set(["network", "runtime", "config_writer", "command_helper", "sandbox_supervisor"]);
const mechanismNames = new Set(["landlock", "seccomp", "no-new-privileges", "descriptor-allowlist", "private-namespaces",
  "mount-namespace", "role-cgroups", "separate-host-identities", "capability-sets", "pidfd-supervision", "fixed-target",
  "fixed-executable", "trusted-broker", "held-inputs", "trusted-supervisor", "session-bound-rpc", "bounded-relay"]);
const mechanismStates = new Set(["applied", "unavailable", "not_requested", "not_applicable", "unknown"]);
const reasonCodes = new Set(["profile-not-implemented", "not-delegated", "same-uid-profile", "inherited-lower", "unsupported",
  "not-required", "external-restriction", "verification-failed"]);
const capabilityNames = new Set(["CAP_CHOWN", "CAP_DAC_OVERRIDE", "CAP_DAC_READ_SEARCH", "CAP_FOWNER", "CAP_FSETID", "CAP_KILL",
  "CAP_SETGID", "CAP_SETUID", "CAP_SETPCAP", "CAP_LINUX_IMMUTABLE", "CAP_NET_BIND_SERVICE", "CAP_NET_BROADCAST", "CAP_NET_ADMIN",
  "CAP_NET_RAW", "CAP_IPC_LOCK", "CAP_IPC_OWNER", "CAP_SYS_MODULE", "CAP_SYS_RAWIO", "CAP_SYS_CHROOT", "CAP_SYS_PTRACE",
  "CAP_SYS_PACCT", "CAP_SYS_ADMIN", "CAP_SYS_BOOT", "CAP_SYS_NICE", "CAP_SYS_RESOURCE", "CAP_SYS_TIME", "CAP_SYS_TTY_CONFIG",
  "CAP_MKNOD", "CAP_LEASE", "CAP_AUDIT_WRITE", "CAP_AUDIT_CONTROL", "CAP_SETFCAP", "CAP_MAC_OVERRIDE", "CAP_MAC_ADMIN",
  "CAP_SYSLOG", "CAP_WAKE_ALARM", "CAP_BLOCK_SUSPEND", "CAP_AUDIT_READ", "CAP_PERFMON", "CAP_BPF", "CAP_CHECKPOINT_RESTORE"]);
const resourceNames = new Set(["memory", "memory-swap", "tasks", "cpu-quota", "descriptors", "file-size"]);
const resourceUnits = new Set(["bytes", "count", "microseconds"]);
const resourceScopes = new Set(["role", "process", "process-set", "inherited"]);
const resourceSources = new Set(["configured", "default", "inherited", "external"]);
const enforcementStates = new Set(["applied", "observed", "unavailable", "not_requested", "not_applicable", "unknown"]);

type AdaptiveIsolationStatus = Required<Pick<IsolationStatus, "status_schema_version" | "requested_policy" | "profile_id" | "profile_revision"
  | "verification_state" | "reduced" | "mechanisms" | "credentials" | "capability_sets" | "resource_limits"
  | "warnings" | "observation_time" | "selected_plan_digest">>;

function parseAdaptiveIsolationStatus(input: Record<string, unknown>): AdaptiveIsolationStatus | null {
  if (input.status_schema_version !== 1
    || !(input.requested_policy === "strict" || input.requested_policy === "auto" || input.requested_policy === "off")
    || !(input.profile_id === "linux-full-v1" || input.profile_id === "linux-user-v1" || input.profile_id === "windows-full-v1")
    || !Number.isSafeInteger(input.profile_revision) || Number(input.profile_revision) <= 0
    || !(input.verification_state === "verified" || input.verification_state === "degraded"
      || input.verification_state === "failed" || input.verification_state === "unknown")
    || typeof input.reduced !== "boolean" || !isRecord(input.mechanisms) || !boundedRecord(input.mechanisms, 1, 8)
    || !isRecord(input.credentials) || !boundedRecord(input.credentials, 1, 8)
    || !isRecord(input.capability_sets) || !boundedRecord(input.capability_sets, 1, 8)
    || !isRecord(input.resource_limits) || !boundedRecord(input.resource_limits, 0, 8)
    || !Array.isArray(input.warnings) || input.warnings.length > 32
    || typeof input.observation_time !== "string" || input.observation_time.length > 64
    || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(input.observation_time)
    || Number.isNaN(Date.parse(input.observation_time))
    || typeof input.selected_plan_digest !== "string" || !/^[0-9a-f]{64}$/.test(input.selected_plan_digest)) return null;
  const mechanisms: Record<string, Record<string, IsolationMechanism>> = {};
  for (const [role, values] of Object.entries(input.mechanisms)) {
    if (!isIsolationRole(role) || !isRecord(values) || !boundedRecord(values, 1, 32)) return null;
    const parsed: Record<string, IsolationMechanism> = {};
    for (const [name, mechanism] of Object.entries(values)) {
      if (!mechanismNames.has(name) || !isRecord(mechanism) || !mechanismStates.has(String(mechanism.state))
        || (mechanism.reason !== undefined && (typeof mechanism.reason !== "string" || !reasonCodes.has(mechanism.reason)))) return null;
      parsed[name] = { state: mechanism.state as IsolationMechanism["state"],
        ...(mechanism.reason === undefined ? {} : { reason: mechanism.reason as string }) };
    }
    mechanisms[role] = parsed;
  }
  const credentials: Record<string, IsolationCredentials> = {};
  for (const [role, value] of Object.entries(input.credentials)) {
    const parsed = parseIsolationCredentials(value);
    if (!isIsolationRole(role) || !parsed) return null;
    credentials[role] = parsed;
  }
  const capabilitySets: Record<string, IsolationCapabilitySets> = {};
  for (const [role, value] of Object.entries(input.capability_sets)) {
    const parsed = parseCapabilitySets(value);
    if (!isIsolationRole(role) || !parsed) return null;
    capabilitySets[role] = parsed;
  }
  if (!sameKeys(mechanisms, credentials) || !sameKeys(mechanisms, capabilitySets)) return null;
  const resourceLimits: Record<string, IsolationResourceLimit[]> = {};
  for (const [role, values] of Object.entries(input.resource_limits)) {
    if (!isIsolationRole(role) || mechanisms[role] === undefined || !Array.isArray(values) || values.length === 0 || values.length > 16) return null;
    const parsed = values.map(parseResourceLimit);
    if (parsed.some((value) => value === null) || new Set(parsed.map((value) => value?.name)).size !== parsed.length) return null;
    resourceLimits[role] = parsed as IsolationResourceLimit[];
  }
  const warnings = input.warnings.map(parseIsolationWarning);
  if (warnings.some((value) => value === null)) return null;
  return { status_schema_version: input.status_schema_version, requested_policy: input.requested_policy, profile_id: input.profile_id,
    profile_revision: Number(input.profile_revision), verification_state: input.verification_state, reduced: input.reduced,
    mechanisms, credentials, capability_sets: capabilitySets, resource_limits: resourceLimits,
    warnings: warnings as IsolationWarning[], observation_time: input.observation_time, selected_plan_digest: input.selected_plan_digest };
}

function parseIsolationCredentials(input: unknown): IsolationCredentials | null {
  if (!isRecord(input)) return null;
  const fields = [input.real_uid, input.effective_uid, input.saved_uid, input.filesystem_uid,
    input.real_gid, input.effective_gid, input.saved_gid, input.filesystem_gid];
  if (!fields.every(isUint32) || !Array.isArray(input.supplementary_groups) || input.supplementary_groups.length > 64
    || !input.supplementary_groups.every(isUint32) || new Set(input.supplementary_groups).size !== input.supplementary_groups.length) return null;
  return { real_uid: Number(input.real_uid), effective_uid: Number(input.effective_uid), saved_uid: Number(input.saved_uid),
    filesystem_uid: Number(input.filesystem_uid), real_gid: Number(input.real_gid), effective_gid: Number(input.effective_gid),
    saved_gid: Number(input.saved_gid), filesystem_gid: Number(input.filesystem_gid), supplementary_groups: [...input.supplementary_groups] };
}

function parseCapabilitySets(input: unknown): IsolationCapabilitySets | null {
  if (!isRecord(input)) return null;
  const fields = [input.inheritable, input.permitted, input.effective, input.bounding, input.ambient];
  if (!fields.every((value) => validEnumArray(value, capabilityNames, 64))) return null;
  return { inheritable: [...input.inheritable as string[]], permitted: [...input.permitted as string[]],
    effective: [...input.effective as string[]], bounding: [...input.bounding as string[]], ambient: [...input.ambient as string[]] };
}

function parseResourceLimit(input: unknown): IsolationResourceLimit | null {
  if (!isRecord(input) || typeof input.name !== "string" || !resourceNames.has(input.name)
    || !isResourceValue(input.requested) || !isResourceValue(input.effective)
    || typeof input.unit !== "string" || !resourceUnits.has(input.unit) || typeof input.scope !== "string" || !resourceScopes.has(input.scope)
    || typeof input.source !== "string" || !resourceSources.has(input.source)
    || typeof input.enforcement !== "string" || !enforcementStates.has(input.enforcement)) return null;
  return input as IsolationResourceLimit;
}

function parseIsolationWarning(input: unknown): IsolationWarning | null {
  return isRecord(input) && typeof input.role === "string" && isIsolationRole(input.role)
    && typeof input.code === "string" && reasonCodes.has(input.code)
    && typeof input.effect === "string" && input.effect.length > 0 && input.effect.length <= 256
    && typeof input.feature === "string" && input.feature.length > 0 && input.feature.length <= 256 ? input as IsolationWarning : null;
}

function boundedRecord(input: Record<string, unknown>, minimum: number, maximum: number): boolean {
  const length = Object.keys(input).length;
  return length >= minimum && length <= maximum;
}
function sameKeys(left: Record<string, unknown>, right: Record<string, unknown>): boolean {
  const leftKeys = Object.keys(left).sort();
  const rightKeys = Object.keys(right).sort();
  return leftKeys.length === rightKeys.length && leftKeys.every((value, index) => value === rightKeys[index]);
}
function isIsolationRole(value: string): boolean { return isolationRoles.has(value); }
function isUint32(input: unknown): boolean { return Number.isSafeInteger(input) && Number(input) >= 0 && Number(input) <= 0xffffffff; }
function validEnumArray(input: unknown, values: Set<string>, maximum: number): input is string[] {
  return Array.isArray(input) && input.length <= maximum && input.every((value) => typeof value === "string" && values.has(value))
    && new Set(input).size === input.length;
}
function isResourceValue(input: unknown): input is string {
  if (typeof input !== "string") return false;
  if (input === "max") return true;
  if (!/^(0|[1-9][0-9]{0,19})$/.test(input)) return false;
  return input.length < 20 || input <= "18446744073709551615";
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
