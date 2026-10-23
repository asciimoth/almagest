import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";

const root = resolve(process.argv[2]);
await import(pathToFileURL(resolve(root, "wasm_exec.js")).href);
const go = new globalThis.Go();
const bytes = await readFile(resolve(root, "almagest.wasm"));
const { instance } = await WebAssembly.instantiate(bytes, go.importObject);
void go.run(instance);

if (typeof globalThis.almagestWebDemoRequest !== "function") {
  throw new Error("Wasm API function is not available");
}
const request = JSON.stringify({
  version: "v1",
  kind: "request",
  id: "smoke",
  surface: "engine",
  method: "workspace_snapshot",
  payload: { workspace: "main" },
});
process.stdout.write(globalThis.almagestWebDemoRequest(request));
process.exit(0);
