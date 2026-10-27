import assert from "node:assert/strict";
import { test } from "node:test";
import { JSDOM } from "jsdom";

const { documentationURL, panelTabURL } = await import("../public/build/site_navigation.js");

function installDOM(url, base = "") {
  const meta = base ? `<meta name="almagest-site-base" content="${base}">` : "";
  const dom = new JSDOM(`<!doctype html>${meta}`, { url });
  globalThis.window = dom.window;
  globalThis.document = dom.window.document;
}

test("desktop documentation links use the packaged page and query route", () => {
  installDOM("tauri://localhost/index.html");
  document.documentElement.dataset.almagestPanelMode = "desktop";

  assert.equal(documentationURL(), "./docs/index.html");
  assert.equal(documentationURL("node/almagest/Tun"), "./docs/index.html?page=node%2Falmagest%2FTun");
});

test("daemon panel documentation links retain path-based routes", () => {
  installDOM("http://127.0.0.1:8090/");

  assert.equal(documentationURL(), "/docs/");
  assert.equal(
    documentationURL("node/almagest/Network Firewall?#"),
    "/docs/node/almagest/Network%20Firewall%3F%23",
  );
});

test("web demo panel documentation links retain the static site base path", () => {
  installDOM("https://example.test/almagest/demo/", "../");

  assert.equal(documentationURL(), "../docs/");
  assert.equal(
    documentationURL("node/almagest/NetworkFirewall"),
    "../docs/?page=node%2Falmagest%2FNetworkFirewall",
  );
});

test("web demo documentation links support an absolute configured site root", () => {
  installDOM("https://cdn.example.test/demo/", "https://example.test/almagest");

  assert.equal(
    documentationURL("guide/welcome"),
    "https://example.test/almagest/docs/?page=guide%2Fwelcome",
  );
});

test("panel tab links retain relative daemon, desktop, and web demo routes", () => {
  assert.equal(panelTabURL("/", "logs"), "/#logs");
  assert.equal(panelTabURL("../index.html", "config"), "../index.html#config");
  assert.equal(panelTabURL("../demo/", "sec"), "../demo/#sec");
  assert.equal(panelTabURL("../../custom/demo/#old", "process logs"), "../../custom/demo/#process%20logs");
});
