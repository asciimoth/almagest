import { text as localizedText } from "./localization.js";

export const API_SURFACES = [
  "admin",
  "config",
  "dns",
  "docs",
  "engine",
  "flags",
  "kv",
  "log",
  "secrets",
  "snapshots",
  "system",
  "tls",
] as const;

export type Surface = typeof API_SURFACES[number];

export type APIError = {
  code: string;
  message: string;
  details?: unknown;
};

export type APIRequestMessage = {
  version: "v1";
  kind: "request";
  id: string;
  surface: Surface;
  method: string;
  payload?: unknown;
};

export type APIResponseMessage = {
  version: "v1";
  kind: "response";
  id: string;
  result?: unknown;
  error?: APIError;
};

export type APIEventMessage = {
  version: "v1";
  kind: "event";
  surface: Surface;
  method: string;
  payload?: unknown;
};

export type APIMessage = APIRequestMessage | APIResponseMessage | APIEventMessage;

const surfaces: ReadonlySet<string> = new Set(API_SURFACES);

export function parseAPIMessage(text: string): APIMessage | null {
  let value: unknown;
  try {
    value = JSON.parse(text) as unknown;
  } catch {
    return null;
  }
  if (!isRecord(value) || value.version !== "v1") return null;
  switch (value.kind) {
    case "request":
      return parseRequest(value);
    case "response":
      return parseResponse(value);
    case "event":
      return parseEvent(value);
    default:
      return null;
  }
}

export function errorMessage(value: unknown): string {
  if (value instanceof Error) return value.message;
  if (typeof value === "string") return value;
  return localizedText("common.unknown_error", "Unknown error");
}

function parseRequest(value: Record<string, unknown>): APIRequestMessage | null {
  if (!isNonEmptyString(value.id) || !isSurface(value.surface) || !isNonEmptyString(value.method)) return null;
  return value.payload === undefined
    ? { version: "v1", kind: "request", id: value.id, surface: value.surface, method: value.method }
    : { version: "v1", kind: "request", id: value.id, surface: value.surface, method: value.method, payload: value.payload };
}

function parseResponse(value: Record<string, unknown>): APIResponseMessage | null {
  if (!isNonEmptyString(value.id)) return null;
  const hasResult = Object.hasOwn(value, "result");
  const error = value.error;
  if (!hasResult && error === undefined) return null;
  if (hasResult && error !== undefined) return null;
  if (error !== undefined && !isAPIError(error)) return null;

  const response: APIResponseMessage = { version: "v1", kind: "response", id: value.id };
  if (hasResult) response.result = value.result;
  if (error !== undefined) response.error = error;
  return response;
}

function parseEvent(value: Record<string, unknown>): APIEventMessage | null {
  if (!isSurface(value.surface) || !isNonEmptyString(value.method)) return null;
  return value.payload === undefined
    ? { version: "v1", kind: "event", surface: value.surface, method: value.method }
    : { version: "v1", kind: "event", surface: value.surface, method: value.method, payload: value.payload };
}

function isSurface(value: unknown): value is Surface {
  return typeof value === "string" && surfaces.has(value);
}

function isAPIError(value: unknown): value is APIError {
  return isRecord(value) && typeof value.code === "string" && typeof value.message === "string";
}

function isNonEmptyString(value: unknown): value is string {
  return typeof value === "string" && value.length > 0;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
