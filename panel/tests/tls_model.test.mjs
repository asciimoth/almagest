import assert from "node:assert/strict";
import { test } from "node:test";
import {
  applyTLSChange,
  describeTLSCertcatScope,
  describeTLSScope,
  formatTLSCertcatBadge,
  formatTLSCertcatMatch,
  formatTLSPolicySpec,
  isBuiltinTLSCertName,
  isMutableTLSCertName,
  isMutableTLSPolicyName,
  parseTLSPolicySpec,
  parseTLSItemKey,
  sortTLSCerts,
  sortTLSPolicies,
  tlsItemKey,
} from "../public/build/tls_model.js";

test("TLS item keys round-trip untrusted names without delimiter parsing", () => {
  const name = `ca|prod\n["cert"]`;
  const key = tlsItemKey("cert", name);

  assert.deepEqual(parseTLSItemKey(key), { kind: "cert", name });
  assert.equal(parseTLSItemKey("cert|ca"), null);
});

test("TLS changes upsert, delete, and keep deterministic order", () => {
  const snapshot = {
    certs: [{ name: "z" }],
    policies: [{ name: "default", spec: {}, valid: true }],
  };

  const withCert = applyTLSChange(snapshot, { kind: "cert", name: "a", cert: { name: "a" } });
  assert.deepEqual(withCert.certs.map((entry) => entry.name), ["a", "z"]);

  const withPolicy = applyTLSChange(withCert, {
    kind: "policy",
    name: "custom",
    policy: { name: "custom", spec: {}, valid: false, error: "missing cert" },
  });
  assert.deepEqual(withPolicy.policies.map((entry) => entry.name), ["custom", "default"]);

  const deleted = applyTLSChange(withPolicy, { kind: "cert", name: "z", deleted: true });
  assert.deepEqual(deleted.certs.map((entry) => entry.name), ["a"]);

  const withBuiltin = applyTLSChange(deleted, {
    kind: "builtin_ca",
    builtin_ca: {
      cert: { name: "builtin-ca:almagest", fingerprint: "abc" },
      options: { issuer: "Almagest", key_algorithm: "ecdsa", curve: "P256" },
    },
  });
  assert.equal(withBuiltin.builtin_ca.options.curve, "P256");
  assert.equal(withBuiltin.certs[0].name, "a");
  assert.equal(withBuiltin.certs[1].name, "builtin-ca:almagest");
});

test("TLS policy JSON parser accepts the API shape and rejects wrong types", () => {
  const spec = parseTLSPolicySpec(JSON.stringify({
    trust_anchors: [{ cert: "root", scope: { any: true } }],
    constraints: [{ cert: "issuer", match: "spki", scope: { dns: [{ domain: "example.test", include_subdomains: true }] } }],
    pins: [{ kind: "certificate", fingerprint: "sha256:abc", scope: { ip_prefixes: ["192.0.2.0/24"] } }],
  }));

  assert.equal(spec.trust_anchors[0].cert, "root");
  assert.equal(spec.constraints[0].scope.dns[0].include_subdomains, true);
  assert.equal(spec.pins[0].scope.ip_prefixes[0], "192.0.2.0/24");
  assert.throws(() => parseTLSPolicySpec("[]"), /must be a JSON object/);
  assert.throws(() => parseTLSPolicySpec('{"trust_anchors":{}}'), /trust_anchors must be an array/);
});

test("TLS policy formatter omits empty arrays and scope summaries are readable", () => {
  assert.equal(formatTLSPolicySpec({ trust_anchors: [], constraints: [], pins: [] }), "{}\n");
  assert.equal(describeTLSScope({ any: true, dns: [{ domain: "ignored.test" }] }), "any server");
  assert.equal(describeTLSScope({ dns: [{ domain: "example.test", include_subdomains: true }], ip_prefixes: ["192.0.2.0/24"] }), "*.example.test, 192.0.2.0/24");
});

test("TLS certcat helpers summarize matches and scopes", () => {
  const entry = {
    name: "root",
    certcat_matches: [
      { severity: "warning", action: "narrow_scope", kind: "narrow_scope_ca" },
      { severity: "critical", action: "deny", kind: "hazard_certificate" },
    ],
  };
  assert.equal(formatTLSCertcatBadge(entry), "critical warning x2");
  assert.equal(formatTLSCertcatMatch(entry.certcat_matches[0]), "Use only for a narrow address scope");
  assert.equal(describeTLSCertcatScope({
    basis: "x509_name_constraints",
    permitted_dns_subtrees: ["example.test"],
    permitted_ip_cidrs: ["192.0.2.0/24"],
    reject_ip_targets: true,
  }), "Use only for DNS names in example.test, Use only for IP ranges in 192.0.2.0/24, Do not use for direct IP targets");
});

test("TLS mutable name guards match backend reserved names", () => {
  assert.equal(isMutableTLSCertName("ca"), true);
  assert.equal(isMutableTLSCertName("os-ca:system"), false);
  assert.equal(isMutableTLSCertName("builtin-ca:almagest"), false);
  assert.equal(isBuiltinTLSCertName("builtin-ca:almagest"), true);
  assert.equal(isMutableTLSCertName(""), false);
  assert.equal(isMutableTLSPolicyName("custom"), true);
  assert.equal(isMutableTLSPolicyName("default"), false);
  assert.equal(isMutableTLSPolicyName(""), false);
});

test("TLS sort helpers order entries by name", () => {
  assert.deepEqual(sortTLSCerts([{ name: "b" }, { name: "a" }]).map((entry) => entry.name), ["a", "b"]);
  assert.deepEqual(
    sortTLSPolicies([{ name: "z", spec: {}, valid: true }, { name: "a", spec: {}, valid: true }]).map((entry) => entry.name),
    ["a", "z"],
  );
});
