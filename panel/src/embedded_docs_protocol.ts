export const EMBEDDED_DOCS_SOURCE = "almagest-desktop" as const;

export type EmbeddedDocsRequest = {
  kind: "docs-request";
  id: string;
  surface: "admin" | "docs";
  method: string;
  payload: Record<string, unknown>;
};

export type EmbeddedDocsClose = {
  kind: "docs-close";
  tab: string;
};

export type EmbeddedDocsReady = {
  kind: "docs-ready";
};

export type EmbeddedDocsMessage = EmbeddedDocsRequest | EmbeddedDocsClose | EmbeddedDocsReady;

const DOCS_METHODS = new Set(["tree", "page", "node_class"]);

/** Validate every value received by the privileged parent panel. */
export function parseEmbeddedDocsMessage(value: unknown): EmbeddedDocsMessage | null {
  if (!isRecord(value) || value.source !== EMBEDDED_DOCS_SOURCE || typeof value.kind !== "string") return null;
  if (value.kind === "docs-ready") {
    return Object.keys(value).length === 2 ? { kind: value.kind } : null;
  }
  if (value.kind === "docs-close") {
    return typeof value.tab === "string" && (!value.tab || validRoutePart(value.tab))
      ? { kind: value.kind, tab: value.tab }
      : null;
  }
  if (value.kind !== "docs-request"
    || !validRequestID(value.id)
    || !isRecord(value.payload)
    || typeof value.method !== "string"
    || (value.surface !== "admin" && value.surface !== "docs")) return null;
  if (value.surface === "admin") {
    if (value.method !== "set_locale" || !validLocalePayload(value.payload)) return null;
  } else if (!DOCS_METHODS.has(value.method) || !validDocsPayload(value.method, value.payload)) {
    return null;
  }
  return {
    kind: value.kind,
    id: value.id,
    surface: value.surface,
    method: value.method,
    payload: value.payload,
  };
}

function validRequestID(value: unknown): value is string {
  return typeof value === "string" && value.length > 0 && value.length <= 128;
}

function validLocalePayload(payload: Record<string, unknown>): boolean {
  return Object.keys(payload).length === 1
    && typeof payload.locale === "string"
    && payload.locale.length > 0
    && payload.locale.length <= 128
    && !controlCharacters(payload.locale);
}

function validDocsPayload(method: string, payload: Record<string, unknown>): boolean {
  const keys = Object.keys(payload);
  if (method === "tree") return keys.length === 0;
  return keys.length === 1 && keys[0] === "id"
    && typeof payload.id === "string" && validRoutePart(payload.id);
}

function validRoutePart(value: string): boolean {
  return value.length > 0 && value.length <= 1024 && !controlCharacters(value);
}

function controlCharacters(value: string): boolean {
  return /[\u0000-\u001f\u007f]/.test(value);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
