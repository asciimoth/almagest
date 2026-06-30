package main

import (
	"testing"
	"time"

	"github.com/asciimoth/pasta/pasta"
)

func TestWorkersSnapshotResponseKeepsOnlyLiveWorkerParents(t *testing.T) {
	started := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	resp := workersSnapshotResponse(pasta.WorkspaceSnapshot{
		Nodes: map[uint64]pasta.NodeSnapshot{
			10: {Name: "live", Class: "test/live"},
			20: {Name: "placeholder", Class: "test/placeholder", Placeholder: true},
			99: {Name: "idle", Class: "test/idle"},
		},
		Workers: map[uint64]pasta.WorkerSnapshot{
			1: {ID: 1, Node: 10, WorkerName: "live worker", StartedAt: started},
			2: {ID: 2, Node: 20, WorkerName: "placeholder worker", StartedAt: started},
			3: {ID: 3, Node: 30, Name: "gone", Class: "test/gone", WorkerName: "orphan", StartedAt: started, Orphan: true},
		},
	})

	if len(resp.Workers) != 3 {
		t.Fatalf("workers = %d, want 3", len(resp.Workers))
	}
	if _, ok := resp.Nodes[10]; !ok {
		t.Fatal("live worker parent missing")
	}
	if node, ok := resp.Nodes[20]; !ok || !node.Placeholder {
		t.Fatalf("placeholder parent = %#v, want present placeholder", node)
	}
	if _, ok := resp.Nodes[30]; ok {
		t.Fatal("gone worker parent should not be present")
	}
	if _, ok := resp.Nodes[99]; ok {
		t.Fatal("idle node should not be included")
	}
}
