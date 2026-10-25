import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const app = readFileSync(new URL("../src/app.ts", import.meta.url), "utf8");

test("hidden log updates do not render DOM during API startup", () => {
  const subscribe = app.slice(
    app.indexOf('case "subscribeLogs"'),
    app.indexOf('case "dnsSubscribeCaches"'),
  );
  assert.match(subscribe, /if \(activeTab === "logs"\) renderLogs\(\)/);

  const append = app.slice(
    app.indexOf("function appendLog"),
    app.indexOf("function isConfigSaveFailure"),
  );
  assert.match(append, /frontendLogs\.push\(entry\)[\s\S]*trimLogStore\(\)[\s\S]*if \(activeTab !== "logs"\) return/);
});

test("opening the Logs tab renders the retained history", () => {
  const setTab = app.slice(
    app.indexOf("function setTab"),
    app.indexOf("function moveTabFocus"),
  );
  assert.match(setTab, /if \(name === "logs"\) renderLogs\(\)/);
});
