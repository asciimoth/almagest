import assert from "node:assert/strict";
import { test } from "node:test";
import {
  applyDNSCacheChange,
  applyDNSKeyChange,
  desiredDNSKeySubscriptionKeys,
  dnsCacheNodeKey,
  dnsRecordsFromValue,
  editableDNSRecordFromValue,
  formatDNSCacheKey,
  parseDNSCacheNodeKey,
} from "../public/build/dns_model.js";

test("DNS cache node keys round-trip untrusted names without delimiter parsing", () => {
  const cache = `resolver/cache|"x"`;
  const key = dnsCacheNodeKey(cache);
  const parsed = parseDNSCacheNodeKey(key);

  assert.deepEqual(parsed, { cache });
});

test("DNS desired key subscriptions include only expanded caches", () => {
  const caches = [{ name: "bootstrap" }, { name: "resolver" }];
  const bootstrap = dnsCacheNodeKey(caches[0]);
  const resolver = dnsCacheNodeKey(caches[1]);
  const stale = dnsCacheNodeKey("old-cache");

  const expanded = new Set([bootstrap, stale]);
  assert.deepEqual([...desiredDNSKeySubscriptionKeys(caches, expanded)], [bootstrap]);

  expanded.add(resolver);
  expanded.delete(bootstrap);
  assert.deepEqual([...desiredDNSKeySubscriptionKeys(caches, expanded)], [resolver]);
});

test("DNS cache changes upsert, delete, and keep deterministic order", () => {
  const caches = [{ name: "resolver" }];
  assert.deepEqual(applyDNSCacheChange(caches, { name: "bootstrap" }), [
    { name: "bootstrap" },
    { name: "resolver" },
  ]);
  assert.deepEqual(applyDNSCacheChange(caches, { name: "resolver", deleted: true }), []);
});

test("DNS key changes merge metadata without disturbing cache existence", () => {
  const snapshot = {
    exists: true,
    keys: [
      { key: "b", size: 20 },
      { key: "a", size: 10 },
    ],
  };

  const upserted = applyDNSKeyChange(snapshot, { key: "b", entry: { key: "b", size: 30, expires_at: "2026-07-20T00:00:00Z" } });
  assert.deepEqual(upserted, {
    exists: true,
    keys: [
      { key: "a", size: 10 },
      { key: "b", size: 30, expires_at: "2026-07-20T00:00:00Z" },
    ],
  });

  assert.deepEqual(applyDNSKeyChange(upserted, { key: "a", deleted: true }), {
    exists: true,
    keys: [
      { key: "b", size: 30, expires_at: "2026-07-20T00:00:00Z" },
    ],
  });
});

test("DNS cache keys render as DNS question records", () => {
  assert.equal(formatDNSCacheKey("example.test.|1|1"), "example.test. IN A");
  assert.equal(formatDNSCacheKey("example.test.|28|1"), "example.test. IN AAAA");
  assert.equal(formatDNSCacheKey("custom-key"), "custom-key");
});

test("DNS value JSON renders resource data as readable DNS records", () => {
  const value = {
    json: JSON.stringify({
      message: {
        questions: [{ name: "example.test.", type: 1, class: 1 }],
        answers: [
          { name: "example.test.", type: 1, class: 1, ttl: 60, data: b64([192, 0, 2, 40]) },
          { name: "v6.example.test.", type: 28, class: 1, ttl: 60, data: b64([0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x10]) },
          { name: "example.test.", type: 15, class: 1, ttl: 60, data: b64([0, 10, ...bytes("mail.example.test.")]) },
          { name: "example.test.", type: 16, class: 1, ttl: 60, data: b64(bytes("hello world")) },
        ],
      },
    }),
  };

  assert.deepEqual(dnsRecordsFromValue(value), [
    {
      title: "Questions",
      records: [{ name: "example.test.", type: "A", class: "IN" }],
    },
    {
      title: "Answers",
      records: [
        { name: "example.test.", type: "A", class: "IN", ttl: 60, value: "192.0.2.40" },
        { name: "v6.example.test.", type: "AAAA", class: "IN", ttl: 60, value: "2001:db8::10" },
        { name: "example.test.", type: "MX", class: "IN", ttl: 60, value: "10 mail.example.test." },
        { name: "example.test.", type: "TXT", class: "IN", ttl: 60, value: "hello world" },
      ],
    },
  ]);
});

test("DNS editable record drafts prefer decoded answer data", () => {
  const value = {
    json: JSON.stringify({
      message: {
        questions: [{ name: "example.test.", type: 1, class: 1 }],
        answers: [
          { name: "example.test.", type: 15, class: 1, ttl: 120, data: b64([0, 20, ...bytes("mail.example.test.")]) },
        ],
      },
    }),
  };

  assert.deepEqual(editableDNSRecordFromValue("example.test.|1|1", value), {
    name: "example.test.",
    type: "MX",
    class: "IN",
    ttl: 120,
    value: "20 mail.example.test.",
  });
});

function b64(bytes) {
  return Buffer.from(bytes).toString("base64");
}

function bytes(text) {
  return [...Buffer.from(text)];
}
