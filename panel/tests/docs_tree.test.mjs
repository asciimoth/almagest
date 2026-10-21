import assert from "node:assert/strict";
import { test } from "node:test";
import { JSDOM } from "jsdom";

import {
  handoffBoundaryScroll,
  initialTopicOpen,
  selectPageInTree,
} from "../public/build/docs_tree.js";

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

test("sidebar wheel scrolling continues in the document at its boundaries", () => {
  const page = { clientHeight: 600, scrollHeight: 2400, scrollTop: 300 };
  const tree = { clientHeight: 600, scrollHeight: 1200, scrollTop: 0 };

  assert.equal(handoffBoundaryScroll(tree, page, -40), true);
  assert.equal(page.scrollTop, 260);

  tree.scrollTop = 300;
  assert.equal(handoffBoundaryScroll(tree, page, 40), false);
  assert.equal(page.scrollTop, 260);

  tree.scrollTop = 600;
  assert.equal(handoffBoundaryScroll(tree, page, 40), true);
  assert.equal(page.scrollTop, 300);
});
