import type { AdminTransport } from "./admin_transport.js";
import { DesktopAdminTransport } from "./desktop_admin_transport.js";
import type { DocsClient } from "./docs_client.js";

type PendingRequest = {
  resolve(value: unknown): void;
  reject(error: Error): void;
};

type APIResponse = {
  version: "v1";
  kind: "response";
  id: string;
  result?: unknown;
  error?: { code: string; message: string };
};

/**
 * Load documentation through the existing native admin connection.
 *
 * A document navigation destroys this JavaScript object, but it does not close
 * the Rust-owned daemon connection. The replacement page registers a new
 * listener and immediately receives the connection's current state.
 */
export class DesktopDocsClient implements DocsClient {
  readonly #transport: AdminTransport;
  readonly #pending = new Map<string, PendingRequest>();
  #connecting: Promise<void> | null = null;
  #nextRequestID = 1;
  #locale = "";

  constructor(transport: AdminTransport = new DesktopAdminTransport()) {
    this.#transport = transport;
  }

  /** Release the page-specific IPC listener before a cached page is hidden. */
  suspend(): void {
    this.#transport.close();
  }

  async tree(locale: string): Promise<unknown> {
    await this.#selectLocale(locale);
    return await this.#request("docs", "tree", {});
  }

  async raw(locale: string, id: string): Promise<string> {
    await this.#selectLocale(locale);
    const value = await this.#request("docs", "page", { id });
    if (!isRecord(value) || typeof value.content !== "string") {
      throw new Error("docs page returned an invalid result");
    }
    return value.content;
  }

  async nodeClass(locale: string, id: string): Promise<unknown> {
    await this.#selectLocale(locale);
    return await this.#request("docs", "node_class", { id });
  }

  pageFromLocation(location: Location): string {
    return new URLSearchParams(location.search).get("page") || "";
  }

  pageURL(page: string, locale = "", hash = ""): string {
    const query = new URLSearchParams();
    if (page) query.set("page", page);
    if (locale) query.set("locale", locale);
    return `./index.html${query.size > 0 ? `?${query}` : ""}${hash}`;
  }

  async #selectLocale(locale: string): Promise<void> {
    await this.#connect();
    if (!locale || locale === this.#locale) return;
    await this.#request("admin", "set_locale", { locale });
    this.#locale = locale;
  }

  #connect(): Promise<void> {
    if (this.#transport.connected) return Promise.resolve();
    this.#connecting ??= this.#transport.connect({
      message: (data) => { this.#receive(data); },
      close: () => {
        this.#connecting = null;
        this.#locale = "";
        this.#rejectPending(new Error("Admin API connection closed"));
      },
      diagnostic: (level, message) => {
        const report = level === "warn" ? console.warn : console.debug;
        report(`[almagest-docs] ${message}`);
      },
    }).finally(() => {
      this.#connecting = null;
    });
    return this.#connecting;
  }

  async #request(surface: string, method: string, payload: Record<string, unknown>): Promise<unknown> {
    await this.#connect();
    const id = `docs-${this.#nextRequestID++}`;
    const message = { version: "v1", kind: "request", id, surface, method, payload };
    return await new Promise<unknown>((resolve, reject) => {
      this.#pending.set(id, { resolve, reject });
      try {
        this.#transport.send(JSON.stringify(message));
      } catch (error) {
        this.#pending.delete(id);
        reject(asError(error));
      }
    });
  }

  #receive(data: string): void {
    let value: unknown;
    try {
      value = JSON.parse(data) as unknown;
    } catch {
      console.warn("[almagest-docs] ignored invalid admin API JSON");
      return;
    }
    const response = parseResponse(value);
    if (!response) return;
    const pending = this.#pending.get(response.id);
    if (!pending) return;
    this.#pending.delete(response.id);
    if (response.error) {
      pending.reject(new Error(`${response.error.code}: ${response.error.message}`));
    } else {
      pending.resolve(response.result);
    }
  }

  #rejectPending(error: Error): void {
    for (const pending of this.#pending.values()) pending.reject(error);
    this.#pending.clear();
  }
}

function parseResponse(value: unknown): APIResponse | null {
  if (!isRecord(value) || value.version !== "v1" || value.kind !== "response" || typeof value.id !== "string") return null;
  if (value.error !== undefined) {
    if (!isRecord(value.error) || typeof value.error.code !== "string" || typeof value.error.message !== "string") return null;
    return { version: "v1", kind: "response", id: value.id, error: { code: value.error.code, message: value.error.message } };
  }
  return { version: "v1", kind: "response", id: value.id, result: value.result };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === "object" && !Array.isArray(value);
}

function asError(value: unknown): Error {
  return value instanceof Error ? value : new Error(String(value));
}
