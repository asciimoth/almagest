import type { AdminTransport, AdminTransportHandlers } from "./admin_transport.js";

type WebDemoAPI = (request: string) => string;

type GoRuntime = {
  importObject: WebAssembly.Imports;
  run(instance: WebAssembly.Instance): Promise<void>;
};

type WebDemoGlobals = typeof globalThis & {
  Go?: new () => GoRuntime;
  almagestWebDemoRequest?: WebDemoAPI;
};

let apiPromise: Promise<WebDemoAPI> | null = null;

export function loadWebDemoAPI(): Promise<WebDemoAPI> {
  apiPromise ??= startWebDemoAPI();
  return apiPromise;
}

async function startWebDemoAPI(): Promise<WebDemoAPI> {
  const globals = globalThis as WebDemoGlobals;
  const runtimeURL = new URL("../../wasm_exec.js", import.meta.url);
  await import(runtimeURL.href);
  if (!globals.Go) throw new Error("Go Wasm runtime did not load");

  const go = new globals.Go();
  const wasmURL = new URL("../../almagest.wasm", import.meta.url);
  const instance = await instantiateWASM(wasmURL, go.importObject);
  void go.run(instance).catch((error: unknown) => {
    console.error("Almagest web demo Wasm stopped", error);
  });

  const api = globals.almagestWebDemoRequest;
  if (!api) throw new Error("Almagest web demo API did not initialize");
  return api;
}

async function instantiateWASM(url: URL, imports: WebAssembly.Imports): Promise<WebAssembly.Instance> {
  const response = await fetch(url);
  if (!response.ok) throw new Error(`Wasm download failed: ${response.status} ${response.statusText}`);
  if (WebAssembly.instantiateStreaming) {
    try {
      const result = await WebAssembly.instantiateStreaming(response.clone(), imports);
      return result.instance;
    } catch {
      // Static servers do not always use application/wasm. Retry from bytes.
    }
  }
  const result = await WebAssembly.instantiate(await response.arrayBuffer(), imports);
  return result.instance;
}

export class WASMAdminTransport implements AdminTransport {
  readonly reconnect = false;
  readonly #loadAPI: () => Promise<WebDemoAPI>;
  #api: WebDemoAPI | null = null;
  #handlers: AdminTransportHandlers | null = null;

  constructor(loadAPI: () => Promise<WebDemoAPI> = loadWebDemoAPI) {
    this.#loadAPI = loadAPI;
  }

  get connected(): boolean {
    return this.#api !== null;
  }

  async connect(handlers: AdminTransportHandlers): Promise<void> {
    this.#handlers = handlers;
    this.#api = await this.#loadAPI();
  }

  send(data: string): void {
    if (!this.#api || !this.#handlers) throw new Error("Admin API is not connected");
    const response = this.#api(data);
    this.#handlers.message(response);
  }

  close(): void {
    if (!this.#api && !this.#handlers) return;
    const handlers = this.#handlers;
    this.#api = null;
    this.#handlers = null;
    handlers?.close();
  }
}
