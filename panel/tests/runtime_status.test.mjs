import assert from "node:assert/strict";
import { test } from "node:test";
import { JSDOM } from "jsdom";

import { setRuntimeStatusVisibility } from "../public/build/runtime_status.js";

test("runtime security statuses hide independently and together", () => {
  const document = new JSDOM(`
    <div id="statuses"><button id="isolation"></button><button id="sandbox"></button></div>
  `).window.document;
  const container = document.querySelector("#statuses");
  const isolation = document.querySelector("#isolation");
  const sandbox = document.querySelector("#sandbox");

  for (const [hasIsolation, hasSandbox, containerHidden] of [
    [false, false, true],
    [true, false, false],
    [false, true, false],
    [true, true, false],
  ]) {
    setRuntimeStatusVisibility(container, isolation, sandbox, hasIsolation, hasSandbox);
    assert.equal(isolation.hidden, !hasIsolation);
    assert.equal(sandbox.hidden, !hasSandbox);
    assert.equal(container.hidden, containerHidden);
  }
});
