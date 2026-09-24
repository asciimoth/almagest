export type TLSSource = "mutable" | "protected" | "os" | "builtin" | (string & {});

export const TLS_BUILTIN_CA_NAME = "builtin-ca:almagest";

export type TLSCertEntry = {
  name: string;
  pem?: string;
  fingerprint?: string;
  spki_fingerprint?: string;
  subject?: string;
  issuer?: string;
  serial_number?: string;
  not_before?: string;
  not_after?: string;
  is_ca?: boolean;
  read_only?: boolean;
  source?: TLSSource;
  certcat_matches?: TLSCertcatMatch[];
};

export type TLSBuiltinCAOptions = {
  issuer?: string;
  validity_days?: number;
  key_algorithm?: string;
  curve?: string;
  key_bits?: number;
};

export type TLSBuiltinCAInfo = {
  cert: TLSCertEntry;
  options: TLSBuiltinCAOptions;
  immutable?: boolean;
  regeneration_disabled?: boolean;
  regeneration_disabled_by?: string;
};

export type TLSDNSRule = {
  domain: string;
  include_subdomains?: boolean;
};

export type TLSScope = {
  any?: boolean;
  dns?: TLSDNSRule[];
  ip_prefixes?: string[];
};

export type TLSCertcatScope = {
  basis?: string;
  permitted_dns_subtrees?: string[];
  permitted_ip_cidrs?: string[];
  requires_explicit_allowlist?: boolean;
  reject_ip_targets?: boolean;
};

export type TLSCertcatPlatformCutoff = {
  sct_timestamp_after?: string;
  enforcement_start?: string;
  first_major_version?: number;
};

export type TLSCertcatMatch = {
  fingerprint?: string;
  match_kind?: string;
  kind?: string;
  hazard_class?: string;
  action?: string;
  severity?: string;
  recommendation?: string;
  scope?: TLSCertcatScope;
  platform_cutoff?: TLSCertcatPlatformCutoff;
};

export type TLSTrustAnchor = {
  cert: string;
  scope: TLSScope;
};

export type TLSAuthorityConstraint = {
  cert: string;
  match?: string;
  scope: TLSScope;
};

export type TLSPin = {
  kind: string;
  fingerprint: string;
  scope: TLSScope;
};

export type TLSPolicySpec = {
  trust_anchors?: TLSTrustAnchor[];
  constraints?: TLSAuthorityConstraint[];
  pins?: TLSPin[];
};

export type TLSPolicyEntry = {
  name: string;
  spec: TLSPolicySpec;
  read_only?: boolean;
  source?: TLSSource;
  valid: boolean;
  error?: string;
  watchers?: string[];
};

export type TLSSnapshot = {
  builtin_ca?: TLSBuiltinCAInfo;
  certs?: TLSCertEntry[];
  policies?: TLSPolicyEntry[];
};

export type TLSChangeKind = "builtin_ca" | "cert" | "policy";

export type TLSChange = {
  kind: TLSChangeKind;
  name?: string;
  deleted?: boolean;
  builtin_ca?: TLSBuiltinCAInfo;
  cert?: TLSCertEntry;
  policy?: TLSPolicyEntry;
};

export type TLSItemKind = "cert" | "policy";

export type TLSItemRef = {
  kind: TLSItemKind;
  name: string;
};

export const TLS_POLICY_JSON_ROWS = 12;

// TLS names are arbitrary UTF-8 strings from config and API storage. JSON tuple
// keys keep UI state reversible and collision-free without delimiter rules.
export function tlsItemKey(kind: TLSItemKind, item: TLSCertEntry | TLSPolicyEntry | string): string {
  return JSON.stringify([kind, typeof item === "string" ? item : item.name]);
}

export function parseTLSItemKey(key: string): TLSItemRef | null {
  try {
    const parsed = JSON.parse(key) as unknown;
    if (!Array.isArray(parsed) || parsed.length !== 2) return null;
    const [kind, name] = parsed;
    if ((kind !== "cert" && kind !== "policy") || typeof name !== "string") return null;
    return { kind, name };
  } catch {
    return null;
  }
}

export function sortTLSCerts(certs: TLSCertEntry[]): TLSCertEntry[] {
  return [...certs].sort(compareTLSNames);
}

export function sortTLSPolicies(policies: TLSPolicyEntry[]): TLSPolicyEntry[] {
  return [...policies].sort(compareTLSNames);
}

export function applyTLSChange(snapshot: TLSSnapshot, change: TLSChange): TLSSnapshot {
  if (change.kind === "builtin_ca") {
    const builtin = change.builtin_ca;
    const certs = builtin?.cert
      ? sortTLSCerts([...(snapshot.certs ?? []).filter((cert) => cert.name !== builtin.cert.name), builtin.cert])
      : snapshot.certs;
    const next = {
      ...snapshot,
      ...(builtin === undefined && snapshot.builtin_ca === undefined
        ? {}
        : { builtin_ca: builtin ?? snapshot.builtin_ca! }),
      ...(certs === undefined ? {} : { certs }),
    };
    return next;
  }

  if (!change.name) return snapshot;
  if (change.kind === "cert") {
    const certs = (snapshot.certs ?? []).filter((cert) => cert.name !== change.name);
    if (!change.deleted && change.cert) certs.push(change.cert);
    return {
      ...snapshot,
      certs: sortTLSCerts(certs),
    };
  }

  const policies = (snapshot.policies ?? []).filter((policy) => policy.name !== change.name);
  if (!change.deleted && change.policy) policies.push(change.policy);
  return {
    ...snapshot,
    policies: sortTLSPolicies(policies),
  };
}

export function formatTLSSource(source: TLSSource | undefined): string {
  switch (source) {
    case "os":
      return "OS";
    case "builtin":
      return "built-in";
    case "protected":
      return "protected";
    case "mutable":
    case "":
    case undefined:
      return "mutable";
    default:
      return source;
  }
}

export function formatTLSCertMeta(entry: TLSCertEntry): string {
  const parts = [formatTLSSource(entry.source)];
  if (entry.is_ca) parts.push("CA");
  const certcat = formatTLSCertcatBadge(entry);
  if (certcat) parts.push(certcat);
  if (entry.read_only) parts.push("read-only");
  if (entry.not_after) parts.push(`expires ${entry.not_after}`);
  return parts.join(" · ");
}

export function formatTLSPolicyMeta(entry: TLSPolicyEntry): string {
  const parts = [formatTLSSource(entry.source), entry.valid ? "valid" : "invalid"];
  if (entry.spec.trust_anchors?.some((anchor) => isBuiltinTLSCertName(anchor.cert))) parts.push("built-in CA");
  const watchers = entry.watchers ?? [];
  if (watchers.length) parts.push(`${watchers.length} watcher${watchers.length === 1 ? "" : "s"}`);
  if (entry.read_only) parts.push("read-only");
  return parts.join(" · ");
}

export function formatTLSPolicySpec(spec: TLSPolicySpec): string {
  return `${JSON.stringify(normalizePolicySpecShape(spec), null, 2)}\n`;
}

export function parseTLSPolicySpec(text: string): TLSPolicySpec {
  const parsed = JSON.parse(text) as unknown;
  if (!isRecord(parsed)) throw new Error("Policy spec must be a JSON object.");
  const spec: TLSPolicySpec = {};
  const anchors = parseArray(parsed.trust_anchors, "trust_anchors").map(parseTrustAnchor);
  const constraints = parseArray(parsed.constraints, "constraints").map(parseAuthorityConstraint);
  const pins = parseArray(parsed.pins, "pins").map(parsePin);
  if (anchors.length) spec.trust_anchors = anchors;
  if (constraints.length) spec.constraints = constraints;
  if (pins.length) spec.pins = pins;
  return spec;
}

export function emptyTLSPolicySpec(): TLSPolicySpec {
  return {};
}

export function describeTLSScope(scope: TLSScope | undefined): string {
  if (!scope) return "empty scope";
  if (scope.any) return "any server";
  const parts: string[] = [];
  for (const rule of scope.dns ?? []) {
    parts.push(rule.include_subdomains ? `*.${rule.domain}` : rule.domain);
  }
  parts.push(...(scope.ip_prefixes ?? []));
  return parts.length ? parts.join(", ") : "empty scope";
}

export function formatTLSCertcatBadge(entry: TLSCertEntry): string {
  const matches = entry.certcat_matches ?? [];
  if (!matches.length) return "";
  const severity = highestTLSCertcatSeverity(matches);
  const label = formatTLSCertcatSeverity(severity);
  return matches.length === 1 ? label : `${label} x${matches.length}`;
}

export function formatTLSCertcatMatch(match: TLSCertcatMatch): string {
  switch (match.hazard_class) {
    case "private_key_disclosure":
      return "Private key was disclosed";
    case "interception_ca":
      return "Interception certificate authority";
    case "government_ca":
      return "Government certificate authority";
    case "state_interception_ca":
      return "State interception certificate authority";
    case "time_scoped_root_program_distrust":
      return "Platform distrust applies";
    case "chromium_certificate_blocklist":
    case "chromium_spki_blocklist":
      return "Browser blocklist match";
  }
  switch (match.kind) {
    case "narrow_scope_ca":
      return "Use only for a narrow address scope";
    case "service_private_anchor":
      return "Use only for specific service addresses";
    case "hazard_certificate":
      return "Certificate warning";
  }
  return formatTLSCertcatSeverity(match.severity);
}

export function describeTLSCertcatScope(scope: TLSCertcatScope | undefined): string {
  if (!scope) return "";
  const parts: string[] = [];
  if (scope.permitted_dns_subtrees?.length) {
    parts.push(`Use only for DNS names in ${scope.permitted_dns_subtrees.join(", ")}`);
  }
  if (scope.permitted_ip_cidrs?.length) {
    parts.push(`Use only for IP ranges in ${scope.permitted_ip_cidrs.join(", ")}`);
  }
  if (scope.requires_explicit_allowlist) parts.push("Use only for explicitly allowed addresses");
  if (scope.reject_ip_targets) parts.push("Do not use for direct IP targets");
  return parts.join(", ");
}

export function describeTLSCertcatRecommendation(match: TLSCertcatMatch): string {
  switch (match.recommendation) {
    case "deny_exact_match":
    case "deny_exact_spki":
      return "Do not trust this certificate.";
    case "deny_exact_certificate_even_if_user_or_os_installed":
      return "Do not trust this certificate, even if it is installed locally.";
    case "trust_only_for_declared_scope":
      return "Use this certificate only for the listed DNS names or IP ranges.";
    case "only_use_for_explicit_cloudflare_origin_hosts":
      return "Use this certificate only for explicit Cloudflare origin hosts.";
    case "only_use_for_russian_government_resources":
      return "This certificate is related to the Russian government. Use it only to access Russian government resources.";
    case "apply_target_platform_distrust_cutoff":
      return "Follow the platform distrust date for this certificate.";
  }
  switch (match.action) {
    case "deny":
    case "deny_even_if_locally_installed":
      return "Do not trust this certificate.";
    case "narrow_scope":
    case "offer_scoped_service_anchor":
      return "Use this certificate only with a specific, narrow set of addresses.";
    case "enforce_platform_cutoff":
      return "Follow the platform distrust date for this certificate.";
    case "warn":
      return "Review before you trust this certificate.";
  }
  if (match.scope?.permitted_dns_subtrees?.length || match.scope?.permitted_ip_cidrs?.length) {
    return "Use this certificate only with the listed narrow address scope.";
  }
  return "";
}

export function isMutableTLSCertName(name: string): boolean {
  return name.trim() !== "" && !name.startsWith("os-ca:") && !isBuiltinTLSCertName(name);
}

export function isImmutableTLSCertName(name: string): boolean {
  return name.startsWith("os-ca:") || isBuiltinTLSCertName(name);
}

export function isMutableTLSPolicyName(name: string): boolean {
  return name.trim() !== "" && name !== "default";
}

export function isBuiltinTLSCertName(name: string): boolean {
  return name === TLS_BUILTIN_CA_NAME || name.startsWith("builtin-ca:");
}

function highestTLSCertcatSeverity(matches: TLSCertcatMatch[]): string {
  let out = "";
  let outRank = -1;
  for (const match of matches) {
    const severity = match.severity ?? "";
    const rank = tlsCertcatSeverityRank(severity);
    if (rank > outRank) {
      out = severity;
      outRank = rank;
    }
  }
  return out;
}

function tlsCertcatSeverityRank(severity: string): number {
  switch (severity) {
    case "critical":
      return 4;
    case "high":
      return 3;
    case "warning":
      return 2;
    case "info":
      return 1;
    default:
      return 0;
  }
}

function formatTLSCertcatSeverity(severity: string | undefined): string {
  switch (severity) {
    case "critical":
      return "critical warning";
    case "high":
      return "high warning";
    case "warning":
      return "warning";
    case "info":
      return "notice";
    default:
      return "warning";
  }
}

function parseTrustAnchor(input: unknown, index: number): TLSTrustAnchor {
  const item = expectRecord(input, `trust_anchors[${index}]`);
  return {
    cert: expectString(item.cert, `trust_anchors[${index}].cert`),
    scope: parseScope(item.scope, `trust_anchors[${index}].scope`),
  };
}

function parseAuthorityConstraint(input: unknown, index: number): TLSAuthorityConstraint {
  const item = expectRecord(input, `constraints[${index}]`);
  const out: TLSAuthorityConstraint = {
    cert: expectString(item.cert, `constraints[${index}].cert`),
    scope: parseScope(item.scope, `constraints[${index}].scope`),
  };
  if (item.match !== undefined) out.match = expectString(item.match, `constraints[${index}].match`);
  return out;
}

function parsePin(input: unknown, index: number): TLSPin {
  const item = expectRecord(input, `pins[${index}]`);
  return {
    kind: expectString(item.kind, `pins[${index}].kind`),
    fingerprint: expectString(item.fingerprint, `pins[${index}].fingerprint`),
    scope: parseScope(item.scope, `pins[${index}].scope`),
  };
}

function parseScope(input: unknown, path: string): TLSScope {
  const item = expectRecord(input, path);
  const scope: TLSScope = {};
  if (item.any !== undefined) {
    if (typeof item.any !== "boolean") throw new Error(`${path}.any must be a boolean.`);
    if (item.any) scope.any = true;
  }
  const dns = parseArray(item.dns, `${path}.dns`).map((value, index) => parseDNSRule(value, `${path}.dns[${index}]`));
  const prefixes = parseArray(item.ip_prefixes, `${path}.ip_prefixes`).map((value, index) => (
    expectString(value, `${path}.ip_prefixes[${index}]`)
  ));
  if (dns.length) scope.dns = dns;
  if (prefixes.length) scope.ip_prefixes = prefixes;
  return scope;
}

function parseDNSRule(input: unknown, path: string): TLSDNSRule {
  const item = expectRecord(input, path);
  const out: TLSDNSRule = {
    domain: expectString(item.domain, `${path}.domain`),
  };
  if (item.include_subdomains !== undefined) {
    if (typeof item.include_subdomains !== "boolean") throw new Error(`${path}.include_subdomains must be a boolean.`);
    if (item.include_subdomains) out.include_subdomains = true;
  }
  return out;
}

function normalizePolicySpecShape(spec: TLSPolicySpec): TLSPolicySpec {
  const out: TLSPolicySpec = {};
  if (spec.trust_anchors?.length) out.trust_anchors = spec.trust_anchors;
  if (spec.constraints?.length) out.constraints = spec.constraints;
  if (spec.pins?.length) out.pins = spec.pins;
  return out;
}

function parseArray(input: unknown, path: string): unknown[] {
  if (input === undefined) return [];
  if (!Array.isArray(input)) throw new Error(`${path} must be an array.`);
  return input;
}

function expectRecord(input: unknown, path: string): Record<string, unknown> {
  if (!isRecord(input)) throw new Error(`${path} must be an object.`);
  return input;
}

function expectString(input: unknown, path: string): string {
  if (typeof input !== "string") throw new Error(`${path} must be a string.`);
  return input;
}

function isRecord(input: unknown): input is Record<string, unknown> {
  return typeof input === "object" && input !== null && !Array.isArray(input);
}

function compareTLSNames<T extends { name: string }>(left: T, right: T): number {
  return left.name.localeCompare(right.name);
}
