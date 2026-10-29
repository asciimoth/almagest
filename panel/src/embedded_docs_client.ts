import type { DocsClient } from "./docs_client.js";
import { EMBEDDED_DOCS_SOURCE } from "./embedded_docs_protocol.js";

type PendingRequest = {
  resolve(value: unknown): void;
  reject(error: Error): void;
};

type ParentResponse = {
  source: "almagest-desktop";
  kind: "docs-response";
  id: string;
  result?: unknown;
  error?: string;
};

/** Use the parent panel's existing admin session from the persistent docs frame. */
export class EmbeddedDocsClient implements DocsClient {
  readonly previewsUnavailableNodes = false;
  readonly #pending = new Map<string, PendingRequest>();
  #nextRequestID = 1;
  #locale = "";

  constructor() {
    window.addEventListener("message", (event) => { this.#receive(event); });
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
    const query = new URLSearchParams({ embedded: "1" });
    if (page) query.set("page", page);
    if (locale) query.set("locale", locale);
    return `./index.html?${query}${hash}`;
  }

  async #selectLocale(locale: string): Promise<void> {
    if (!locale || locale === this.#locale) return;
    await this.#request("admin", "set_locale", { locale });
    this.#locale = locale;
  }

  #request(surface: "admin" | "docs", method: string, payload: Record<string, unknown>): Promise<unknown> {
    const id = `embedded-docs-${this.#nextRequestID++}`;
    return new Promise<unknown>((resolve, reject) => {
      this.#pending.set(id, { resolve, reject });
      window.parent.postMessage({
        source: EMBEDDED_DOCS_SOURCE,
        kind: "docs-request",
        id,
        surface,
        method,
        payload,
      }, window.location.origin);
    });
  }

  #receive(event: MessageEvent<unknown>): void {
    if (event.origin !== window.location.origin || event.source !== window.parent) return;
    const response = parseResponse(event.data);
    if (!response) return;
    const pending = this.#pending.get(response.id);
    if (!pending) return;
    this.#pending.delete(response.id);
    if (response.error !== undefined) pending.reject(new Error(response.error));
    else pending.resolve(response.result);
  }
}

function parseResponse(value: unknown): ParentResponse | null {
  if (!isRecord(value)
    || value.source !== "almagest-desktop"
    || value.kind !== "docs-response"
    || typeof value.id !== "string") return null;
  if (value.error !== undefined && typeof value.error !== "string") return null;
  return {
    source: value.source,
    kind: value.kind,
    id: value.id,
    result: value.result,
    ...(typeof value.error === "string" ? { error: value.error } : {}),
  };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
