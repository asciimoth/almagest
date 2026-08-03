export type TLSSource = "mutable" | "protected" | "os" | (string & {});

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
  certs?: TLSCertEntry[];
  policies?: TLSPolicyEntry[];
};

export type TLSChangeKind = "cert" | "policy";

export type TLSChange = {
  kind: TLSChangeKind;
  name: string;
  deleted?: boolean;
  cert?: TLSCertEntry;
  policy?: TLSPolicyEntry;
};

export type TLSItemKind = TLSChangeKind;

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
  if (entry.read_only) parts.push("read-only");
  if (entry.not_after) parts.push(`expires ${entry.not_after}`);
  return parts.join(" · ");
}

export function formatTLSPolicyMeta(entry: TLSPolicyEntry): string {
  const parts = [formatTLSSource(entry.source), entry.valid ? "valid" : "invalid"];
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

export function isMutableTLSCertName(name: string): boolean {
  return name.trim() !== "" && !name.startsWith("os-ca:");
}

export function isMutableTLSPolicyName(name: string): boolean {
  return name.trim() !== "" && name !== "default";
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
