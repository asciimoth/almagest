import assert from "node:assert/strict";
import { test } from "node:test";

import {
  applyStandardWheelZoom,
  GraphPinchZoom,
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

test("two touch pointers zoom and pan around their moving midpoint", () => {
  let changes = 0;
  const canvas = {
    allow_dragcanvas: true,
    ds: {
      scale: 1,
      offset: [0, 0],
      changeScale(scale, center) {
        this.scale = scale;
        assert.deepEqual(center, [60, 50]);
      },
    },
    graph: { change() { changes += 1; } },
  };
  const pinch = new GraphPinchZoom(canvas, {
    getBoundingClientRect: () => ({ left: 10, top: 20 }),
  });

  assert.equal(pinch.start({ pointerId: 1, pointerType: "touch", clientX: 50, clientY: 70 }), false);
  assert.equal(pinch.start({ pointerId: 2, pointerType: "touch", clientX: 90, clientY: 70 }), true);
  assert.equal(pinch.move({ pointerId: 2, pointerType: "touch", clientX: 130, clientY: 90 }), true);
  assert.equal(canvas.ds.scale, Math.sqrt(6800) / 40);
  assert.deepEqual(canvas.ds.offset.map((value) => Math.round(value * 1000) / 1000), [9.701, 4.851]);
  assert.equal(changes, 1);
  assert.equal(pinch.end({ pointerId: 2, pointerType: "touch", clientX: 130, clientY: 90 }), true);
  assert.equal(pinch.end({ pointerId: 1, pointerType: "touch", clientX: 50, clientY: 70 }), true);
});

test("pinch zoom ignores mouse pointers and resets after cancellation", () => {
  const canvas = {
    allow_dragcanvas: true,
    ds: { scale: 1, offset: [0, 0], changeScale() {} },
    graph: { change() {} },
  };
  const pinch = new GraphPinchZoom(canvas, { getBoundingClientRect: () => ({ left: 0, top: 0 }) });

  assert.equal(pinch.start({ pointerId: 1, pointerType: "mouse", clientX: 0, clientY: 0 }), false);
  pinch.start({ pointerId: 2, pointerType: "touch", clientX: 0, clientY: 0 });
  pinch.start({ pointerId: 3, pointerType: "touch", clientX: 20, clientY: 0 });
  pinch.cancel();
  assert.equal(pinch.move({ pointerId: 2, pointerType: "touch", clientX: 2, clientY: 0 }), false);
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
