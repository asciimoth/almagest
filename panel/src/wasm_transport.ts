import type { AdminTransport, AdminTransportHandlers } from "./admin_transport.js";
import { parseAPIMessage } from "./api_model.js";
import { parseTypePaletteMap, type TypePalette } from "./pasta_model.js";

type WebDemoAPI = (request: string) => string;
type WebDemoEvents = () => string;
type ColorMapLoader = () => Promise<Record<string, TypePalette>>;

type GoRuntime = {
  importObject: WebAssembly.Imports;
  run(instance: WebAssembly.Instance): Promise<void>;
};

type WebDemoGlobals = typeof globalThis & {
  Go?: new () => GoRuntime;
  almagestWebDemoRequest?: WebDemoAPI;
  almagestWebDemoEvents?: WebDemoEvents;
};

let apiPromise: Promise<WebDemoAPI> | null = null;

export function loadWebDemoAPI(): Promise<WebDemoAPI> {
  apiPromise ??= startWebDemoAPI();
  return apiPromise;
}

// loadWebDemoColorMap reads the production color map that is published with
// the static documentation. The small Wasm engine does not contain this map.
export async function loadWebDemoColorMap(): Promise<Record<string, TypePalette>> {
  const configured = document.querySelector<HTMLMetaElement>("meta[name='almagest-site-base']")?.content;
  const siteBase = new URL(configured || "../../", window.location.href);
  const url = new URL("docs/content/node-classes.json", siteBase);
  const response = await fetch(url);
  if (!response.ok) throw new Error(`Node class cache download failed: ${response.status} ${response.statusText}`);
  const cache = await response.json() as unknown;
  if (!cache || typeof cache !== "object" || Array.isArray(cache)) throw new Error("Invalid node class cache");
  const value = cache as Record<string, unknown>;
  const colormap = value.version === 1 ? parseTypePaletteMap(value.colormap) : null;
  if (!colormap) throw new Error("Invalid node class cache");
  return colormap;
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
  readonly #loadColorMap: ColorMapLoader | null;
  #api: WebDemoAPI | null = null;
  #colorMap: Record<string, TypePalette> | null = null;
  #handlers: AdminTransportHandlers | null = null;
  #eventTimer: ReturnType<typeof setInterval> | null = null;

  constructor(loadAPI: () => Promise<WebDemoAPI> = loadWebDemoAPI, loadColorMap: ColorMapLoader | null = null) {
    this.#loadAPI = loadAPI;
    this.#loadColorMap = loadColorMap;
  }

  get connected(): boolean {
    return this.#api !== null;
  }

  async connect(handlers: AdminTransportHandlers): Promise<void> {
    this.#handlers = handlers;
    const [api, colorMap] = await Promise.all([
      this.#loadAPI(),
      this.#loadColorMap?.() ?? Promise.resolve(null),
    ]);
    this.#api = api;
    this.#colorMap = colorMap;
    this.#eventTimer = setInterval(() => this.#drainEvents(), 50);
    (this.#eventTimer as unknown as { unref?: () => void }).unref?.();
  }

  send(data: string): void {
    if (!this.#api || !this.#handlers) throw new Error("Admin API is not connected");
    const request = parseAPIMessage(data);
    if (this.#colorMap && request?.kind === "request" && request.surface === "admin" && request.method === "colormap") {
      this.#handlers.message(JSON.stringify({ version: "v1", kind: "response", id: request.id, result: this.#colorMap }));
      return;
    }
    const response = this.#api(data);
    this.#handlers.message(response);
    this.#drainEvents();
  }

  close(): void {
    if (!this.#api && !this.#handlers) return;
    const handlers = this.#handlers;
    this.#api = null;
    this.#colorMap = null;
    this.#handlers = null;
    if (this.#eventTimer !== null) clearInterval(this.#eventTimer);
    this.#eventTimer = null;
    handlers?.close();
  }

  #drainEvents(): void {
    if (!this.#handlers) return;
    const drain = (globalThis as WebDemoGlobals).almagestWebDemoEvents;
    if (!drain) return;
    try {
      const events = JSON.parse(drain()) as unknown;
      if (!Array.isArray(events)) return;
      for (const event of events) this.#handlers.message(JSON.stringify(event));
    } catch (error: unknown) {
      console.error("Almagest web demo event delivery failed", error);
    }
  }
}
