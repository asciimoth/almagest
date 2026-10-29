import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import {
  preservePendingNodePosition,
  preservePendingNodePositions,
  shouldDeferGraphUpdate,
} from "../public/build/graph_update_guard.js";

const appSource = await readFile(new URL("../src/app.ts", import.meta.url), "utf8");

const node = (position, label = "") => ({
  class: "demo/node",
  name: "node",
  primary_type: "demo/type",
  label,
  position,
  placeholder: false,
  root: false,
  has_root_path: false,
  left_ports: [],
  right_ports: [],
});

test("graph updates wait while a node or link is being dragged", () => {
  const dragged = { id: 10 };
  assert.equal(shouldDeferGraphUpdate({
    pointer_is_down: true,
    node_dragged: dragged,
    selected_nodes: { 10: dragged, 20: { id: 20 } },
  }, ["10", "20"]), true);

  assert.equal(shouldDeferGraphUpdate({
    pointer_is_down: true,
    connecting_node: { id: 10 },
  }, ["10", "20"]), true);
});

test("a deleted interaction node interrupts the deferred update", () => {
  assert.equal(shouldDeferGraphUpdate({
    pointer_is_down: true,
    node_dragged: { id: 10 },
    selected_nodes: { 10: { id: 10 }, 20: { id: 20 } },
  }, ["10"]), false);
  assert.equal(shouldDeferGraphUpdate({
    pointer_is_down: true,
    connecting_node: { id: 10 },
  }, ["20"]), false);
});

test("pointer actions without a node also defer object replacement", () => {
  assert.equal(shouldDeferGraphUpdate({ pointer_is_down: true }, ["10"]), true);
  assert.equal(shouldDeferGraphUpdate({ pointer_is_down: false }, ["10"]), false);
});

test("pending moves survive unrelated node updates until acknowledged", () => {
  const pending = new Map([[10, '{"x":300,"y":400}']]);
  const update = preservePendingNodePosition(10, node('{"x":0,"y":0}', "bootstrapping"), pending);
  assert.equal(update.position, '{"x":300,"y":400}');
  assert.equal(update.label, "bootstrapping");
  assert.equal(pending.has(10), true);

  const acknowledged = preservePendingNodePosition(10, node('{"x":300,"y":400}', "ready"), pending);
  assert.equal(acknowledged.position, '{"x":300,"y":400}');
  assert.equal(pending.has(10), false);
});

test("complete snapshots discard pending positions only for deleted nodes", () => {
  const pending = new Map([[10, "one"], [20, "two"]]);
  preservePendingNodePositions({ 10: node("one") }, pending, true);
  assert.deepEqual([...pending], []);
});

test("graph notification bursts coalesce into one animation-frame render", () => {
  const requestRender = appSource.slice(
    appSource.indexOf("function requestGraphRender"),
    appSource.indexOf("function finishGraphPointerInteraction"),
  );
  assert.match(requestRender, /graphRenderPending = true/);
  assert.match(requestRender, /if \(graphRenderFrame\) return/);
  assert.match(requestRender, /graphRenderFrame = window\.requestAnimationFrame/);
  assert.match(requestRender, /shouldDeferGraphUpdate/);
  assert.match(requestRender, /graphRenderPending = false;\s*renderGraph\(\)/);
});

test("node creation draws a local preview before the backend round trip", () => {
  const addNode = appSource.slice(
    appSource.indexOf("function addPastaNodeAt"),
    appSource.indexOf("function openDocumentation"),
  );
  assert.match(addNode, /pendingNodePlacements\.push\(placement\);\s*addPendingNodePreview\(placement\);\s*void tryBackend/);

  const preview = appSource.slice(
    appSource.indexOf("function addPendingNodePreview"),
    appSource.indexOf("function selectGraphNodes"),
  );
  assert.match(preview, /node\.id = placement\.previewID/);
  assert.match(preview, /placement\.id !== undefined && snapshot\.nodes\[String\(placement\.id\)\]/);
  assert.match(preview, /for \(const port of cls\.initial_ports\)/);
  assert.match(preview, /graph\.add\(node\)/);
  assert.match(addNode, /if \(id === null\) \{\s*forgetPendingNodePlacement\(placement\);\s*requestGraphRender\(\)/);
});
