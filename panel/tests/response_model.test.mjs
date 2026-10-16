import assert from "node:assert/strict";
import { test } from "node:test";
import {
  parseConfigUpdateResponse,
  parseDNSSubscribeCachesResponse,
  parseHelloResponse,
  parseKVSubscribeValueResponse,
  parseSecretsSubscribeResponse,
  parseSnapshotExportResponse,
  parseSubscribeLogsResponse,
  parseTLSBuiltinCAResponse,
  parseTLSSubscribeResponse,
} from "../public/build/response_model.js";

const payload = '<img src=x onerror="globalThis.__xss = true">';

test("response decoders preserve untrusted text only after shape validation", () => {
  assert.equal(parseHelloResponse({
    version: payload,
    execution_mode: "direct",
    project_version_status: { version: payload, highlight: "warn", hover_message: payload },
  })?.project_version_status.hover_message, payload);
  assert.equal(parseSubscribeLogsResponse({
    subscription_id: 1,
    cache_size: 10,
    entries: [{ at: "now", source: payload, level: "info", message: payload }],
  })?.entries[0]?.message, payload);
  assert.equal(parseDNSSubscribeCachesResponse({
    subscription_id: 2,
    snapshot: { caches: [{ name: payload }] },
  })?.snapshot.caches?.[0]?.name, payload);
  assert.equal(parseSecretsSubscribeResponse({
    subscription_id: 3,
    snapshot: { secrets: [{ key: payload, value: payload, nodes: [payload] }] },
  })?.snapshot.secrets?.[0]?.value, payload);
  assert.equal(parseTLSBuiltinCAResponse({
    cert: { name: "builtin-ca:almagest", pem: payload },
    crl: { pem: payload, number: "1", issuer: payload, this_update: "now", next_update: "later" },
    options: { issuer: payload },
  })?.crl.pem, payload);
});

test("hello decoder accepts isolated status and rejects mode/status mismatch", () => {
  const isolated = parseHelloResponse({
    version: "1",
    execution_mode: "isolation",
    project_version_status: { version: "1", highlight: "none" },
    isolation: {
      protocol_version: 1,
      restrictions: { runtime: ["seccomp", "landlock"] },
      roles: { network: "ready", runtime: "ready" },
      capabilities: { native_network: true },
      recovery_state: "complete",
    },
  });
  assert.equal(isolated?.isolation?.roles.runtime, "ready");
  assert.equal(parseHelloResponse({ ...isolated, execution_mode: "direct" }), null);
});

test("hello decoder validates subprogram sandbox status", () => {
  const subprogram_sandbox = {
    mode: "auto", platform: "linux", effective_identity: "1000",
    limits: { instances: 32 }, active_owners: 1, orphaned_owners: 2, quarantined_owners: 0,
    templates: { tor: { profile: "linux-landlock-v1", effective_identity: "1000:1000", strict_satisfied: false, full_boundary: false,
      features: [{ feature: "filesystem-access", state: "applied", reason: "qualified-backend" }],
      warnings: ["private-network: backend-unavailable"] } },
  };
  const hello = { version: "1", execution_mode: "direct",
    project_version_status: { version: "1", highlight: "none" }, subprogram_sandbox };
  assert.equal(parseHelloResponse(hello)?.subprogram_sandbox?.templates.tor?.features[0]?.state, "applied");
  assert.equal(parseHelloResponse({ ...hello, subprogram_sandbox: { ...subprogram_sandbox, mode: "best-effort" } }), null);
  assert.equal(parseHelloResponse({ ...hello, subprogram_sandbox: { ...subprogram_sandbox, active_owners: -1 } }), null);
});

test("hello decoder validates adaptive isolation status", () => {
  const isolation = {
    protocol_version: 1,
    status_schema_version: 1,
    restrictions: { runtime: ["seccomp"] },
    roles: { network: "ready", runtime: "ready" },
    capabilities: { native_network: true },
    recovery_state: "complete",
    requested_policy: "auto",
    profile_id: "linux-user-v1",
    profile_revision: 1,
    verification_state: "verified",
    reduced: true,
    mechanisms: {
      network: { "capability-sets": { state: "applied" } },
      runtime: { seccomp: { state: "applied" }, "role-cgroups": { state: "unavailable", reason: "not-delegated" } },
    },
    credentials: {
      network: { real_uid: 1000, effective_uid: 1000, saved_uid: 1000, filesystem_uid: 1000,
        real_gid: 1000, effective_gid: 1000, saved_gid: 1000, filesystem_gid: 1000, supplementary_groups: [1000] },
      runtime: { real_uid: 1000, effective_uid: 1000, saved_uid: 1000, filesystem_uid: 1000,
        real_gid: 1000, effective_gid: 1000, saved_gid: 1000, filesystem_gid: 1000, supplementary_groups: [] },
    },
    capability_sets: {
      network: { inheritable: ["CAP_NET_ADMIN"], permitted: ["CAP_NET_ADMIN"], effective: ["CAP_NET_ADMIN"],
        bounding: ["CAP_NET_ADMIN"], ambient: ["CAP_NET_ADMIN"] },
      runtime: { inheritable: [], permitted: [], effective: [], bounding: ["CAP_NET_ADMIN"], ambient: [] },
    },
    resource_limits: {
      runtime: [{ name: "descriptors", requested: "2048", effective: "2048", unit: "count",
        scope: "process", source: "configured", enforcement: "applied" }],
    },
    warnings: [{ role: "runtime", code: "not-delegated", effect: "No per-role cgroup ceiling.", feature: "A memory ceiling is unavailable." }],
    observation_time: "2026-10-15T12:00:00Z",
    selected_plan_digest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
  };
  const hello = { version: "1", execution_mode: "isolation", project_version_status: { version: "1", highlight: "none" }, isolation };
  const parsed = parseHelloResponse(hello);
  assert.equal(parsed?.isolation?.mechanisms?.runtime?.["role-cgroups"]?.reason, "not-delegated");
  assert.equal(parsed?.isolation?.capability_sets?.network?.effective[0], "CAP_NET_ADMIN");
  for (const invalid of [
    { ...isolation, verification_state: "ready" },
    { ...isolation, status_schema_version: 2 },
    { ...isolation, profile_id: "custom" },
    { ...isolation, mechanisms: { runtime: { seccomp: { state: "enabled" } } } },
    { ...isolation, capability_sets: { runtime: { ...isolation.capability_sets.runtime, effective: ["CAP_MAGIC"] } } },
    { ...isolation, resource_limits: { runtime: [{ ...isolation.resource_limits.runtime[0], scope: "host" }] } },
    { ...isolation, selected_plan_digest: "ABC" },
    { ...isolation, warnings: Array(33).fill(isolation.warnings[0]) },
  ]) assert.equal(parseHelloResponse({ ...hello, isolation: invalid }), null);
});

test("response decoders reject malformed nested storage and TLS values", () => {
  const invalid = [
    parseHelloResponse({ version: "v1", project_version_status: { version: "v1", highlight: "script" } }),
    parseSubscribeLogsResponse({ subscription_id: 1, cache_size: -1, entries: [] }),
    parseDNSSubscribeCachesResponse({ subscription_id: 1, snapshot: { caches: [{ name: [] }] } }),
    parseKVSubscribeValueResponse({
      subscription_id: 1,
      snapshot: { found: true, value: { data: payload, hidden: "yes" } },
    }),
    parseSecretsSubscribeResponse({
      subscription_id: 1,
      snapshot: { secrets: [{ key: "key", value: payload, nodes: {} }] },
    }),
    parseTLSSubscribeResponse({
      subscription_id: 1,
      snapshot: {
        policies: [{ name: "policy", valid: true, spec: { trust_anchors: [{ cert: "ca", scope: { dns: {} } }] } }],
      },
    }),
    parseTLSBuiltinCAResponse({ cert: { name: "builtin-ca:almagest" }, options: {} }),
  ];
  assert.deepEqual(invalid, Array(invalid.length).fill(null));
});

test("config and snapshot response decoders validate download metadata", () => {
  assert.deepEqual(parseConfigUpdateResponse({
    snapshot: { text: payload, revision: "rev-1", read_only: false },
    changes: { added: [payload], removed: [] },
  }), {
    snapshot: { text: payload, revision: "rev-1", read_only: false },
    changes: { added: [payload], removed: [] },
  });

  const snapshot = {
    uuid: "11111111-1111-4111-8111-111111111111",
    timestamp: 1788955200000000000,
    name: payload,
  };
  assert.deepEqual(parseSnapshotExportResponse({ snapshot, archive: "UEsDBAo=" }), {
    snapshot,
    archive: "UEsDBAo=",
  });
  assert.equal(parseSnapshotExportResponse({
    snapshot: { ...snapshot, uuid: "../../payload.html" },
    archive: "UEsDBAo=",
  }), null);
});
