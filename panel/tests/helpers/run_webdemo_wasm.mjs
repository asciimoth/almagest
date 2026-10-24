import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";

const root = resolve(process.argv[2]);
const localValues = new Map();
globalThis.localStorage = {
  getItem: (key) => localValues.get(key) ?? null,
  setItem: (key, value) => localValues.set(key, String(value)),
};
await import(pathToFileURL(resolve(root, "wasm_exec.js")).href);
const go = new globalThis.Go();
const bytes = await readFile(resolve(root, "almagest.wasm"));
const { instance } = await WebAssembly.instantiate(bytes, go.importObject);
void go.run(instance);

if (typeof globalThis.almagestWebDemoRequest !== "function") {
  throw new Error("Wasm API function is not available");
}
function request(id, surface, method, payload = {}) {
  return JSON.parse(globalThis.almagestWebDemoRequest(JSON.stringify({
    version: "v1",
    kind: "request",
    id,
    surface,
    method,
    payload,
  })));
}

process.stdout.write(JSON.stringify({
  logs: request("logs", "log", "subscribe"),
  snapshots: request("snapshots", "snapshots", "list"),
  tls: request("tls", "tls", "snapshot"),
  workspace: request("smoke", "engine", "workspace_snapshot", { workspace: "main" }),
}));
process.exit(0);
