import assert from "node:assert/strict";
import { test } from "node:test";
import {
  applyDNSCacheChange,
  applyDNSKeyChange,
  desiredDNSKeySubscriptionKeys,
  dnsCacheNodeKey,
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
