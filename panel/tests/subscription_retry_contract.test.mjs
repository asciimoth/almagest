import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";

const app = await readFile(new URL("../src/app.ts", import.meta.url), "utf8");

function sourceBetween(start, end) {
  const startIndex = app.indexOf(start);
  const endIndex = app.indexOf(end, startIndex);
  assert.notEqual(startIndex, -1);
  assert.notEqual(endIndex, -1);
  return app.slice(startIndex, endIndex);
}

test("failed DNS tree subscriptions do not retry during their failure render", () => {
  const sync = sourceBetween("function syncDNSKeySubscriptions", "async function subscribeDNSKeys");
  const subscribe = sourceBetween("async function subscribeDNSKeys", "function dnsNodeKeyForKeySubscription");

  assert.match(sync, /!dnsKeySubscribeFailed\.has\(nodeKey\)/);
  assert.match(subscribe, /if \(!response\) \{\s+dnsKeySubscribeFailed\.add\(nodeKey\);[\s\S]*?renderDNS\(\)/);
});

test("failed KV tree subscriptions do not retry during their failure render", () => {
  const sync = sourceBetween("function syncKVEntrySubscriptions", "async function subscribeKVEntries");
  const subscribe = sourceBetween("async function subscribeKVEntries", "function kvNodeKeyForEntrySubscription");

  assert.match(sync, /!kvEntrySubscribeFailed\.has\(nodeKey\)/);
  assert.match(subscribe, /if \(!response\) \{\s+kvEntrySubscribeFailed\.add\(nodeKey\);[\s\S]*?renderKV\(\)/);
});
