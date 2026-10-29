import assert from "node:assert/strict";
import { access, readFile, readdir } from "node:fs/promises";
import { test } from "node:test";
import { JSDOM } from "jsdom";

const source = (path) => new URL(`../${path}`, import.meta.url);
const published = (path) => new URL(`../../app/frontend/${path}`, import.meta.url);

test("desktop publication retains the panel document and uses its native entry point", async () => {
  const sourceDocument = new JSDOM(await readFile(source("index.html"), "utf8")).window.document;
  const appDocument = new JSDOM(await readFile(published("index.html"), "utf8")).window.document;

  assert.deepEqual(
    [...appDocument.querySelectorAll("[id]")].map((element) => element.id).sort(),
    [...sourceDocument.querySelectorAll("[id]")].map((element) => element.id).sort(),
  );
  assert.match(
    appDocument.querySelector("script[type='module']")?.getAttribute("src") ?? "",
    /^\.\/public\/build\/desktop\.js\?v=[a-f0-9]{16}$/,
  );
  assert.match(
    appDocument.querySelector("link[rel='modulepreload']")?.getAttribute("href") ?? "",
    /^\.\/public\/build\/desktop-docs\.js\?v=[a-f0-9]{16}$/,
  );
  assert.deepEqual((await readdir(published("public/build/"))).sort(), ["desktop-docs.js", "desktop.js"]);
  const docsDocument = new JSDOM(await readFile(published("docs/index.html"), "utf8").then(String)).window.document;
  assert.match(docsDocument.querySelector("script[type='module']")?.getAttribute("src") ?? "",
    /^\.\.\/public\/build\/desktop-docs\.js\?v=[a-f0-9]{16}$/);
  assert.match(docsDocument.querySelector("link[rel='modulepreload']")?.getAttribute("href") ?? "",
    /^\.\.\/public\/build\/desktop\.js\?v=[a-f0-9]{16}$/);
  assert.equal(docsDocument.querySelector("meta[http-equiv='Content-Security-Policy']"), null);
  await access(published("vendor/litegraph/litegraph.js"));
  await access(published("vendor/formular-menu/formular-menu.js"));
  await access(published("LUCIDE_LICENSE.txt"));
});

test("desktop publication permits only isolated Tauri IPC", async () => {
  const appDocument = new JSDOM(await readFile(published("index.html"), "utf8")).window.document;
  const desktopEntry = await readFile(source("src/desktop.ts"), "utf8");
  const appSource = await readFile(source("src/app.ts"), "utf8");
  const tauri = JSON.parse(await readFile(new URL("../../app/src-tauri/tauri.conf.json", import.meta.url), "utf8"));
  const capability = JSON.parse(await readFile(new URL("../../app/src-tauri/capabilities/main.json", import.meta.url), "utf8"));

  // A second meta policy would intersect with Tauri's response policy and can
  // block the isolation iframe that carries all IPC requests.
  assert.equal(appDocument.querySelector("meta[http-equiv='Content-Security-Policy']"), null);
  assert.match(desktopEntry, /setPanelMode\(document\.documentElement, "desktop"\)/);
  assert.match(desktopEntry, /configureAdminTransport\(\(\) => new DesktopAdminTransport\(\)\)/);
  // Desktop mode must not bypass normal panel initialization. Compatibility
  // branches for native webviews are permitted when they do not exit it.
  assert.doesNotMatch(appSource, /if \(currentPanelMode === "desktop"\)\s*return;/);
  assert.equal(tauri.app.withGlobalTauri, false);
  assert.equal(tauri.app.security.pattern.use, "isolation");
  assert.equal(tauri.app.security.freezePrototype, true);
  assert.equal(tauri.app.security.assetProtocol.enable, false);
  assert.deepEqual(tauri.app.security.csp, {
    "default-src": "'none'",
    "connect-src": "ipc: http://ipc.localhost",
    "img-src": "'self' data:",
    "script-src": "'self'",
    "style-src": "'self'",
  });
  assert.deepEqual(capability.permissions, ["allow-listen-admin", "allow-send-admin-message"]);
  assert.equal(capability.permissions.some((permission) => String(permission).includes("opener")), false);
});
