import assert from "node:assert/strict";
import { test } from "node:test";
import { fitGraphRectangles } from "../public/build/graph_viewport.js";

test("graph viewport fits and centers all node bounds", () => {
  const viewport = fitGraphRectangles([
    [100, 50, 200, 100],
    [500, 250, 100, 200],
  ], 1000, 600, 50, 10);

  assert.deepEqual(viewport, {
    scale: 1.25,
    offset: [50, -10],
  });
});

test("graph viewport caps zoom-in at the default scale but always fits large content", () => {
  assert.equal(fitGraphRectangles([[0, 0, 10, 10]], 1000, 600, 40, 1)?.scale, 1);
  assert.equal(fitGraphRectangles([[0, 0, 20_000, 20_000]], 1000, 600, 40, 1)?.scale, 0.026);
});

test("graph viewport ignores invalid rectangles and returns null without content", () => {
  assert.equal(fitGraphRectangles([], 1000, 600, 40, 10), null);
  assert.equal(fitGraphRectangles([[Number.NaN, 0, 20, 20]], 1000, 600, 40, 10), null);
});
