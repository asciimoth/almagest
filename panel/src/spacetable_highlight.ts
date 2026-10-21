/** Lexical classes for whitespace-separated tables. */
export type SpaceTableTokenKind = "plain" | "comment" | "key" | "value" | "number" | "string";

/** A contiguous part of the source and its display class. */
export type SpaceTableToken = Readonly<{
  kind: SpaceTableTokenKind;
  text: string;
}>;

/**
 * Split a whitespace-separated table into display tokens.
 *
 * Spaces and tabs separate cells. Double quotes group cells that contain
 * separators, and a backslash escapes the next character inside a quoted
 * cell. A line whose first non-whitespace character is `#` is a comment.
 * The tokenizer preserves incomplete input so it can also highlight drafts.
 */
export function tokenizeSpaceTable(source: string): SpaceTableToken[] {
  const tokens: SpaceTableToken[] = [];
  let lineStart = 0;

  while (lineStart < source.length) {
    let lineEnd = source.indexOf("\n", lineStart);
    if (lineEnd === -1) lineEnd = source.length;
    tokenizeLine(tokens, source.slice(lineStart, lineEnd));
    if (lineEnd < source.length) appendToken(tokens, "plain", "\n");
    lineStart = lineEnd + 1;
  }

  return tokens;
}

/** Render a highlighted table without passing its source to an HTML parser. */
export function renderSpaceTableHighlight(container: HTMLElement, source: string): void {
  const document = container.ownerDocument;
  const fragment = document.createDocumentFragment();

  for (const token of tokenizeSpaceTable(source)) {
    if (token.kind === "plain") {
      fragment.append(document.createTextNode(token.text));
      continue;
    }
    const span = document.createElement("span");
    span.className = tokenClass(token.kind);
    span.textContent = token.text;
    fragment.append(span);
  }

  container.replaceChildren(fragment);
}

function tokenizeLine(tokens: SpaceTableToken[], line: string): void {
  let index = scanWhitespace(line, 0);
  appendToken(tokens, "plain", line.slice(0, index));
  if (index === line.length) return;

  if (line[index] === "#") {
    appendToken(tokens, "comment", line.slice(index));
    return;
  }

  let column = 0;
  while (index < line.length) {
    const cellEnd = scanCell(line, index);
    const cell = line.slice(index, cellEnd);
    appendToken(tokens, cellKind(cell, column), cell);
    column += 1;
    index = cellEnd;

    const whitespaceEnd = scanWhitespace(line, index);
    appendToken(tokens, "plain", line.slice(index, whitespaceEnd));
    index = whitespaceEnd;
  }
}

function scanWhitespace(source: string, start: number): number {
  let index = start;
  while (source[index] === " " || source[index] === "\t" || source[index] === "\r") index += 1;
  return index;
}

function scanCell(source: string, start: number): number {
  let index = start;
  let quote = false;
  let escape = false;

  while (index < source.length) {
    const character = source[index];
    if (escape) {
      escape = false;
    } else if (quote && character === "\\") {
      escape = true;
    } else if (character === "\"") {
      quote = !quote;
    } else if (!quote && (character === " " || character === "\t" || character === "\r")) {
      break;
    }
    index += 1;
  }

  return index;
}

function cellKind(cell: string, column: number): Exclude<SpaceTableTokenKind, "plain" | "comment"> {
  if (cell.startsWith("\"")) return "string";
  if (/^[+-]?(?:\d+(?:\.\d*)?|\.\d+)$/.test(cell)) return "number";
  return column === 0 ? "key" : "value";
}

function appendToken(tokens: SpaceTableToken[], kind: SpaceTableTokenKind, text: string): void {
  if (!text) return;
  const previous = tokens[tokens.length - 1];
  if (previous?.kind === kind) {
    tokens[tokens.length - 1] = { kind, text: previous.text + text };
    return;
  }
  tokens.push({ kind, text });
}

function tokenClass(kind: Exclude<SpaceTableTokenKind, "plain">): string {
  switch (kind) {
    case "comment": return "spacetable-comment";
    case "key": return "spacetable-key";
    case "number": return "spacetable-number";
    case "string": return "spacetable-string";
    case "value": return "spacetable-value";
  }
}
