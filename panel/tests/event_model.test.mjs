import assert from "node:assert/strict";
import { test } from "node:test";
import {
  parseDNSCacheEvent,
  parseDNSKeyEvent,
  parseDNSValueEvent,
  parseKVEntriesEvent,
  parseKVStoresEvent,
  parseKVValueEvent,
  parseLogEntry,
  parseProjectVersionStatus,
  parseSecretsEvent,
  parseTLSEvent,
} from "../public/build/event_model.js";

const payload = '<img src=x onerror="globalThis.__xss = true">';

test("admin and log event decoders accept text without interpreting it", () => {
  assert.deepEqual(parseProjectVersionStatus({
    version: payload,
    highlight: "warn",
    hover_message: payload,
  }), {
    version: payload,
    highlight: "warn",
    hover_message: payload,
  });
  assert.deepEqual(parseLogEntry({
    at: "2026-01-01T00:00:00Z",
    source: payload,
    level: "error",
    message: payload,
    node_id: 4,
  })?.message, payload);
});

test("admin and log event decoders reject malformed fields", () => {
  assert.equal(parseProjectVersionStatus({ version: "1", highlight: "javascript:alert(1)" }), null);
  assert.equal(parseProjectVersionStatus({ version: 1, highlight: "none" }), null);
  assert.equal(parseLogEntry({ at: "now", source: "daemon", level: "info", message: {} }), null);
  assert.equal(parseLogEntry({ at: "now", source: "daemon", level: "info", message: "ok", node_id: NaN }), null);
});

test("storage event decoders accept valid subscription updates", () => {
  assert.deepEqual(parseDNSCacheEvent({
    subscription_id: 1,
    change: { name: payload },
  })?.change.name, payload);
  assert.deepEqual(parseDNSKeyEvent({
    subscription_id: 2,
    cache: payload,
    change: { key: payload, entry: { key: payload, size: 12 } },
  })?.change.entry?.key, payload);
  assert.deepEqual(parseDNSValueEvent({
    subscription_id: 3,
    cache: payload,
    key: payload,
    found: true,
    value: { json: payload },
  })?.value?.json, payload);
  assert.deepEqual(parseKVStoresEvent({
    subscription_id: 4,
    snapshot: { stores: [{ kind: "plugin", name: payload }] },
  })?.snapshot.stores[0]?.name, payload);
  assert.equal(parseKVEntriesEvent({
    subscription_id: 5,
    snapshot: { exists: true, entries: [{ kind: "value", name: payload, hidden: true }] },
  })?.snapshot.entries?.[0]?.name, payload);
  assert.equal(parseKVValueEvent({
    subscription_id: 6,
    snapshot: { found: true, value: { data: payload, encoding: "text" } },
  })?.snapshot.value?.data, payload);
  assert.equal(parseSecretsEvent({
    subscription_id: 7,
    change: { key: payload, entry: { key: payload, value: payload, nodes: [payload] } },
  })?.change.entry?.value, payload);
  assert.equal(parseTLSEvent({
    subscription_id: 8,
    change: { kind: "cert", name: payload, cert: { name: payload } },
  })?.change.name, payload);
  assert.equal(parseTLSEvent({
    subscription_id: 9,
    change: {
      kind: "policy",
      name: payload,
      policy: {
        name: payload,
        valid: true,
        spec: {
          trust_anchors: [{ cert: payload, scope: { dns: [{ domain: payload, include_subdomains: true }] } }],
          constraints: [{ cert: payload, match: "spki", scope: { any: true } }],
          pins: [{ kind: "spki", fingerprint: payload, scope: { ip_prefixes: ["192.0.2.0/24"] } }],
        },
      },
    },
  })?.change.policy?.spec.pins?.[0]?.fingerprint, payload);
});

test("storage event decoders reject invalid nested shapes", () => {
  const invalid = [
    parseDNSCacheEvent({ subscription_id: "1", change: { name: "cache" } }),
    parseDNSKeyEvent({ subscription_id: 1, cache: "cache", change: { key: 2 } }),
    parseDNSValueEvent({ subscription_id: 1, cache: "cache", key: "key", found: "yes" }),
    parseKVStoresEvent({ subscription_id: 1, snapshot: { stores: [{ kind: "remote" }] } }),
    parseKVEntriesEvent({ subscription_id: 1, snapshot: { exists: true, entries: [{ kind: "value" }] } }),
    parseKVValueEvent({ subscription_id: 1, snapshot: { found: true, value: { size: "large" } } }),
    parseSecretsEvent({ subscription_id: 1, change: { key: "key", entry: { key: "key", value: [] } } }),
    parseTLSEvent({ subscription_id: 1, change: { kind: "script", name: "bad" } }),
    parseTLSEvent({ subscription_id: 1, change: { kind: "cert", name: "bad", cert: {} } }),
    parseTLSEvent({
      subscription_id: 1,
      change: {
        kind: "cert",
        name: "bad",
        cert: { name: "bad", certcat_matches: [{ scope: { permitted_dns_subtrees: {} } }] },
      },
    }),
    parseTLSEvent({ subscription_id: 1, change: { kind: "policy", name: "bad", policy: { name: "bad" } } }),
    parseTLSEvent({
      subscription_id: 1,
      change: {
        kind: "policy",
        name: "bad",
        policy: { name: "bad", valid: true, spec: { trust_anchors: {} } },
      },
    }),
    parseTLSEvent({
      subscription_id: 1,
      change: {
        kind: "policy",
        name: "bad",
        policy: { name: "bad", valid: true, spec: { pins: [{ kind: "spki", fingerprint: "sha256:x", scope: [] }] } },
      },
    }),
    parseTLSEvent({
      subscription_id: Number.MAX_SAFE_INTEGER + 1,
      change: { kind: "cert", name: "cert", cert: { name: "cert" } },
    }),
  ];
  assert.deepEqual(invalid, Array(invalid.length).fill(null));
});
