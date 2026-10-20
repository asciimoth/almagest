import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";

const app = await readFile(new URL("../public/build/app.js", import.meta.url), "utf8");

test("selected experimental nodes show a warning outside both tab panels", () => {
  const warningAppend = app.indexOf("nodePanelHeader.push(createExperimentalNodeWarning())");
  const panelsAppend = app.indexOf("el.sidekick.replaceChildren(...nodePanelHeader, menuPanel, docsPanel)");

  assert.notEqual(warningAppend, -1);
  assert.notEqual(panelsAppend, -1);
  assert.ok(warningAppend < panelsAppend);
  assert.match(app, /nodeClass\?\.experimental/);
});

test("experimental graph palette entries have a warning sign and hover text", () => {
  assert.match(app, /cls\.experimental\s*\?\s*" ⚠"/);
  assert.match(app, /className:\s*"experimental-node-class"/);
  assert.match(app, /entry\.title\s*=\s*localizedText\("node\.experimental\.warning"/);
});
