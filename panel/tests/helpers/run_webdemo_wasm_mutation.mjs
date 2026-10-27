import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";

const root = resolve(process.argv[2]);
const localValues = new Map();
globalThis.console = { ...globalThis.console, debug() {}, info() {}, warn() {}, error() {} };
if (process.argv[3] !== "no-storage") {
  globalThis.localStorage = {
    getItem: (key) => localValues.get(key) ?? null,
    setItem: (key, value) => localValues.set(key, String(value)),
  };
}
await import(pathToFileURL(resolve(root, "wasm_exec.js")).href);
const go = new globalThis.Go();
const bytes = await readFile(resolve(root, "almagest.wasm"));
const { instance } = await WebAssembly.instantiate(bytes, go.importObject);
void go.run(instance);

function request(id, surface, method, payload) {
  return JSON.parse(globalThis.almagestWebDemoRequest(JSON.stringify({
    version: "v1", kind: "request", id, surface, method, payload,
  })));
}

const subscribed = request("subscribe", "engine", "subscribe_notifications", { workspace: "main" });
const initial = request("initial", "engine", "workspace_snapshot", { workspace: "main" });
const classes = initial.result.snapshot.classes.map((entry) => entry.class);
const added = classes.map((className, index) => (
  request(`add-${index}`, "engine", "add_node", { workspace: "main", class: className })
));
const snapshot = request("snapshot", "engine", "workspace_snapshot", { workspace: "main" });
const config = request("config", "config", "snapshot", {});
const events = JSON.parse(globalThis.almagestWebDemoEvents());
process.stdout.write(JSON.stringify({
  subscribed,
  initialNodeCount: Object.keys(initial.result.snapshot.nodes).length,
  classes,
  added,
  snapshot,
  events,
  config: config.result.text,
  persistedConfig: localValues.get("almagest.webdemo.config.v1") ?? null,
}));
process.exit(0);
