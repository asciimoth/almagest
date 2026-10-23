import assert from "node:assert/strict";
import { afterEach, test } from "node:test";
import { JSDOM } from "jsdom";

const originalFetch = globalThis.fetch;

function installDOM(url, base = "") {
  const meta = base ? `<meta name="almagest-site-base" content="${base}">` : "";
  const dom = new JSDOM(`<!doctype html>${meta}`, { url });
  globalThis.document = dom.window.document;
  globalThis.window = dom.window;
  return dom;
}

installDOM("http://127.0.0.1:8090/docs/");
const { HTTPDocsClient, siteBaseURL } = await import("../public/build/docs_client.js");

afterEach(() => {
  globalThis.fetch = originalFetch;
});

test("HTTP documentation client preserves daemon path URLs", () => {
  const dom = installDOM("http://127.0.0.1:8090/docs/node/almagest/NetworkFirewall");
  const client = new HTTPDocsClient();

  assert.equal(client.pageFromLocation(dom.window.location), "node/almagest/NetworkFirewall");
  assert.equal(client.pageURL("node/almagest/NetworkFirewall"), "/docs/node/almagest/NetworkFirewall");
  assert.equal(client.pageURL("guide/name with spaces", "ka", "#part"), "/docs/guide/name%20with%20spaces?locale=ka#part");
});

test("HTTP documentation route parser handles root, index, trailing slash, escapes, and unrelated paths", () => {
  const client = new HTTPDocsClient();
  const pageAt = (path) => client.pageFromLocation(installDOM(`https://panel.test${path}`).window.location);

  assert.equal(pageAt("/docs/"), "");
  assert.equal(pageAt("/docs/index.html"), "");
  assert.equal(pageAt("/docs/guide/welcome/"), "guide/welcome");
  assert.equal(pageAt("/docs/guide/name%20with%20spaces"), "guide/name with spaces");
  assert.equal(pageAt("/docs/guide/%E0%A4%A"), "");
  assert.equal(pageAt("/documentation/guide/welcome"), "");
});

test("HTTP documentation URLs encode each page segment and retain locale and anchor", () => {
  installDOM("https://panel.test/docs/current?old=1#old");
  const client = new HTTPDocsClient();

  assert.equal(client.pageURL(""), "/docs/");
  assert.equal(
    client.pageURL("node/vendor class/name?#", "pt-BR", "#input%20port"),
    "/docs/node/vendor%20class/name%3F%23?locale=pt-BR#input%20port",
  );
});

test("HTTP documentation API uses the configured site root and expected request headers", async () => {
  installDOM("https://example.test/almagest/docs/?page=guide%2Fwelcome", "https://example.test/almagest/");
  const requests = [];
  globalThis.fetch = async (input, init) => {
    requests.push([String(input), init]);
    if (String(input).includes("/raw")) return new Response("# Welcome");
    return Response.json({ ok: true });
  };
  const client = new HTTPDocsClient();

  assert.deepEqual(await client.tree("en"), { ok: true });
  assert.equal(await client.raw("lorem", "guide/name with spaces"), "# Welcome");
  assert.deepEqual(await client.nodeClass("", "node/almagest/Test"), { ok: true });

  assert.deepEqual(requests.map(([url]) => url), [
    "https://example.test/almagest/api/docs/tree?locale=en",
    "https://example.test/almagest/api/docs/raw?id=guide%2Fname+with+spaces&locale=lorem",
    "https://example.test/almagest/api/docs/node-class?id=node%2Falmagest%2FTest",
  ]);
  assert.deepEqual(requests.map(([, init]) => init), [
    { credentials: "same-origin", headers: { Accept: "application/json" } },
    { credentials: "same-origin", headers: { Accept: "text/markdown" } },
    { credentials: "same-origin", headers: { Accept: "application/json" } },
  ]);
  assert.equal(siteBaseURL().href, "https://example.test/almagest/");
});

test("HTTP documentation API reports unsuccessful responses", async () => {
  installDOM("https://example.test/docs/", "https://example.test/");
  globalThis.fetch = async () => new Response("missing", { status: 404, statusText: "Not Found" });
  const client = new HTTPDocsClient();

  await assert.rejects(client.tree("en"), /404 Not Found/);
  await assert.rejects(client.raw("en", "missing"), /404 Not Found/);
  await assert.rejects(client.nodeClass("en", "node/missing"), /404 Not Found/);
});
