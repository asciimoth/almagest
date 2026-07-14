package main

import (
	"context"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/configer/configer"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta"
)

func TestWorkersSnapshotResponseKeepsOnlyLiveWorkerParents(t *testing.T) {
	started := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	resp := workersSnapshotResponse(
		map[uint64]pasta.NodeSnapshot{
			10: {Name: "live", Class: "test/live"},
			20: {Name: "placeholder", Class: "test/placeholder", Placeholder: true},
			99: {Name: "idle", Class: "test/idle"},
		},
		map[uint64]almagest.WorkerSnapshot{
			1: {ID: 1, Node: 10, WorkerName: "live worker", StartedAt: started},
			2: {ID: 2, Node: 20, WorkerName: "placeholder worker", StartedAt: started},
			3: {ID: 3, Node: 30, Name: "gone", Class: "test/gone", WorkerName: "orphan", StartedAt: started, Orphan: true},
		},
	)

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

func TestEngineBatchNodeAPIDispatch(t *testing.T) {
	engine, err := almagest.BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		almagest.NewDebugLogFactory(16),
		&sysnetdebug.System{},
		[]pasta.NodeClass{adminAPIBatchNodeClass{}},
		nil,
		false,
	)
	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})
	session := (&AdminAPI{}).newAPISession(engine, nil, nil)

	first, err := engine.AddNode(almagest.E2EWorkspace, "test/AdminAPIBatchNode", "first")
	if err != nil {
		t.Fatalf("AddNode first error = %v, want nil", err)
	}
	second, err := engine.AddNode(almagest.E2EWorkspace, "test/AdminAPIBatchNode", "second")
	if err != nil {
		t.Fatalf("AddNode second error = %v, want nil", err)
	}

	moveReq, err := api.NewRequest("1", api.SurfaceEngine, string(api.MethodEngineMoveNodes), api.MoveNodesRequest{
		Workspace: almagest.E2EWorkspace,
		Positions: map[uint64]string{
			first:  "10 20",
			second: "30 40",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	moveResp, err := session.handleMessage(moveReq)
	if err != nil {
		t.Fatal(err)
	}
	if moveResp.Error != nil {
		t.Fatalf("move response error = %#v, want nil", moveResp.Error)
	}
	if got, ok := engine.NodeSnapshot(almagest.E2EWorkspace, second); !ok || got.Position != "30 40" {
		t.Fatalf("second snapshot = %#v, %v; want position 30 40", got, ok)
	}

	removeReq, err := api.NewRequest("2", api.SurfaceEngine, string(api.MethodEngineRemoveNodes), api.IDsRequest{
		Workspace: almagest.E2EWorkspace,
		IDs:       []uint64{first, second},
	})
	if err != nil {
		t.Fatal(err)
	}
	removeResp, err := session.handleMessage(removeReq)
	if err != nil {
		t.Fatal(err)
	}
	if removeResp.Error != nil {
		t.Fatalf("remove response error = %#v, want nil", removeResp.Error)
	}
	if _, ok := engine.NodeSnapshot(almagest.E2EWorkspace, first); ok {
		t.Fatal("first node still exists after remove_nodes dispatch")
	}
	if _, ok := engine.NodeSnapshot(almagest.E2EWorkspace, second); ok {
		t.Fatal("second node still exists after remove_nodes dispatch")
	}
}

type adminAPIBatchNodeClass struct{}

func (adminAPIBatchNodeClass) ClassName() string {
	return "test/AdminAPIBatchNode"
}

func (adminAPIBatchNodeClass) ShortDescription() string {
	return "Admin API batch node test"
}

func (adminAPIBatchNodeClass) LongDescription() string {
	return "Tests admin API batch node dispatch."
}

func (adminAPIBatchNodeClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{}
}

func (adminAPIBatchNodeClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &adminAPIBatchNode{}, nil
}

type adminAPIBatchNode struct {
	pasta.BasicNode
	_ byte
}
