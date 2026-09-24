export type SecretSource = "mutable" | "protected" | "env" | (string & {});

export type SecretEntry = {
  key: string;
  value: string;
  nodes?: string[];
  read_only?: boolean;
  source?: SecretSource;
};

export type SecretsSnapshot = {
  secrets?: SecretEntry[];
};

export type SecretChange = {
  key: string;
  deleted?: boolean;
  entry?: SecretEntry;
};

export type SecretNodeRef = {
  key: string;
};

export type SecretNodeTextToken = {
  start: number;
  end: number;
  prefix: string;
  token: string;
};

export type SecretNodeTextSuggestionResult = {
  token: SecretNodeTextToken;
  suggestions: string[];
};

export const SECRET_TEXT_RENDER_LIMIT = 32 * 1024;
export const FORMULAR_SECRET_SELECTOR = "secrets";

// Secret keys are arbitrary user/admin-controlled strings. Use a reversible
// tuple key for panel state instead of delimiter parsing so odd names cannot
// collide with future tree identity fields.
export function secretNodeKey(secret: SecretEntry | string): string {
  return JSON.stringify([typeof secret === "string" ? secret : secret.key]);
}

export function parseSecretNodeKey(key: string): SecretNodeRef | null {
  try {
    const parsed = JSON.parse(key) as unknown;
    if (!Array.isArray(parsed) || parsed.length !== 1 || typeof parsed[0] !== "string") return null;
    return { key: parsed[0] };
  } catch {
    return null;
  }
}

export function applySecretChange(secrets: SecretEntry[], change: SecretChange): SecretEntry[] {
  const next = secrets.filter((secret) => secret.key !== change.key);
  if (!change.deleted && change.entry) next.push(normalizeSecretEntry(change.entry));
  return sortSecrets(next);
}

export function sortSecrets(secrets: SecretEntry[]): SecretEntry[] {
  return [...secrets].map(normalizeSecretEntry).sort((left, right) => left.key.localeCompare(right.key));
}

export function secretNamesForNode(secrets: SecretEntry[], nodeName: string): string[] {
  if (!nodeName) return [];
  return sortSecrets(secrets)
    .filter((secret) => secret.nodes?.includes(nodeName))
    .map((secret) => secret.key);
}

export function normalizeSecretEntry(entry: SecretEntry): SecretEntry {
  return {
    ...entry,
    value: entry.value ?? "",
    nodes: normalizeSecretNodes(entry.nodes ?? []),
  };
}

// The API accepts node names as an array. The panel form stores one node name
// per line so spaces, commas, tabs, and punctuation remain part of the name.
export function parseSecretNodeText(text: string): string[] {
  return normalizeSecretNodes(text.split(/\r\n?|\n/g));
}

export function secretNodeTextTokenAt(text: string, cursor: number): SecretNodeTextToken {
  const position = Number.isFinite(cursor)
    ? Math.max(0, Math.min(text.length, Math.trunc(cursor)))
    : text.length;
  let start = position;
  while (start > 0 && !isSecretNodeTextDelimiter(text[start - 1] ?? "")) start -= 1;
  let end = position;
  while (end < text.length && !isSecretNodeTextDelimiter(text[end] ?? "")) end += 1;
  return {
    start,
    end,
    prefix: text.slice(start, position),
    token: text.slice(start, end),
  };
}

export function secretNodeTextSuggestions(
  text: string,
  cursor: number,
  nodeNames: string[],
  limit = 8,
): SecretNodeTextSuggestionResult {
  const token = secretNodeTextTokenAt(text, cursor);
  const prefix = token.prefix.toLocaleLowerCase();
  if (!prefix) return { token, suggestions: [] };
  const selected = new Set(parseSecretNodeText(text));
  const suggestions = normalizeSecretNodes(nodeNames)
    .filter((node) => node !== token.token)
    .filter((node) => !selected.has(node))
    .filter((node) => node.toLocaleLowerCase().startsWith(prefix))
    .slice(0, Math.max(0, limit));
  return { token, suggestions };
}

export function replaceSecretNodeTextToken(
  text: string,
  token: SecretNodeTextToken,
  nodeName: string,
): { text: string; cursor: number } {
  const next = `${text.slice(0, token.start)}${nodeName}${text.slice(token.end)}`;
  return { text: next, cursor: token.start + nodeName.length };
}

export function normalizeSecretNodes(nodes: string[]): string[] {
  return [...new Set(nodes.map((node) => node.trim()).filter(Boolean))].sort((left, right) => left.localeCompare(right));
}

export function secretNodesText(nodes: string[] | undefined): string {
  return normalizeSecretNodes(nodes ?? []).join("\n");
}

export function formatSecretSource(source: SecretSource | undefined): string {
  switch (source) {
    case "env":
      return "env";
    case "protected":
      return "protected";
    case "mutable":
    case "":
    case undefined:
      return "mutable";
    default:
      return source;
  }
}

function isSecretNodeTextDelimiter(char: string): boolean {
  return char === "\r" || char === "\n";
}
