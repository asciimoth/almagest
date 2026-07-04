import assert from "node:assert/strict";
import { test } from "node:test";
import {
  applyNotification,
  applyWorkspaceSnapshot,
  applyWorkerNotification,
  emptySnapshot,
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

test("workspace snapshots initialize worker state", () => {
  const next = applyNotification(emptySnapshot(), {
    kind: "workspace_snapshot",
    snapshot: {
      classes: {},
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
      classes: {},
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
    classes: {},
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
      classes: {},
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
