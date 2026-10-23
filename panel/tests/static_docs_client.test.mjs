import assert from "node:assert/strict";
import { afterEach, test } from "node:test";
import { JSDOM } from "jsdom";

const originalFetch = globalThis.fetch;

function installDOM(base = "https://example.test/almagest/") {
  const dom = new JSDOM(
    `<!doctype html><meta name="almagest-site-base" content="${base}">`,
    { url: `${base}docs/` },
  );
  globalThis.window = dom.window;
  globalThis.document = dom.window.document;
  return dom;
}

installDOM();
const { StaticDocsClient } = await import("../public/build/static_docs_client.js");

afterEach(() => {
  globalThis.fetch = originalFetch;
});

function catalog() {
  return {
    available: [{ locale: "en", name: "English" }],
    locales: {
      en: {
        tree: [{ id: "guide", name: "Guide", pages: [], topics: [] }],
        pages: { "guide/welcome": "content/en/pages/guides/welcome.md" },
      },
    },
  };
}

test("static documentation uses query URLs that work below a hosting subpath", () => {
  const dom = installDOM();
  dom.reconfigure({ url: "https://example.test/almagest/docs/?page=node%2Falmagest%2FMeshNames&locale=en" });
  const client = new StaticDocsClient();

  assert.equal(client.pageFromLocation(dom.window.location), "node/almagest/MeshNames");
  assert.equal(client.pageURL(""), "./");
  assert.equal(
    client.pageURL("guide/name with spaces", "pt-BR", "#section"),
    "./?page=guide%2Fname+with+spaces&locale=pt-BR#section",
  );
});

test("static documentation loads and caches its catalog and raw Markdown", async () => {
  installDOM();
  const requests = [];
  globalThis.fetch = async (input) => {
    const url = String(input);
    requests.push(url);
    if (url.endsWith("catalog.json")) return Response.json(catalog());
    if (url.endsWith("welcome.md")) return new Response("# Welcome");
    return new Response("missing", { status: 404, statusText: "Not Found" });
  };
  const client = new StaticDocsClient();

  const [tree, raw] = await Promise.all([
    client.tree("en"),
    client.raw("en", "guide/welcome"),
  ]);
  assert.deepEqual(tree, {
    locale: "en",
    available: [{ locale: "en", name: "English" }],
    topics: [{ id: "guide", name: "Guide", pages: [], topics: [] }],
    node_unavailable_message: "This node is not available in this web demo.",
    mark_messages: {
      unavailable: "This node is not available in this web demo.",
      deprecated: "This node is deprecated.",
    },
  });
  assert.equal(raw, "# Welcome");
  assert.deepEqual(requests, [
    "https://example.test/almagest/docs/content/catalog.json",
    "https://example.test/almagest/docs/content/en/pages/guides/welcome.md",
  ]);

  await client.tree("en");
  assert.equal(requests.filter((url) => url.endsWith("catalog.json")).length, 1);
});

test("static documentation falls back to English and rejects unavailable features", async () => {
  installDOM();
  globalThis.fetch = async () => Response.json(catalog());
  const client = new StaticDocsClient();

  assert.equal((await client.tree("lorem")).locale, "en");
  await assert.rejects(client.raw("en", "missing"), /Documentation page was not found/);
  await assert.rejects(client.nodeClass("en", "node/almagest/Test"), /Node previews require the browser engine/);
});

test("static documentation reports catalog, Markdown, and missing-English errors", async () => {
  installDOM();
  globalThis.fetch = async () => new Response("bad", { status: 503, statusText: "Unavailable" });
  await assert.rejects(new StaticDocsClient().tree("en"), /503 Unavailable/);

  globalThis.fetch = async (input) => {
    if (String(input).endsWith("catalog.json")) return Response.json(catalog());
    return new Response("missing", { status: 404, statusText: "Not Found" });
  };
  await assert.rejects(new StaticDocsClient().raw("en", "guide/welcome"), /404 Not Found/);

  globalThis.fetch = async () => Response.json({ available: [], locales: {} });
  await assert.rejects(new StaticDocsClient().tree("unknown"), /English documentation is not available/);
});
