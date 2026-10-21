import assert from "node:assert/strict";
import { test } from "node:test";
import { JSDOM } from "jsdom";

import { initialTopicOpen, selectPageInTree } from "../public/build/docs_tree.js";

test("the current page overrides a folded topic preference", () => {
  assert.equal(initialTopicOpen(true, false, true), true);
  assert.equal(initialTopicOpen(false, false, true), true);
  assert.equal(initialTopicOpen(true, false, false), false);
});

test("selecting a page opens all of its topic ancestors", () => {
  const document = new JSDOM(`
    <nav>
      <details>
        <summary>Outer</summary>
        <div>
          <a data-page-id="other" class="active">Other</a>
          <details>
            <summary>Inner</summary>
            <div><a data-page-id="current">Current</a></div>
          </details>
        </div>
      </details>
    </nav>
  `).window.document;
  const tree = document.querySelector("nav");

  selectPageInTree(tree, "current");

  assert.deepEqual([...tree.querySelectorAll("details")].map((topic) => topic.open), [true, true]);
  assert.equal(tree.querySelector('[data-page-id="current"]')?.classList.contains("active"), true);
  assert.equal(tree.querySelector('[data-page-id="other"]')?.classList.contains("active"), false);
});
