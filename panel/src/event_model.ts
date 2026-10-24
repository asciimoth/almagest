import type {
  DNSCacheChange,
  DNSCacheInfo,
  DNSKeyChange,
  DNSKeyInfo,
  DNSKeysSnapshot,
  DNSValue,
  DNSValueSnapshot,
} from "./dns_model.js";
import type { KVEntriesSnapshot, KVEntryInfo, KVStoreInfo, KVValueSnapshot } from "./kv_model.js";
import type { SecretChange, SecretEntry, SecretsSnapshot } from "./secrets_model.js";
import type { TLSBuiltinCAInfo, TLSCertEntry, TLSChange, TLSPolicyEntry, TLSRevocationStatus, TLSSnapshot } from "./tls_model.js";

export type LogEntry = {
  at: string;
  source: string;
  level: string;
  message: string;
  node_id?: number;
  node_class?: string;
};

export type ProjectVersionStatus = {
  version: string;
  highlight: "none" | "warn" | "critical";
  hover_message?: string;
};

export type SubscriptionChange<T> = {
  subscription_id: number;
  change: T;
};

export type SubscriptionSnapshot<T> = {
  subscription_id: number;
  snapshot: T;
};

export type DNSValueEvent = {
  subscription_id: number;
  cache: string;
  key: string;
  found: boolean;
  value?: DNSValue;
};

export function parseLogEntry(input: unknown): LogEntry | null {
  if (!isRecord(input)
    || !isString(input.at)
    || !isString(input.source)
    || !isString(input.level)
    || !isString(input.message)
    || !isOptionalID(input.node_id)
    || !isOptionalString(input.node_class)) return null;
  return {
    at: input.at,
    source: input.source,
    level: input.level,
    message: input.message,
    ...(input.node_id === undefined ? {} : { node_id: input.node_id }),
    ...(input.node_class === undefined ? {} : { node_class: input.node_class }),
  };
}

export function parseProjectVersionStatus(input: unknown): ProjectVersionStatus | null {
  if (!isRecord(input)
    || !isString(input.version)
    || !isProjectVersionHighlight(input.highlight)
    || !isOptionalString(input.hover_message)) return null;
  return {
    version: input.version,
    highlight: input.highlight,
    ...(input.hover_message === undefined ? {} : { hover_message: input.hover_message }),
  };
}

export function parseDNSCacheEvent(input: unknown): SubscriptionChange<DNSCacheChange> | null {
  return parseSubscriptionChange(input, parseDNSCacheChange);
}

export function parseDNSKeyEvent(input: unknown): (SubscriptionChange<DNSKeyChange> & { cache: string }) | null {
  const event = parseSubscriptionChange(input, parseDNSKeyChange);
  if (!event || !isRecord(input) || !isString(input.cache)) return null;
  return { ...event, cache: input.cache };
}

export function parseDNSValueEvent(input: unknown): DNSValueEvent | null {
  if (!isRecord(input)
    || !isID(input.subscription_id)
    || !isString(input.cache)
    || !isString(input.key)
    || typeof input.found !== "boolean") return null;
  const value = input.value === undefined ? undefined : parseDNSValue(input.value);
  if (value === null) return null;
  return {
    subscription_id: input.subscription_id,
    cache: input.cache,
    key: input.key,
    found: input.found,
    ...(value === undefined ? {} : { value }),
  };
}

export function parseKVStoresEvent(input: unknown): SubscriptionSnapshot<{ stores: KVStoreInfo[] }> | null {
  return parseSubscriptionSnapshot(input, parseKVStoresSnapshot);
}

export function parseKVEntriesEvent(input: unknown): SubscriptionSnapshot<KVEntriesSnapshot> | null {
  return parseSubscriptionSnapshot(input, parseKVEntriesSnapshot);
}

export function parseKVValueEvent(input: unknown): SubscriptionSnapshot<KVValueSnapshot> | null {
  return parseSubscriptionSnapshot(input, parseKVValueSnapshot);
}

export function parseSecretsEvent(input: unknown): SubscriptionChange<SecretChange> | null {
  return parseSubscriptionChange(input, parseSecretChange);
}

export function parseTLSEvent(input: unknown): SubscriptionChange<TLSChange> | null {
  return parseSubscriptionChange(input, parseTLSChange);
}

export function parseDNSCachesSnapshot(input: unknown): { caches?: DNSCacheInfo[] } | null {
  if (!isRecord(input)) return null;
  if (input.caches === undefined) return {};
  if (!Array.isArray(input.caches)) return null;
  const caches = input.caches.map((cache): DNSCacheInfo | null => (
    isRecord(cache) && isString(cache.name) ? { name: cache.name } : null
  ));
  return caches.every((cache): cache is DNSCacheInfo => cache !== null) ? { caches } : null;
}

export function parseDNSKeysSnapshot(input: unknown): DNSKeysSnapshot | null {
  if (!isRecord(input) || typeof input.exists !== "boolean") return null;
  if (input.keys === undefined) return { exists: input.exists };
  if (!Array.isArray(input.keys)) return null;
  const keys = input.keys.map(parseDNSKeyInfo);
  return keys.every((key): key is DNSKeyInfo => key !== null) ? { exists: input.exists, keys } : null;
}

export function parseDNSValueSnapshot(input: unknown): DNSValueSnapshot | null {
  if (!isRecord(input) || typeof input.found !== "boolean") return null;
  if (input.value === undefined) return { found: input.found };
  const value = parseDNSValue(input.value);
  return value ? { found: input.found, value } : null;
}

export function parseSecretsSnapshot(input: unknown): SecretsSnapshot | null {
  if (!isRecord(input)) return null;
  if (input.secrets === undefined) return {};
  if (!Array.isArray(input.secrets)) return null;
  const secrets = input.secrets.map(parseSecretEntry);
  return secrets.every((entry): entry is SecretEntry => entry !== null) ? { secrets } : null;
}

export function parseTLSSnapshot(input: unknown): TLSSnapshot | null {
  if (!isRecord(input)) return null;
  if (input.builtin_ca !== undefined && !isTLSBuiltinCAInfo(input.builtin_ca)) return null;
  if (input.certs !== undefined
    && (!Array.isArray(input.certs) || !input.certs.every(isTLSCertEntry))) return null;
  if (input.policies !== undefined
    && (!Array.isArray(input.policies) || !input.policies.every(isTLSPolicyEntry))) return null;
  if (input.revocations !== undefined && !isTLSRevocationStatus(input.revocations)) return null;
  return {
    ...(input.builtin_ca === undefined ? {} : { builtin_ca: input.builtin_ca }),
    ...(input.certs === undefined ? {} : { certs: [...input.certs] }),
    ...(input.policies === undefined ? {} : { policies: [...input.policies] }),
    ...(input.revocations === undefined ? {} : { revocations: input.revocations }),
  };
}

export function parseTLSBuiltinCAInfo(input: unknown): TLSBuiltinCAInfo | null {
  return isTLSBuiltinCAInfo(input) ? input : null;
}

export function parseTLSCertEntry(input: unknown): TLSCertEntry | null {
  return isTLSCertEntry(input) ? input : null;
}

export function parseTLSPolicyEntry(input: unknown): TLSPolicyEntry | null {
  return isTLSPolicyEntry(input) ? input : null;
}

function parseSubscriptionChange<T>(input: unknown, parse: (value: unknown) => T | null): SubscriptionChange<T> | null {
  if (!isRecord(input) || !isID(input.subscription_id)) return null;
  const change = parse(input.change);
  return change ? { subscription_id: input.subscription_id, change } : null;
}

function parseSubscriptionSnapshot<T>(input: unknown, parse: (value: unknown) => T | null): SubscriptionSnapshot<T> | null {
  if (!isRecord(input) || !isID(input.subscription_id)) return null;
  const snapshot = parse(input.snapshot);
  return snapshot ? { subscription_id: input.subscription_id, snapshot } : null;
}

function parseDNSCacheChange(input: unknown): DNSCacheChange | null {
  if (!isRecord(input) || !isString(input.name) || !isOptionalBoolean(input.deleted)) return null;
  return { name: input.name, ...(input.deleted === undefined ? {} : { deleted: input.deleted }) };
}

function parseDNSKeyInfo(input: unknown): DNSKeyInfo | null {
  if (!isRecord(input)
    || !isString(input.key)
    || !isOptionalFiniteNumber(input.size)
    || !isOptionalString(input.expires_at)) return null;
  return {
    key: input.key,
    ...(input.size === undefined ? {} : { size: input.size }),
    ...(input.expires_at === undefined ? {} : { expires_at: input.expires_at }),
  };
}

function parseDNSKeyChange(input: unknown): DNSKeyChange | null {
  if (!isRecord(input) || !isString(input.key) || !isOptionalBoolean(input.deleted)) return null;
  const entry = input.entry === undefined ? undefined : parseDNSKeyInfo(input.entry);
  if (entry === null) return null;
  return {
    key: input.key,
    ...(input.deleted === undefined ? {} : { deleted: input.deleted }),
    ...(entry === undefined ? {} : { entry }),
  };
}

export function parseDNSValue(input: unknown): DNSValue | null {
  if (!isRecord(input)
    || !isOptionalString(input.json)
    || !isOptionalFiniteNumber(input.size)
    || !isOptionalString(input.expires_at)) return null;
  return {
    ...(input.json === undefined ? {} : { json: input.json }),
    ...(input.size === undefined ? {} : { size: input.size }),
    ...(input.expires_at === undefined ? {} : { expires_at: input.expires_at }),
  };
}

export function parseKVStoresSnapshot(input: unknown): { stores: KVStoreInfo[] } | null {
  if (!isRecord(input) || !Array.isArray(input.stores)) return null;
  const stores = input.stores.map(parseKVStore);
  return stores.every((store): store is KVStoreInfo => store !== null) ? { stores } : null;
}

function parseKVStore(input: unknown): KVStoreInfo | null {
  if (!isRecord(input) || (input.kind !== "default" && input.kind !== "plugin")) return null;
  if (input.kind === "plugin") return isString(input.name) ? { kind: "plugin", name: input.name } : null;
  return input.name === undefined || isString(input.name) ? { kind: "default" } : null;
}

export function parseKVEntriesSnapshot(input: unknown): KVEntriesSnapshot | null {
  if (!isRecord(input) || typeof input.exists !== "boolean") return null;
  if (input.entries === undefined) return { exists: input.exists };
  if (!Array.isArray(input.entries)) return null;
  const entries: Array<KVEntryInfo | null> = input.entries.map((entry): KVEntryInfo | null => {
    if (!isRecord(entry)
      || (entry.kind !== "bucket" && entry.kind !== "value")
      || !isString(entry.name)
      || !isOptionalString(entry.owner)
      || !isOptionalFiniteNumber(entry.size)
      || !isOptionalBoolean(entry.hidden)
      || !isOptionalBoolean(entry.string)) return null;
    return {
      kind: entry.kind,
      name: entry.name,
      ...(entry.owner === undefined ? {} : { owner: entry.owner }),
      ...(entry.size === undefined ? {} : { size: entry.size }),
      ...(entry.hidden === undefined ? {} : { hidden: entry.hidden }),
      ...(entry.string === undefined ? {} : { string: entry.string }),
    };
  });
  return entries.every((entry): entry is NonNullable<typeof entry> => entry !== null)
    ? { exists: input.exists, entries }
    : null;
}

export function parseKVValueSnapshot(input: unknown): KVValueSnapshot | null {
  if (!isRecord(input) || typeof input.found !== "boolean") return null;
  if (input.value === undefined) return { found: input.found };
  if (!isRecord(input.value)
    || !isOptionalString(input.value.data)
    || !isOptionalString(input.value.encoding)
    || !isOptionalString(input.value.owner)
    || !isOptionalFiniteNumber(input.value.size)
    || !isOptionalBoolean(input.value.hidden)
    || !isOptionalBoolean(input.value.string)) return null;
  return {
    found: input.found,
    value: {
      ...(input.value.data === undefined ? {} : { data: input.value.data }),
      ...(input.value.encoding === undefined ? {} : { encoding: input.value.encoding }),
      ...(input.value.owner === undefined ? {} : { owner: input.value.owner }),
      ...(input.value.size === undefined ? {} : { size: input.value.size }),
      ...(input.value.hidden === undefined ? {} : { hidden: input.value.hidden }),
      ...(input.value.string === undefined ? {} : { string: input.value.string }),
    },
  };
}

function parseSecretChange(input: unknown): SecretChange | null {
  if (!isRecord(input) || !isString(input.key) || !isOptionalBoolean(input.deleted)) return null;
  const entry = input.entry === undefined ? undefined : parseSecretEntry(input.entry);
  if (entry === null) return null;
  return {
    key: input.key,
    ...(input.deleted === undefined ? {} : { deleted: input.deleted }),
    ...(entry === undefined ? {} : { entry }),
  };
}

export function parseSecretEntry(input: unknown): SecretEntry | null {
  if (!isRecord(input)
    || !isString(input.key)
    || !isString(input.value)
    || !isOptionalStringArray(input.nodes)
    || !isOptionalBoolean(input.read_only)
    || !isOptionalString(input.source)) return null;
  return {
    key: input.key,
    value: input.value,
    ...(input.nodes === undefined ? {} : { nodes: [...input.nodes] }),
    ...(input.read_only === undefined ? {} : { read_only: input.read_only }),
    ...(input.source === undefined ? {} : { source: input.source }),
  };
}

// TLS changes have a large nested policy schema. Validate the discriminant and
// the object selected by it here. The policy editor validates policy details
// when it converts them to an editable document.
function parseTLSChange(input: unknown): TLSChange | null {
  if (!isRecord(input)
    || (input.kind !== "builtin_ca" && input.kind !== "cert" && input.kind !== "policy")
    || !isOptionalString(input.name)
    || !isOptionalBoolean(input.deleted)) return null;
  if (input.kind === "builtin_ca") {
    if (input.builtin_ca !== undefined && !isTLSBuiltinCAInfo(input.builtin_ca)) return null;
    return {
      kind: input.kind,
      ...(input.name === undefined ? {} : { name: input.name }),
      ...(input.deleted === undefined ? {} : { deleted: input.deleted }),
      ...(input.builtin_ca === undefined ? {} : { builtin_ca: input.builtin_ca }),
    };
  }
  if (input.kind === "cert") {
    if (input.cert !== undefined && !isTLSCertEntry(input.cert)) return null;
    return {
      kind: input.kind,
      ...(input.name === undefined ? {} : { name: input.name }),
      ...(input.deleted === undefined ? {} : { deleted: input.deleted }),
      ...(input.cert === undefined ? {} : { cert: input.cert }),
    };
  }
  if (input.policy !== undefined && !isTLSPolicyEntry(input.policy)) return null;
  return {
    kind: input.kind,
    ...(input.name === undefined ? {} : { name: input.name }),
    ...(input.deleted === undefined ? {} : { deleted: input.deleted }),
    ...(input.policy === undefined ? {} : { policy: input.policy }),
  };
}

function isTLSBuiltinCAInfo(input: unknown): input is TLSBuiltinCAInfo {
  return isRecord(input)
    && isTLSCertEntry(input.cert)
    && isTLSCRLInfo(input.crl)
    && isTLSBuiltinCAOptions(input.options)
    && isOptionalBoolean(input.immutable)
    && isOptionalBoolean(input.regeneration_disabled)
    && isOptionalString(input.regeneration_disabled_by);
}

function isTLSCRLInfo(input: unknown): boolean {
  return isRecord(input)
    && isString(input.pem)
    && isString(input.number)
    && isString(input.issuer)
    && isString(input.this_update)
    && isString(input.next_update);
}

function isTLSBuiltinCAOptions(input: unknown): input is TLSBuiltinCAInfo["options"] {
  return isRecord(input)
    && isOptionalString(input.issuer)
    && isOptionalFiniteNumber(input.validity_days)
    && isOptionalString(input.key_algorithm)
    && isOptionalString(input.curve)
    && isOptionalFiniteNumber(input.key_bits);
}

function isTLSCertEntry(input: unknown): input is TLSCertEntry {
  return isRecord(input)
    && isString(input.name)
    && isOptionalString(input.pem)
    && isOptionalString(input.fingerprint)
    && isOptionalString(input.spki_fingerprint)
    && isOptionalString(input.subject)
    && isOptionalString(input.issuer)
    && isOptionalString(input.serial_number)
    && isOptionalString(input.not_before)
    && isOptionalString(input.not_after)
    && isOptionalBoolean(input.is_ca)
    && isOptionalBoolean(input.read_only)
    && isOptionalString(input.source)
    && (input.certcat_matches === undefined
      || (Array.isArray(input.certcat_matches) && input.certcat_matches.every(isTLSCertcatMatch)))
    && (input.revocation_matches === undefined
      || (Array.isArray(input.revocation_matches) && input.revocation_matches.every(isTLSRevocationMatch)))
    && (input.distrust_matches === undefined
      || (Array.isArray(input.distrust_matches) && input.distrust_matches.every((match) =>
        isRecord(match) && isString(match.source) && isString(match.kind))));
}

function isTLSRevocationMatch(input: unknown): boolean {
  return isRecord(input) && isString(input.source) && isString(input.serial_number)
    && isString(input.revocation_time) && typeof input.reason_code === "number";
}

function isTLSRevocationStatus(input: unknown): input is TLSRevocationStatus {
  return isRecord(input) && typeof input.enabled === "boolean"
    && isOptionalString(input.last_refresh_error)
    && (input.system === undefined || (Array.isArray(input.system) && input.system.every((status) =>
      isRecord(status) && isString(status.source) && isString(status.state)
      && (status.observed_at === undefined || typeof status.observed_at === "number")
      && (status.item_count === undefined || typeof status.item_count === "number"))))
    && (input.sources === undefined || (Array.isArray(input.sources) && input.sources.every((source) =>
      isRecord(source) && isString(source.name) && isString(source.source) && isString(source.status)
      && isOptionalString(source.issuer) && isOptionalString(source.number)
      && isOptionalString(source.this_update) && isOptionalString(source.next_update)
      && typeof source.revoked_count === "number" && isOptionalString(source.error))));
}

function isTLSPolicyEntry(input: unknown): input is TLSPolicyEntry {
  return isRecord(input)
    && isString(input.name)
    && isTLSPolicySpec(input.spec)
    && typeof input.valid === "boolean"
    && isOptionalBoolean(input.read_only)
    && isOptionalString(input.source)
    && isOptionalString(input.error)
    && isOptionalStringArray(input.watchers);
}

function isTLSPolicySpec(input: unknown): input is TLSPolicyEntry["spec"] {
  return isRecord(input)
    && (input.trust_anchors === undefined
      || (Array.isArray(input.trust_anchors) && input.trust_anchors.every(isTLSTrustAnchor)))
    && (input.constraints === undefined
      || (Array.isArray(input.constraints) && input.constraints.every(isTLSAuthorityConstraint)))
    && (input.pins === undefined
      || (Array.isArray(input.pins) && input.pins.every(isTLSPin)));
}

function isTLSTrustAnchor(input: unknown): boolean {
  return isRecord(input) && isString(input.cert) && isTLSScope(input.scope);
}

function isTLSAuthorityConstraint(input: unknown): boolean {
  return isRecord(input)
    && isString(input.cert)
    && isOptionalString(input.match)
    && isTLSScope(input.scope);
}

function isTLSPin(input: unknown): boolean {
  return isRecord(input)
    && isString(input.kind)
    && isString(input.fingerprint)
    && isTLSScope(input.scope);
}

function isTLSScope(input: unknown): boolean {
  return isRecord(input)
    && isOptionalBoolean(input.any)
    && (input.dns === undefined || (Array.isArray(input.dns) && input.dns.every(isTLSDNSRule)))
    && isOptionalStringArray(input.ip_prefixes);
}

function isTLSDNSRule(input: unknown): boolean {
  return isRecord(input)
    && isString(input.domain)
    && isOptionalBoolean(input.include_subdomains);
}

function isTLSCertcatMatch(input: unknown): boolean {
  return isRecord(input)
    && isOptionalString(input.fingerprint)
    && isOptionalString(input.match_kind)
    && isOptionalString(input.kind)
    && isOptionalString(input.hazard_class)
    && isOptionalString(input.action)
    && isOptionalString(input.severity)
    && isOptionalString(input.recommendation)
    && (input.scope === undefined || isTLSCertcatScope(input.scope))
    && (input.platform_cutoff === undefined || isTLSCertcatPlatformCutoff(input.platform_cutoff));
}

function isTLSCertcatScope(input: unknown): boolean {
  return isRecord(input)
    && isOptionalString(input.basis)
    && isOptionalStringArray(input.permitted_dns_subtrees)
    && isOptionalStringArray(input.permitted_ip_cidrs)
    && isOptionalBoolean(input.requires_explicit_allowlist)
    && isOptionalBoolean(input.reject_ip_targets);
}

function isTLSCertcatPlatformCutoff(input: unknown): boolean {
  return isRecord(input)
    && isOptionalString(input.sct_timestamp_after)
    && isOptionalString(input.enforcement_start)
    && isOptionalFiniteNumber(input.first_major_version);
}

function isProjectVersionHighlight(input: unknown): input is ProjectVersionStatus["highlight"] {
  return input === "none" || input === "warn" || input === "critical";
}

function isString(input: unknown): input is string {
  return typeof input === "string";
}

function isFiniteNumber(input: unknown): input is number {
  return typeof input === "number" && Number.isFinite(input);
}

function isID(input: unknown): input is number {
  return typeof input === "number" && Number.isSafeInteger(input) && input >= 0;
}

function isOptionalString(input: unknown): input is string | undefined {
  return input === undefined || isString(input);
}

function isOptionalFiniteNumber(input: unknown): input is number | undefined {
  return input === undefined || isFiniteNumber(input);
}

function isOptionalID(input: unknown): input is number | undefined {
  return input === undefined || isID(input);
}

function isOptionalBoolean(input: unknown): input is boolean | undefined {
  return input === undefined || typeof input === "boolean";
}

function isOptionalStringArray(input: unknown): input is string[] | undefined {
  return input === undefined || (Array.isArray(input) && input.every(isString));
}

function isRecord(input: unknown): input is Record<string, unknown> {
  return typeof input === "object" && input !== null && !Array.isArray(input);
}
