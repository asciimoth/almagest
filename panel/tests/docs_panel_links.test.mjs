import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import { test } from "node:test";

const pagesDirectory = new URL("../../locales/en/docs/pages/almagest/", import.meta.url);
const panelTabs = new Set([
  "graph",
  "sec",
  "dns",
  "tls",
  "kv",
  "ps",
  "logs",
  "config",
  "snapshots",
]);

test("Almagest topic panel links use every real panel tab route", async () => {
  const destinations = [];
  for (const file of await readdir(pagesDirectory)) {
    if (!file.endsWith(".md")) continue;
    const source = await readFile(new URL(file, pagesDirectory), "utf8");
    destinations.push(...[...source.matchAll(/\]\(@panel\/([^)]+)\)/g)].map((match) => match[1]));
  }

  assert.deepEqual(new Set(destinations), panelTabs);
  assert.equal(destinations.every((destination) => panelTabs.has(destination)), true);
});
