import assert from "node:assert/strict";
import { test } from "node:test";

import {
  applyStandardWheelZoom,
  redrawRevealedGraph,
} from "../public/build/graph_canvas_compat.js";

test("standard wheel events zoom around canvas-local pointer coordinates", () => {
  const calls = { center: null, changed: 0, prevented: 0 };
  const canvas = {
    allow_dragcanvas: true,
    ds: {
      scale: 2,
      changeScale(scale, center) {
        this.scale = scale;
        calls.center = center;
      },
    },
    graph: { change() { calls.changed += 1; } },
  };
  const element = {
    getBoundingClientRect: () => ({ left: 40, top: 70 }),
  };

  const handled = applyStandardWheelZoom(canvas, element, {
    clientX: 140,
    clientY: 270,
    deltaY: -20,
    preventDefault() { calls.prevented += 1; },
  });

  assert.equal(handled, true);
  assert.equal(canvas.ds.scale, 2.2);
  assert.deepEqual(calls.center, [100, 200]);
  assert.equal(calls.changed, 1);
  assert.equal(calls.prevented, 1);
});

test("standard wheel adapter leaves disabled and zero-delta input unchanged", () => {
  let changes = 0;
  const canvas = {
    allow_dragcanvas: false,
    ds: { scale: 1, changeScale() { changes += 1; } },
    graph: { change() { changes += 1; } },
  };
  const element = { getBoundingClientRect: () => ({ left: 0, top: 0 }) };
  const event = { clientX: 0, clientY: 0, deltaY: 1, preventDefault() { changes += 1; } };

  assert.equal(applyStandardWheelZoom(canvas, element, event), false);
  canvas.allow_dragcanvas = true;
  event.deltaY = 0;
  assert.equal(applyStandardWheelZoom(canvas, element, event), false);
  assert.equal(changes, 0);
});

test("revealing the graph dirties and draws both canvas layers", () => {
  const calls = [];
  redrawRevealedGraph({
    setDirty(foreground, background) { calls.push(["dirty", foreground, background]); },
    draw(foreground, background) { calls.push(["draw", foreground, background]); },
  });

  assert.deepEqual(calls, [
    ["dirty", true, true],
    ["draw", true, true],
  ]);
});
