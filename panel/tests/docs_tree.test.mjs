import assert from "node:assert/strict";
import { test } from "node:test";
import { JSDOM } from "jsdom";

import {
  handoffBoundaryScroll,
  initialTopicOpen,
  revealTreeItem,
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
  const current = tree.querySelector('[data-page-id="current"]');
  Object.defineProperty(tree, "clientHeight", { value: 100 });
  tree.getBoundingClientRect = () => ({ top: 20, bottom: 120 });
  current.getBoundingClientRect = () => ({ top: 130, bottom: 150 });

  selectPageInTree(tree, "current");

  assert.deepEqual([...tree.querySelectorAll("details")].map((topic) => topic.open), [true, true]);
  assert.equal(current.classList.contains("active"), true);
  assert.equal(tree.querySelector('[data-page-id="other"]')?.classList.contains("active"), false);
  assert.equal(tree.scrollTop, 30);
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

test("the documentation tree scrolls only when the current page is outside its viewport", () => {
  const document = new JSDOM("<nav><a>Current</a></nav>").window.document;
  const tree = document.querySelector("nav");
  const current = document.querySelector("a");
  Object.defineProperty(tree, "clientHeight", { value: 100 });
  tree.getBoundingClientRect = () => ({ top: 20, bottom: 120 });

  tree.scrollTop = 50;
  current.getBoundingClientRect = () => ({ top: 40, bottom: 60 });
  assert.equal(revealTreeItem(tree, current), false);
  assert.equal(tree.scrollTop, 50);

  current.getBoundingClientRect = () => ({ top: 5, bottom: 25 });
  assert.equal(revealTreeItem(tree, current), true);
  assert.equal(tree.scrollTop, 35);

  current.getBoundingClientRect = () => ({ top: 130, bottom: 150 });
  assert.equal(revealTreeItem(tree, current), true);
  assert.equal(tree.scrollTop, 65);
});
