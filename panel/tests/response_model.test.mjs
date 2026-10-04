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
