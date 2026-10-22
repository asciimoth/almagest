import { renderHuJSONHighlight } from "./hujson_highlight.js";
import { renderRuleV1Highlight } from "./rulev1_highlight.js";
import { renderSpaceTableHighlight } from "./spacetable_highlight.js";

/** The part of a Formular text field that the highlighting hook uses. */
export type FormularHighlightField = Readonly<{
  subtype?: string;
}>;

/** The stable subset of the Formular multiline hook context used here. */
export type FormularMultilineHighlightContext = Readonly<{
  field: FormularHighlightField;
  onValueChange(listener: (value: string) => void): void;
}>;

export type FormularMultilineHighlightRenderer = (
  textarea: HTMLTextAreaElement,
  context: FormularMultilineHighlightContext,
) => HTMLElement | undefined;

type HighlightRenderer = (container: HTMLElement, source: string) => void;
type FormularLabelItem = Readonly<{
  format?: string;
  syntax?: string;
  text?: string;
}>;

type FormularItemNode = HTMLElement & {
  // Formular keeps the current item here when it renders or patches a node.
  // It does not currently expose a label-rendering hook, so this is the only
  // source that also works for labels nested in array elements.
  __formularStateContext?: { item?: FormularLabelItem };
};

const HIGHLIGHTERS: Readonly<Record<string, HighlightRenderer>> = {
  json: renderHuJSONHighlight,
  hjson: renderHuJSONHighlight,
  hujson: renderHuJSONHighlight,
  rulev1: renderRuleV1Highlight,
  spacetable: renderSpaceTableHighlight,
};

/** Return a highlighter only for exact, protocol-defined language names. */
export function formularHighlighter(language: string | undefined): HighlightRenderer | undefined {
  return language === undefined ? undefined : HIGHLIGHTERS[language];
}

/**
 * Create the Formular multiline hook used by node menus.
 *
 * The supplied textarea remains the only interactive control. The highlight
 * layer is presentation-only and receives source through safe DOM operations
 * in the existing HuJSON, RuleV1, and space-table renderers.
 */
export function createFormularMultilineHighlightRenderer(): FormularMultilineHighlightRenderer {
  return (textarea, context) => {
    const render = formularHighlighter(context.field.subtype);
    if (!render) return undefined;

    const editor = textarea.ownerDocument.createElement("span");
    editor.className = "formular-syntax-editor";
    editor.dataset.syntax = context.field.subtype ?? "";

    const highlight = textarea.ownerDocument.createElement("pre");
    highlight.className = "formular-syntax-highlight";
    highlight.setAttribute("aria-hidden", "true");

    const update = (value = textarea.value): void => render(highlight, value);
    const syncScroll = (): void => {
      highlight.scrollTop = textarea.scrollTop;
      highlight.scrollLeft = textarea.scrollLeft;
    };
    textarea.classList.add("formular-syntax-input");
    textarea.addEventListener("input", () => update());
    textarea.addEventListener("scroll", syncScroll);
    context.onValueChange(update);
    editor.append(highlight, textarea);
    return editor;
  };
}

/**
 * Highlight code labels after Formular creates or patches them.
 *
 * Formular 0.4 provides a hook for multiline fields but not for code labels.
 * Watching its rendered item nodes also covers labels in arrays and labels
 * created later when a block expands. The marker prevents observer feedback.
 */
export function observeFormularCodeLabels(root: HTMLElement): () => void {
  const refresh = (): void => {
    for (const node of root.querySelectorAll<FormularItemNode>('[data-formular-item-type="label"]')) {
      const item = node.__formularStateContext?.item;
      if (item?.format !== "code") continue;
      const render = formularHighlighter(item.syntax);
      if (!render) continue;
      const code = node.querySelector<HTMLElement>("pre > code");
      if (!code) continue;
      const source = item.text ?? "";
      if (code.dataset.formularHighlightedSyntax === item.syntax && code.textContent === source) continue;
      render(code, source);
      code.dataset.formularHighlightedSyntax = item.syntax ?? "";
    }
  };

  const observer = new MutationObserver(refresh);
  observer.observe(root, { childList: true, subtree: true });
  refresh();
  return () => observer.disconnect();
}
