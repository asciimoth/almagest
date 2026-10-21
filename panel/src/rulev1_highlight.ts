/** Lexical classes shared by the line-oriented routing rule syntaxes. */
export type RuleV1TokenKind = "plain" | "comment" | "instruction" | "argument" | "number";

/** A contiguous part of the source and its display class. */
export type RuleV1Token = Readonly<{
  kind: RuleV1TokenKind;
  text: string;
}>;

/**
 * Split routing or filtering rules into display tokens.
 *
 * Rule variants differ in their accepted instructions and argument formats,
 * but share one lexical form: one instruction and its arguments per line,
 * with whole-line comments that start with `#` after optional whitespace.
 * This lexer deliberately does not validate variant-specific details.
 */
export function tokenizeRuleV1(source: string): RuleV1Token[] {
  const tokens: RuleV1Token[] = [];
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

/** Render highlighted rules without passing rule text to an HTML parser. */
export function renderRuleV1Highlight(container: HTMLElement, source: string): void {
  const document = container.ownerDocument;
  const fragment = document.createDocumentFragment();

  for (const token of tokenizeRuleV1(source)) {
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

function tokenizeLine(tokens: RuleV1Token[], line: string): void {
  let index = scanWhitespace(line, 0);
  appendToken(tokens, "plain", line.slice(0, index));
  if (index === line.length) return;

  if (line[index] === "#") {
    appendToken(tokens, "comment", line.slice(index));
    return;
  }

  const instructionEnd = scanNonWhitespace(line, index);
  appendToken(tokens, "instruction", line.slice(index, instructionEnd));
  index = instructionEnd;

  while (index < line.length) {
    const whitespaceEnd = scanWhitespace(line, index);
    appendToken(tokens, "plain", line.slice(index, whitespaceEnd));
    index = whitespaceEnd;
    if (index === line.length) break;

    const argumentEnd = scanNonWhitespace(line, index);
    const argument = line.slice(index, argumentEnd);
    appendToken(tokens, isDecimalInteger(argument) ? "number" : "argument", argument);
    index = argumentEnd;
  }
}

function scanWhitespace(source: string, start: number): number {
  let index = start;
  while (source[index] === " " || source[index] === "\t" || source[index] === "\r") index += 1;
  return index;
}

function scanNonWhitespace(source: string, start: number): number {
  let index = start;
  while (index < source.length && source[index] !== " " && source[index] !== "\t" && source[index] !== "\r") {
    index += 1;
  }
  return index;
}

function isDecimalInteger(input: string): boolean {
  return /^\d+$/.test(input);
}

function appendToken(tokens: RuleV1Token[], kind: RuleV1TokenKind, text: string): void {
  if (!text) return;
  const previous = tokens[tokens.length - 1];
  if (previous?.kind === kind) {
    tokens[tokens.length - 1] = { kind, text: previous.text + text };
    return;
  }
  tokens.push({ kind, text });
}

function tokenClass(kind: Exclude<RuleV1TokenKind, "plain">): string {
  switch (kind) {
    case "argument": return "rulev1-argument";
    case "comment": return "rulev1-comment";
    case "instruction": return "rulev1-instruction";
    case "number": return "rulev1-number";
  }
}
