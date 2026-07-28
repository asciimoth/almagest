import assert from "node:assert/strict";
import { test } from "node:test";
import {
  applyNotification,
  applyWorkspaceSnapshot,
  applyWorkerNotification,
  emptySnapshot,
  isPastaClipboard,
  nodeClassByName,
  nodePositionMap,
  positionClipboardAt,
} from "../public/build/pasta_model.js";

const node = {
  class: "demo/server",
  name: "server",
  primary_type: "demo/net",
  label: "",
  position: "",
  placeholder: false,
  root: false,
  has_root_path: false,
  left_ports: [],
  right_ports: [],
};

function nodeClass(name, primaryType = "") {
  return {
    class: name,
    short_description: "",
    unique: false,
    primary_type: primaryType,
    initial_ports: [],
  };
}

function worker(id, nodeID = 10) {
  return {
    id,
    node: nodeID,
    class: "demo/server",
    name: "server",
    worker_name: `worker-${id}`,
    started_at: "2026-06-30T00:00:00Z",
    orphan: false,
  };
}

test("workspace snapshots preserve received class order", () => {
  const next = applyWorkspaceSnapshot(emptySnapshot(), {
    classes: [
      nodeClass("pasta/PopUp", "pasta/popup"),
      nodeClass("almagest/Tun", "almagest/tun"),
      nodeClass("example/Untyped"),
    ],
    nodes: {},
    ports: {},
    links: {},
  });

  assert.deepEqual(next.classes.map((cls) => cls.class), ["pasta/PopUp", "almagest/Tun", "example/Untyped"]);
  assert.equal(nodeClassByName(next, "almagest/Tun")?.primary_type, "almagest/tun");
});

test("class notifications update the ordered class list without resorting it", () => {
  let snapshot = applyWorkspaceSnapshot(emptySnapshot(), {
    classes: [nodeClass("pasta/PopUp"), nodeClass("example/Worker")],
    nodes: {},
    ports: {},
    links: {},
  });

  snapshot = applyNotification(snapshot, {
    kind: "node_class_added",
    class_name: "almagest/Tun",
    node_class: nodeClass("almagest/Tun"),
  });
  assert.deepEqual(snapshot.classes.map((cls) => cls.class), ["pasta/PopUp", "example/Worker", "almagest/Tun"]);

  snapshot = applyNotification(snapshot, { kind: "node_class_removed", class_name: "example/Worker" });
  assert.deepEqual(snapshot.classes.map((cls) => cls.class), ["pasta/PopUp", "almagest/Tun"]);
});

test("workspace snapshots initialize worker state", () => {
  const next = applyNotification(emptySnapshot(), {
    kind: "workspace_snapshot",
    snapshot: {
      classes: [],
      nodes: { 10: node },
      ports: {},
      links: {},
      workers: { 1: worker(1) },
    },
  });

  assert.deepEqual(Object.keys(next.workers), ["1"]);
  assert.equal(next.workers["1"].worker_name, "worker-1");
});

test("workspace snapshots without workers preserve worker notifications", () => {
  let snapshot = applyWorkerNotification(emptySnapshot(), {
    kind: "worker_snapshot",
    snapshot: { 1: worker(1) },
  });

  snapshot = applyNotification(snapshot, {
    kind: "workspace_snapshot",
    snapshot: {
      classes: [],
      nodes: { 10: node },
      ports: {},
      links: {},
    },
  });

  assert.deepEqual(Object.keys(snapshot.workers), ["1"]);
  assert.equal(snapshot.workers["1"].worker_name, "worker-1");
});

test("direct workspace refresh without workers preserves worker notifications", () => {
  const snapshot = applyWorkerNotification(emptySnapshot(), {
    kind: "worker_snapshot",
    snapshot: { 1: worker(1) },
  });

  const next = applyWorkspaceSnapshot(snapshot, {
    classes: [],
    nodes: { 10: node },
    ports: {},
    links: {},
  });

  assert.deepEqual(Object.keys(next.workers), ["1"]);
  assert.equal(next.workers["1"].worker_name, "worker-1");
});

test("worker notifications update local worker state", () => {
  let snapshot = applyNotification(emptySnapshot(), {
    kind: "workspace_snapshot",
    snapshot: {
      classes: [],
      nodes: { 10: node },
      ports: {},
      links: {},
      workers: { 1: worker(1) },
    },
  });

  snapshot = applyNotification(snapshot, { kind: "worker_spawned", id: 2, worker: worker(2) });
  assert.deepEqual(Object.keys(snapshot.workers).sort(), ["1", "2"]);

  snapshot = applyNotification(snapshot, { kind: "worker_failed", id: 1, worker: { ...worker(1), error: "boom" } });
  assert.deepEqual(Object.keys(snapshot.workers), ["2"]);

  snapshot = applyNotification(snapshot, { kind: "worker_stopped", id: 2, worker: worker(2) });
  assert.deepEqual(snapshot.workers, {});
});

test("batch node notifications update local graph state", () => {
  let snapshot = applyNotification(emptySnapshot(), {
    kind: "workspace_snapshot",
    snapshot: {
      classes: [],
      nodes: {
        10: { ...node, name: "first", position: "" },
        20: { ...node, name: "second", position: "" },
        30: { ...node, name: "third", position: "" },
      },
      ports: { 1: { direction: "left", node: 10, name: "In", types: [], links: [3] } },
      links: { 3: { type: "demo/net", placeholder: false, left_port: 1, left_port_node: 10, right_port: 2, right_port_node: 20 } },
    },
  });

  snapshot = applyNotification(snapshot, {
    kind: "nodes_updated",
    nodes: {
      10: { ...node, name: "first", position: "10 20" },
      20: { ...node, name: "second", position: "30 40" },
    },
  });
  assert.equal(snapshot.nodes["10"].position, "10 20");
  assert.equal(snapshot.nodes["20"].position, "30 40");
  assert.equal(snapshot.nodes["30"].name, "third");

  snapshot = applyNotification(snapshot, {
    kind: "nodes_removed",
    nodes: {
      10: { ...node, name: "first", position: "10 20" },
      20: { ...node, name: "second", position: "30 40" },
    },
    snapshot: {
      classes: [],
      nodes: { 30: { ...node, name: "third", position: "" } },
      ports: {},
      links: {},
    },
  });
  assert.deepEqual(Object.keys(snapshot.nodes), ["30"]);
  assert.deepEqual(snapshot.ports, {});
  assert.deepEqual(snapshot.links, {});
});

test("nodePositionMap builds batch move API payloads", () => {
  assert.deepEqual(nodePositionMap([
    { id: 10, position: "10 20" },
    { id: 20, position: "30 40" },
  ]), {
    10: "10 20",
    20: "30 40",
  });
});

test("clipboard helpers validate and place pasted nodes around target", () => {
  const data = JSON.stringify({
    version: 1,
    nodes: [
      { id: 10, class: "demo/server", name: "left", position: JSON.stringify({ x: 10, y: 20 }) },
      { id: 20, class: "demo/server", name: "right", position: JSON.stringify({ x: 50, y: 60 }) },
    ],
    links: [],
  });

  assert.equal(isPastaClipboard(data), true);
  assert.equal(isPastaClipboard("plain text"), false);

  const positioned = JSON.parse(positionClipboardAt(data, [200, 300]));
  assert.equal(positioned.nodes[0].position, JSON.stringify({ x: 180, y: 280 }));
  assert.equal(positioned.nodes[1].position, JSON.stringify({ x: 220, y: 320 }));
});
