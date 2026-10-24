import assert from "node:assert/strict";
import { afterEach, test } from "node:test";
import { JSDOM } from "jsdom";

const originalFetch = globalThis.fetch;

function installDOM(base = "https://example.test/almagest/", url = `${base}docs/`) {
  const dom = new JSDOM(
    `<!doctype html><meta name="almagest-site-base" content="${base}">`,
    { url },
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

function nodeClassCache() {
  return {
    version: 1,
    order: ["demo/Unsafe<img>"],
    colormap: { "demo/net": { type: "red", text: "white" } },
    classes: {
      "demo/Unsafe<img>": {
        primary_type: "demo/net",
        initial_ports: [{ direction: "left", name: "Input<script>", types: ["demo/net"] }],
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
      available: "This node is available in the web demo.",
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

test("static documentation can use a site base resolved from the web demo page", async () => {
  installDOM("../", "https://example.test/almagest/demo/");
  const requests = [];
  globalThis.fetch = async (input) => {
    requests.push(String(input));
    if (String(input).endsWith("catalog.json")) return Response.json(catalog());
    return new Response("Web demo documentation");
  };

  const client = new StaticDocsClient(new URL("../", window.location.href));
  assert.equal(await client.raw("en", "guide/welcome"), "Web demo documentation");
  assert.deepEqual(requests, [
    "https://example.test/almagest/docs/content/catalog.json",
    "https://example.test/almagest/docs/content/en/pages/guides/welcome.md",
  ]);
});

test("static documentation falls back to English and rejects missing node classes", async () => {
  installDOM();
  globalThis.fetch = async (input) => Response.json(String(input).endsWith("node-classes.json") ? nodeClassCache() : catalog());
  const client = new StaticDocsClient();

  assert.equal((await client.tree("lorem")).locale, "en");
  await assert.rejects(client.raw("en", "missing"), /Documentation page was not found/);
  await assert.rejects(client.nodeClass("en", "guide/welcome"), /Node documentation page was not specified/);
  await assert.rejects(client.nodeClass("en", "node/almagest/Test"), /Node class was not found/);
});

test("static documentation loads cached node preview data without interpreting text", async () => {
  installDOM();
  const requests = [];
  globalThis.fetch = async (input) => {
    requests.push(String(input));
    return Response.json(nodeClassCache());
  };
  const client = new StaticDocsClient();

  const response = await client.nodeClass("en", "node/demo/Unsafe<img>");
  assert.deepEqual(response, {
    snapshot: {
      class: "demo/Unsafe<img>",
      short_description: "",
      unique: false,
      primary_type: "demo/net",
      initial_ports: [{ direction: "left", name: "Input<script>", types: ["demo/net"] }],
      experimental: false,
    },
    colormap: { "demo/net": { type: "red", text: "white" } },
  });
  await client.nodeClass("en", "node/demo/Unsafe<img>");
  assert.deepEqual(requests, ["https://example.test/almagest/docs/content/node-classes.json"]);
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

  globalThis.fetch = async () => Response.json({ version: 2, order: [], classes: {}, colormap: {} });
  await assert.rejects(new StaticDocsClient().nodeClass("en", "node/demo/Test"), /Invalid node class cache/);
});
