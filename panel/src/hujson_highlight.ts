/** Lexical classes that the HuJSON editor can display. */
export type HuJSONTokenKind =
  | "plain"
  | "comment"
  | "key"
  | "keyword"
  | "number"
  | "punctuation"
  | "string";

/** A contiguous part of the source and its display class. */
export type HuJSONToken = Readonly<{
  kind: HuJSONTokenKind;
  text: string;
}>;

const KEYWORDS = ["false", "null", "true"] as const;

/**
 * Split HuJSON source into display tokens.
 *
 * This is intentionally a lexer, not a validator. The daemon remains the
 * authority for config validation. A lexer can also color an incomplete
 * document while a user is typing. HuJSON adds line comments, block comments,
 * and trailing commas to JSON; trailing commas need no special lexical rule.
 */
export function tokenizeHuJSON(source: string): HuJSONToken[] {
  const tokens: HuJSONToken[] = [];
  let index = 0;

  while (index < source.length) {
    const start = index;
    const character = source[index] ?? "";

    if (isWhitespace(character)) {
      do index += 1;
      while (index < source.length && isWhitespace(source[index] ?? ""));
      appendToken(tokens, "plain", source.slice(start, index));
      continue;
    }

    if (character === "\"") {
      index = scanString(source, index);
      const kind = hasColonAfterTrivia(source, index) ? "key" : "string";
      appendToken(tokens, kind, source.slice(start, index));
      continue;
    }

    if (character === "/" && source[index + 1] === "/") {
      index = scanLineComment(source, index);
      appendToken(tokens, "comment", source.slice(start, index));
      continue;
    }

    if (character === "/" && source[index + 1] === "*") {
      index = scanBlockComment(source, index);
      appendToken(tokens, "comment", source.slice(start, index));
      continue;
    }

    if (isNumberStart(source, index)) {
      index = scanNumber(source, index);
      appendToken(tokens, "number", source.slice(start, index));
      continue;
    }

    const keyword = keywordAt(source, index);
    if (keyword !== null) {
      index += keyword.length;
      appendToken(tokens, "keyword", keyword);
      continue;
    }

    if (isPunctuation(character)) {
      index += 1;
      appendToken(tokens, "punctuation", character);
      continue;
    }

    // Merge unrecognized characters into plain text. This preserves invalid
    // drafts exactly and avoids assigning meaning to syntax HuJSON rejects.
    index += 1;
    appendToken(tokens, "plain", character);
  }

  return tokens;
}

/**
 * Render highlighted source without passing config data to an HTML parser.
 *
 * Token text is assigned only through Text nodes and `textContent`. Class
 * names come from the closed HuJSONTokenKind union, never from config text.
 */
export function renderHuJSONHighlight(container: HTMLElement, source: string): void {
  const document = container.ownerDocument;
  const fragment = document.createDocumentFragment();

  for (const token of tokenizeHuJSON(source)) {
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

function appendToken(tokens: HuJSONToken[], kind: HuJSONTokenKind, text: string): void {
  if (!text) return;
  const previous = tokens[tokens.length - 1];
  if (previous?.kind === kind) {
    tokens[tokens.length - 1] = { kind, text: previous.text + text };
    return;
  }
  tokens.push({ kind, text });
}

function scanString(source: string, start: number): number {
  let index = start + 1;
  while (index < source.length) {
    const character = source[index];
    if (character === "\\") {
      // Skip the escaped UTF-16 code unit. Validation of escape sequences is
      // deliberately left to the daemon parser.
      index = Math.min(index + 2, source.length);
      continue;
    }
    index += 1;
    if (character === "\"") break;
  }
  return index;
}

function scanLineComment(source: string, start: number): number {
  let index = start + 2;
  while (index < source.length && source[index] !== "\n" && source[index] !== "\r") index += 1;
  return index;
}

function scanBlockComment(source: string, start: number): number {
  const close = source.indexOf("*/", start + 2);
  return close === -1 ? source.length : close + 2;
}

function hasColonAfterTrivia(source: string, start: number): boolean {
  let index = start;
  while (index < source.length) {
    const character = source[index] ?? "";
    if (isWhitespace(character)) {
      index += 1;
      continue;
    }
    if (character === "/" && source[index + 1] === "/") {
      index = scanLineComment(source, index);
      continue;
    }
    if (character === "/" && source[index + 1] === "*") {
      index = scanBlockComment(source, index);
      continue;
    }
    return character === ":";
  }
  return false;
}

function isNumberStart(source: string, index: number): boolean {
  const character = source[index] ?? "";
  const next = source[index + 1] ?? "";
  const previous = source[index - 1] ?? "";
  const startsWithDigit = isDigit(character);
  const startsWithMinus = character === "-" && isDigit(next);
  return (startsWithDigit || startsWithMinus) && !isWordCharacter(previous) && previous !== ".";
}

function scanNumber(source: string, start: number): number {
  let index = source[start] === "-" ? start + 1 : start;

  if (source[index] === "0") {
    index += 1;
  } else {
    while (isDigit(source[index] ?? "")) index += 1;
  }

  if (source[index] === "." && isDigit(source[index + 1] ?? "")) {
    index += 1;
    while (isDigit(source[index] ?? "")) index += 1;
  }

  if ((source[index] === "e" || source[index] === "E") && exponentHasDigits(source, index)) {
    index += 1;
    if (source[index] === "+" || source[index] === "-") index += 1;
    while (isDigit(source[index] ?? "")) index += 1;
  }

  return index;
}

function exponentHasDigits(source: string, exponentIndex: number): boolean {
  const afterMarker = source[exponentIndex + 1] ?? "";
  if (afterMarker === "+" || afterMarker === "-") return isDigit(source[exponentIndex + 2] ?? "");
  return isDigit(afterMarker);
}

function keywordAt(source: string, index: number): typeof KEYWORDS[number] | null {
  if (isWordCharacter(source[index - 1] ?? "")) return null;
  for (const keyword of KEYWORDS) {
    if (source.startsWith(keyword, index) && !isWordCharacter(source[index + keyword.length] ?? "")) return keyword;
  }
  return null;
}

function isWhitespace(character: string): boolean {
  return character === " " || character === "\t" || character === "\n" || character === "\r";
}

function isDigit(character: string): boolean {
  return character >= "0" && character <= "9";
}

function isWordCharacter(character: string): boolean {
  return (character >= "a" && character <= "z")
    || (character >= "A" && character <= "Z")
    || isDigit(character)
    || character === "_"
    || character === "$";
}

function isPunctuation(character: string): boolean {
  return character === "{" || character === "}" || character === "[" || character === "]"
    || character === ":" || character === ",";
}

function tokenClass(kind: Exclude<HuJSONTokenKind, "plain">): string {
  switch (kind) {
    case "comment": return "hujson-comment";
    case "key": return "hujson-key";
    case "keyword": return "hujson-keyword";
    case "number": return "hujson-number";
    case "punctuation": return "hujson-punctuation";
    case "string": return "hujson-string";
  }
}
