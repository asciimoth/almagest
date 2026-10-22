import assert from "node:assert/strict";
import { test } from "node:test";
import { JSDOM } from "jsdom";
import { FormularMenu } from "../vendor/formular-menu/formular-menu.js";
import {
  createFormularMultilineHighlightRenderer,
  formularHighlighter,
  observeFormularCodeLabels,
} from "../public/build/formular_highlight.js";

const knownLanguages = ["json", "hjson", "hujson", "rulev1", "spacetable"];

function installDOM() {
  const dom = new JSDOM('<!doctype html><div id="node-menu"></div>', {
    url: "http://localhost/",
    runScripts: "dangerously",
  });
  const previous = {
    document: globalThis.document,
    window: globalThis.window,
    MutationObserver: globalThis.MutationObserver,
  };
  globalThis.document = dom.window.document;
  globalThis.window = dom.window;
  globalThis.MutationObserver = dom.window.MutationObserver;
  return {
    dom,
    restore() {
      globalThis.document = previous.document;
      globalThis.window = previous.window;
      globalThis.MutationObserver = previous.MutationObserver;
      dom.window.close();
    },
  };
}

function field(id, subtype, value) {
  return {
    type: "field",
    id,
    kind: "text",
    label: id,
    multiline: true,
    subtype,
    value,
  };
}

function label(id, syntax, text) {
  return { type: "label", id, format: "code", syntax, text };
}

function snapshot(items) {
  return {
    type: "menu.snapshot",
    menuId: "NODE1MENU",
    menuGeneration: 1,
    blocks: [{ id: "content", order: 1, generation: 1, form: false, items }],
  };
}

function nextMutationTurn(dom) {
  return new Promise((resolve) => dom.window.setTimeout(resolve, 0));
}

test("language lookup accepts only the five known protocol names", () => {
  for (const language of knownLanguages) assert.equal(typeof formularHighlighter(language), "function");
  for (const language of [undefined, "", "JSON", " json", "javascript", "json<script>"]) {
    assert.equal(formularHighlighter(language), undefined);
  }
});

test("Formular hook highlights every known field subtype and leaves unknown subtypes native", () => {
  const { dom, restore } = installDOM();
  try {
    const menu = new FormularMenu("node-menu", "NODE1MENU", () => {}, {
      renderMultilineTextInput: createFormularMultilineHighlightRenderer(),
    });
    assert.equal(menu.feed(snapshot([
      field("json", "json", '{"key": true}'),
      field("hjson", "hjson", "{/* comment */ key: 7}"),
      field("hujson", "hujson", '{"key": null,}'),
      field("rule", "rulev1", "DOMAIN example.com"),
      field("table", "spacetable", 'key "two words" 7'),
      field("plain", "javascript", "const unsafe = true"),
    ])), true);

    for (const language of knownLanguages) {
      const textarea = dom.window.document.querySelector(`[data-formular-item-id="${language === "rulev1" ? "rule" : language === "spacetable" ? "table" : language}"] textarea`);
      assert.ok(textarea);
      const editor = textarea.closest(".formular-syntax-editor");
      assert.equal(editor?.dataset.syntax, language);
      assert.equal(editor?.querySelector("pre")?.getAttribute("aria-hidden"), "true");
      assert.equal(editor?.querySelector("pre")?.textContent, textarea.value);
    }
    assert.equal(dom.window.document.querySelector('[data-formular-item-id="plain"] .formular-syntax-editor'), null);
    menu.destroy();
  } finally {
    restore();
  }
});

test("field highlighting follows user input, backend patches, and horizontal scroll", () => {
  const { dom, restore } = installDOM();
  try {
    const menu = new FormularMenu("node-menu", "NODE1MENU", () => {}, {
      renderMultilineTextInput: createFormularMultilineHighlightRenderer(),
    });
    menu.feed(snapshot([field("source", "json", '{"before": 1}')]));
    const textarea = dom.window.document.querySelector("textarea");
    const highlight = dom.window.document.querySelector(".formular-syntax-highlight");
    assert.ok(textarea && highlight);

    menu.feed({
      type: "block.snapshot",
      menuId: "NODE1MENU",
      menuGeneration: 1,
      block: { id: "content", order: 1, generation: 2, form: false, items: [field("source", "json", '{"server": null}')] },
    });
    assert.equal(textarea.value, '{"server": null}');
    assert.equal(highlight.textContent, textarea.value);

    textarea.value = '{"after": false}';
    textarea.dispatchEvent(new dom.window.Event("input", { bubbles: true }));
    assert.equal(highlight.textContent, textarea.value);
    assert.notEqual(highlight.querySelector(".hujson-keyword"), null);

    textarea.scrollLeft = 31;
    textarea.scrollTop = 7;
    textarea.dispatchEvent(new dom.window.Event("scroll"));
    assert.equal(highlight.scrollLeft, 31);
    assert.equal(highlight.scrollTop, 7);
    menu.destroy();
  } finally {
    restore();
  }
});

test("code labels highlight Item.Syntax on initial, patched, and newly expanded content", async () => {
  const { dom, restore } = installDOM();
  let stop = () => {};
  try {
    const root = dom.window.document.querySelector("#node-menu");
    const menu = new FormularMenu(root, "NODE1MENU", () => {});
    stop = observeFormularCodeLabels(root);
    menu.feed(snapshot(knownLanguages.map((syntax) => label(syntax, syntax, syntax === "rulev1" ? "MATCH 80" : syntax === "spacetable" ? "port 80" : '{"port": 80}'))));
    await nextMutationTurn(dom);

    for (const syntax of knownLanguages) {
      const code = root.querySelector(`[data-formular-item-id="${syntax}"] code`);
      assert.ok(code);
      assert.equal(code.dataset.formularHighlightedSyntax, syntax);
      assert.notEqual(code.querySelector("span"), null);
    }

    menu.feed({
      type: "block.snapshot",
      menuId: "NODE1MENU",
      menuGeneration: 1,
      block: { id: "content", order: 1, generation: 2, form: false, items: [label("json", "json", '{"patched": true}')] },
    });
    await nextMutationTurn(dom);
    const patched = root.querySelector('[data-formular-item-id="json"] code');
    assert.equal(patched.textContent, '{"patched": true}');
    assert.notEqual(patched.querySelector(".hujson-keyword"), null);
    menu.destroy();
  } finally {
    stop();
    restore();
  }
});

test("field and label highlighting never parses hostile source as HTML", async () => {
  const { dom, restore } = installDOM();
  let stop = () => {};
  try {
    const payload = '</span><img src=x onerror="globalThis.__formularXSS=true"><script>globalThis.__formularXSS=true</script><svg/onload=globalThis.__formularXSS=true>';
    const root = dom.window.document.querySelector("#node-menu");
    const menu = new FormularMenu(root, "NODE1MENU", () => {}, {
      renderMultilineTextInput: createFormularMultilineHighlightRenderer(),
    });
    stop = observeFormularCodeLabels(root);
    menu.feed(snapshot([
      field("source", "json", payload),
      label("result", "hujson", payload),
    ]));
    await nextMutationTurn(dom);

    for (const highlighted of root.querySelectorAll(".formular-syntax-highlight, [data-formular-item-id='result'] code")) {
      assert.equal(highlighted.textContent, payload);
      assert.equal(highlighted.querySelector("script,img,svg,iframe,math"), null);
      assert.equal([...highlighted.querySelectorAll("span")].every((span) => /^hujson-(?:comment|key|keyword|number|punctuation|string)$/.test(span.className)), true);
    }
    assert.equal(dom.window.__formularXSS, undefined);
    menu.destroy();
  } finally {
    stop();
    restore();
  }
});
