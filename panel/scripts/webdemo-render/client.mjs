// This entry point is served only by render-webdemo-images.mjs. Production
// builds keep their real transports, markup, styles, and rendering code.
import { configureAdminTransport } from "/public/build/admin_transport.js";
import { fitGraphRectangles } from "/public/build/graph_viewport.js";
import { parseWorkspaceSnapshot } from "/public/build/pasta_model.js";

const sceneName = new URL(location.href).searchParams.get("scene");
const errors = [];
window.addEventListener("error", (event) => errors.push(event.message));
window.addEventListener("unhandledrejection", (event) => errors.push(String(event.reason)));
async function render() {
  try {
    const input = await (await fetch(`/input?scene=${sceneName}`)).json();
    const snapshot = buildSnapshot(input);
    if (!parseWorkspaceSnapshot(snapshot)) throw new Error("Fixture failed the panel snapshot parser");
    localStorage.clear();
    configureAdminTransport(() => new FixtureTransport(input, snapshot));
    await import("/public/build/app.js");
    await waitFor(() => document.querySelector("#backend-status").textContent === "Admin API connected");
    const canvas = document.querySelector("#graph-canvas").data;
    await waitFor(() => canvas.graph._nodes.length === Object.keys(snapshot.nodes).length);
    if (input.tab) {
      document.querySelector(`#tab-${input.tab}`).click();
      await waitFor(() => document.querySelectorAll(".snapshot-row").length === input.snapshots.length);
    } else {
      if (input.select) {
        const id = Object.keys(snapshot.nodes).find((key) => snapshot.nodes[key].name === input.select);
        if (!id) throw new Error(`Selected node not found: ${input.select}`);
        window.__almagestPanel.selectNode(Number(id));
        await waitFor(() => document.querySelector("#node-menu .formular-block"));
      }
      await document.fonts.ready;
      await frames();
      const rect = canvas.canvas.getBoundingClientRect();
      canvas.resize(rect.width, rect.height);
      // Node width is a normal LiteGraph layout property. Give adjacent port
      // labels room without replacing the panel's draw or style logic.
      for (const node of canvas.graph._nodes) node.size[0] = Math.max(node.size[0], input.nodeWidth ?? 180);
      const rectangles = canvas.graph._nodes.map((node) => [...node.getBounding(undefined, true)]);
      const fit = fitGraphRectangles(rectangles, rect.width, rect.height, input.padding, 1.4, input.zoom);
      if (fit) {
        canvas.ds.scale = fit.scale;
        canvas.ds.offset.set(fit.offset);
      }
      canvas.show_info = false;
      canvas.draw(true, true);
      // Check the final composition, including title and port extents.
      for (const [x, y, width, height] of rectangles) {
        const left = (x + canvas.ds.offset[0]) * canvas.ds.scale;
        const top = (y + canvas.ds.offset[1]) * canvas.ds.scale;
        if (left < -1 || top < -1 || left + width * canvas.ds.scale > rect.width + 1
          || top + height * canvas.ds.scale > rect.height + 1) throw new Error("Graph is cropped; reduce --zoom or change positions");
      }
    }
    await document.fonts.ready;
    await frames();
    if (document.querySelector("#notifications").textContent.trim()) errors.push(document.querySelector("#notifications").textContent);
    if (errors.length) throw new Error(errors.join("\n"));
    window.__renderReport = { scene: sceneName, ready: true, nodes: Object.keys(snapshot.nodes).length, links: Object.keys(snapshot.links).length };
  } catch (error) {
    window.__renderReport = { scene: sceneName, error: error.stack || String(error) };
  }
}

function buildSnapshot(input) {
  const { cache } = input;
  const snapshot = { nodes: {}, ports: {}, links: {}, workers: {}, classes: (input.palette ?? cache.order).map((name) => ({
    class: name, short_description: input.locale[name] ?? "", unique: false, experimental: false, ...cache.classes[name],
  })) };
  let next = 1;
  const nodesByName = new Map();
  for (const source of input.graph.nodes) {
    const cls = cache.classes[source.className];
    if (!cls) throw new Error(`Unknown node class: ${source.className}`);
    const id = next++;
    const [x, y] = input.positions?.[source.name] ?? source.position;
    const node = { class: source.className, name: input.names?.[source.name] ?? source.name, primary_type: cls.primary_type,
      label: input.labels?.[source.name] ?? source.label, position: JSON.stringify({ x, y }),
      placeholder: false, root: source.className === "almagest/LocalProxy", has_root_path: true,
      left_ports: [], right_ports: [] };
    snapshot.nodes[id] = node;
    nodesByName.set(source.name, id);
    for (const port of cls.initial_ports) addPort(id, port);
  }
  for (const source of input.graph.nodes) {
    for (const link of source.links) {
      const right = findPort(link.sourceNode, link.sourcePort, "right");
      const left = findPort(link.targetNode, link.targetPort, "left");
      const a = snapshot.ports[right], b = snapshot.ports[left];
      const type = a.types.find((value) => b.types.includes(value));
      if (!type) throw new Error(`Incompatible ports: ${JSON.stringify(link)}`);
      const id = next++;
      snapshot.links[id] = { type, placeholder: false, left_port: left, left_port_node: b.node, right_port: right, right_port_node: a.node };
      a.links.push(id); b.links.push(id);
    }
  }
  return snapshot;

  function addPort(node, port) {
    const id = next++;
    snapshot.ports[id] = { ...port, node, links: [] };
    snapshot.nodes[node][`${port.direction}_ports`].push(id);
    return id;
  }
  function findPort(name, portName, direction) {
    const node = nodesByName.get(name);
    if (!node) throw new Error(`Unknown node: ${name}`);
    const ports = snapshot.nodes[node][`${direction}_ports`];
    const existing = ports.find((id) => snapshot.ports[id].name === portName);
    if (existing) return existing;
    // The example rules create dynamic slots on Sniffer and DnsFallback.
    const cls = snapshot.nodes[node].class;
    if (direction === "right" && /^slot [1-9]\d*$/u.test(portName)
      && ["almagest/Sniffer", "almagest/DnsFallback"].includes(cls)) {
      return addPort(node, { direction, name: portName, types: [snapshot.nodes[node].primary_type] });
    }
    throw new Error(`Unknown ${direction} port: ${name}/${portName}`);
  }
}

class FixtureTransport {
  connected = false;
  reconnect = false;
  constructor(input, snapshot) { this.input = input; this.snapshot = snapshot; }
  async connect(handlers) { this.handlers = handlers; this.connected = true; }
  close() { this.connected = false; }
  emit(notification) {
    this.handlers.message(JSON.stringify({ version: "v1", kind: "event", surface: "engine", method: "notification",
      payload: { subscription_id: 1, notification } }));
  }
  workspace(notification) { this.emit({ kind: "workspace", workspace_id: "main", workspace_notification: notification }); }
  send(data) {
    const request = JSON.parse(data);
    queueMicrotask(() => {
      try {
        const result = this.respond(`${request.surface}.${request.method}`, request.payload ?? {});
        this.handlers.message(JSON.stringify({ version: "v1", kind: "response", id: request.id, result }));
      } catch (error) {
        errors.push(String(error));
        this.handlers.message(JSON.stringify({ version: "v1", kind: "response", id: request.id,
          error: { code: "fixture", message: String(error) } }));
      }
    });
  }
  respond(method, payload) {
    switch (method) {
      case "admin.hello": return { version: "v1", daemon: "almagestd", execution_mode: "direct",
        project_version_status: { version: "pre-alpha", highlight: "none" } };
      case "engine.subscribe_notifications":
        this.emit({ kind: "colormap", colormap: this.input.cache.colormap });
        this.workspace({ kind: "workspace_snapshot", snapshot: this.snapshot });
        return { subscription_id: 1 };
      case "engine.workspace_snapshot": return { snapshot: this.snapshot };
      case "engine.subscribe_node_menu": {
        const name = this.snapshot.nodes[payload.node].name;
        const blocks = this.input.menus?.[name];
        if (!blocks) throw new Error(`Missing menu fixture: ${name}`);
        this.workspace({ kind: "node_menu", id: payload.node, formular: {
          type: "menu.snapshot", menuId: `NODE${payload.node}MENU`, menuGeneration: 1, blocks,
        } });
        return {};
      }
      case "engine.unsubscribe_node_menu": return {};
      case "engine.node_class_long_description": return { description: "" };
      case "log.subscribe": return { subscription_id: 2, cache_size: 100, entries: [] };
      case "secrets.subscribe": return { subscription_id: 3, snapshot: { secrets: [] } };
      case "snapshots.list": return { snapshots: this.input.snapshots };
      default: throw new Error(`Unimplemented fixture method: ${method}`);
    }
  }
}

async function waitFor(predicate) {
  const deadline = performance.now() + 12000;
  while (!predicate()) {
    if (errors.length) throw new Error(errors.join("\n"));
    if (performance.now() > deadline) throw new Error(`UI did not become ready: ${predicate}`);
    await new Promise((done) => setTimeout(done, 25));
  }
}
async function frames() {
  for (let i = 0; i < 3; i++) await new Promise(requestAnimationFrame);
}

await render();
