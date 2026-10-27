import assert from "node:assert/strict";
import { test } from "node:test";
import { JSDOM } from "jsdom";
import {
  graphPreviewPorts,
  parseGraphPreviewConfig,
  renderGraphPreview,
} from "../public/build/graph_preview.js";

function graphSource() {
  return `{
    // Preview comments use daemon-config syntax.
    "Router": {
      "Class": "demo/Router",
      "Pos": "{\\"x\\":300,\\"y\\":120}",
      "Primary": "demo/network",
      "Label": "routes <script> as text",
      "Links": [
        "slot 2 -> [Sink Two] Network",
        "slot 3 -> [Sink Three] Network",
        "slot 4 -> [Sink Two] Network",
      ],
    },
    "Sink Two": {"Class": "demo/Sink"},
    "Sink Three": {"Class": "demo/Sink"},
  }`;
}

function classInfo(className, initialPorts) {
  return {
    snapshot: {
      class: className,
      short_description: "",
      unique: false,
      primary_type: "demo/network",
      initial_ports: initialPorts,
      experimental: false,
    },
    colormap: { "demo/network": { type: "#123456", text: "#ffffff" } },
  };
}

test("graph previews parse HuJSON, positions, labels, links, and full config wrappers", () => {
  const direct = parseGraphPreviewConfig(graphSource());
  assert.equal(direct.nodes.length, 3);
  assert.deepEqual(direct.nodes[0]?.position, [300, 120]);
  assert.equal(direct.nodes[0]?.primaryType, "demo/network");
  assert.equal(direct.nodes[0]?.label, "routes <script> as text");
  assert.deepEqual(direct.nodes[0]?.links[1], {
    sourceNode: "Router",
    sourcePort: "slot 3",
    targetNode: "Sink Three",
    targetPort: "Network",
  });

  const wrapped = parseGraphPreviewConfig(`{"nodes": ${graphSource()}, "log": {}}`);
  assert.deepEqual(wrapped, direct);
});

test("graph previews add linked dynamic ports that class descriptions omit", () => {
  const config = parseGraphPreviewConfig(graphSource());
  const classes = new Map([
    ["demo/Router", classInfo("demo/Router", [
      { direction: "input", name: "Network", types: ["demo/network"] },
      { direction: "output", name: "slot 1", types: ["demo/network"] },
    ])],
    ["demo/Sink", classInfo("demo/Sink", [
      { direction: "input", name: "Network", types: ["demo/network"] },
    ])],
  ]);

  const ports = graphPreviewPorts(config, classes);
  assert.deepEqual(ports.get("Router")?.right, [
    { name: "slot 1", types: ["demo/network"] },
    { name: "slot 2", types: ["demo/network"] },
    { name: "slot 3", types: ["demo/network"] },
    { name: "slot 4", types: ["demo/network"] },
  ]);
  assert.deepEqual(ports.get("Sink Two")?.left, [
    { name: "Network", types: ["demo/network"] },
    { name: "Network", types: ["demo/network"] },
  ]);
  assert.deepEqual(ports.get("Sink Three")?.left, [{ name: "Network", types: ["demo/network"] }]);
});

test("graph preview parser rejects malformed and excessive untrusted input", () => {
  assert.throws(() => parseGraphPreviewConfig("[]"), /must be an object/);
  assert.throws(() => parseGraphPreviewConfig('{"A":{"Class":"demo/A","Links":"bad"}}'), /array of strings/);
  assert.throws(() => parseGraphPreviewConfig('{"A":{"Class":"demo/A","Pos":4}}'), /Pos must be a string/);
  assert.throws(() => parseGraphPreviewConfig('{"A":{"Class":"demo/A","Primary":4}}'), /Primary must be a string/);
  assert.throws(() => parseGraphPreviewConfig('{"A":{"Class":"demo/A","Links":["bad"]}}'), /invalid link/);
  assert.throws(
    () => parseGraphPreviewConfig('{"A":{"Class":"demo/A","Links":["Out -> [missing] In"]}}'),
    /does not exist/,
  );
  assert.throws(() => parseGraphPreviewConfig(" ".repeat(256 * 1024 + 1)), /too large/);
  assert.throws(() => parseGraphPreviewConfig('{/* unterminated'), /unterminated comment/);
});

test("graph preview draws one inert centered frame and keeps hostile labels out of HTML", async () => {
  const dom = new JSDOM("<!doctype html><main></main>", { url: "https://panel.test/" });
  globalThis.window = dom.window;
  globalThis.document = dom.window.document;
  const calls = {
    options: null,
    draw: 0,
    detached: 0,
    stopped: 0,
    offsets: null,
    scale: null,
    size: null,
    showInfo: null,
    connections: [],
  };

  class FakeNode {
    constructor() {
      this.properties = {};
      this.inputs = [];
      this.outputs = [];
      this.size = [140, 80];
      this.pos = [0, 0];
    }
    addInput(name, type, options) { this.inputs.push({ name, type, ...options }); }
    addOutput(name, type, options) { this.outputs.push({ name, type, ...options }); }
    computeSize() { return [140, 80]; }
    getBounding() { return [this.pos[0], this.pos[1], this.size[0], this.size[1]]; }
    connect(output, target, input) {
      calls.connections.push({ source: this.title, output, target: target.title, input });
      return { output, target, input };
    }
  }
  class FakeGraph {
    constructor() { this._nodes = []; }
    add(node) { this._nodes.push(node); }
    stop() { calls.stopped += 1; }
  }
  class FakeCanvas {
    constructor(element, graph, options) {
      calls.options = options;
      calls.size = [element.width, element.height];
      this.graph = graph;
      this.ds = { scale: 1, offset: [0, 0] };
    }
    draw() { calls.draw += 1; calls.offsets = [...this.ds.offset]; calls.scale = this.ds.scale; calls.showInfo = this.show_info; }
    stopRendering() {}
    setGraph(value) { if (value === null) calls.detached += 1; }
  }
  globalThis.LiteGraph = {
    allow_scripts: true,
    registered_node_types: {},
    registerNodeType(name, constructor) { this.registered_node_types[name] = constructor; },
    createNode(name) { return new FakeNode(this.registered_node_types[name]); },
  };
  globalThis.LGraph = FakeGraph;
  globalThis.LGraphCanvas = FakeCanvas;

  const host = document.querySelector("main");
  Object.defineProperty(host, "clientWidth", { value: 800 });
  await renderGraphPreview(host, graphSource(), async (name) => classInfo(name, []), {
    width: 640,
    height: 320,
    zoom: 2,
    padding: { top: 8, right: 16, bottom: 24, left: 32 },
  });

  assert.deepEqual(calls.options, { skip_render: true, skip_events: true });
  assert.equal(calls.draw, 1);
  assert.equal(calls.detached, 1);
  assert.equal(calls.stopped, 1);
  assert.deepEqual(calls.size, [640, 320]);
  assert.equal(calls.scale, 2);
  assert.notDeepEqual(calls.offsets, [0, 0]);
  assert.equal(calls.showInfo, false);
  assert.equal(globalThis.LiteGraph.allow_scripts, false);
  assert.deepEqual(
    calls.connections.filter((connection) => connection.target === "Sink Two").map((connection) => connection.input),
    [0, 1],
  );
  assert.equal(host.querySelector("canvas")?.getAttribute("role"), "img");
  assert.equal(host.querySelector("script,img,svg,iframe"), null);
  assert.equal(host.textContent, "");
});

test("graph preview errors are plain text", async () => {
  const dom = new JSDOM("<!doctype html><main></main>");
  globalThis.window = dom.window;
  globalThis.document = dom.window.document;
  const host = document.querySelector("main");
  await renderGraphPreview(host, '{"<img src=x>": {"Class": 4}}', async () => null);
  assert.match(host.textContent, /<img src=x>/);
  assert.equal(host.querySelector("img,script,svg"), null);
});

test("graph preview rejects invalid image options before LiteGraph starts", async () => {
  const dom = new JSDOM("<!doctype html><main></main>");
  globalThis.window = dom.window;
  globalThis.document = dom.window.document;
  const host = document.querySelector("main");
  await renderGraphPreview(host, graphSource(), async (name) => classInfo(name, []), {
    width: 0,
    padding: { left: -1 },
  });
  assert.match(host.textContent, /width must be a positive number/);
  assert.equal(host.querySelector("canvas"), null);
});
