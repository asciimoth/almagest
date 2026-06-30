package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/pasta/pasta"
)

func TestWorkersTreePrintsWorkersUnderLivePlaceholderAndGoneNodes(t *testing.T) {
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	out := renderWorkersTree("main", api.WorkersSnapshotResponse{
		Nodes: map[uint64]pasta.NodeSnapshot{
			10: {Name: "server", Class: "test/server"},
			20: {Name: "broken", Class: "test/broken", Placeholder: true},
		},
		Workers: map[uint64]pasta.WorkerSnapshot{
			3: {ID: 3, Node: 10, Class: "test/server", Name: "server", WorkerName: "accept", StartedAt: now.Add(-90 * time.Second)},
			1: {ID: 1, Node: 10, Class: "test/server", Name: "server", WorkerName: "listen", StartedAt: now.Add(-2 * time.Minute)},
			2: {ID: 2, Node: 20, Class: "test/broken", Name: "broken", WorkerName: "retry", StartedAt: now.Add(-time.Hour)},
			4: {ID: 4, Node: 30, Class: "test/gone", Name: "deleted", WorkerName: "cleanup", StartedAt: now.Add(-5 * time.Second), Orphan: true},
		},
	}, now)

	want := strings.Join([]string{
		"main",
		"|-- node 10 server (test/server)",
		"|   |-- worker 1 name=\"listen\" age=2m0s",
		"|   `-- worker 3 name=\"accept\" age=1m30s",
		"|-- node 20 broken (test/broken) [placeholder]",
		"|   `-- worker 2 name=\"retry\" age=1h0m0s",
		"`-- node 30 deleted (test/gone) [gone]",
		"    `-- worker 4 name=\"cleanup\" age=5s",
		"",
	}, "\n")
	if out != want {
		t.Fatalf("tree output:\n%s\nwant:\n%s", out, want)
	}
}

func TestWorkersTreePrintsNoWorkers(t *testing.T) {
	out := renderWorkersTree("main", api.WorkersSnapshotResponse{}, time.Now())
	if out != "main\n`-- no workers\n" {
		t.Fatalf("tree output = %q", out)
	}
}

func TestRunWorkersTreeRequestsWorkersSnapshot(t *testing.T) {
	resp, err := api.NewResponse("1", api.WorkersSnapshotResponse{
		Nodes: map[uint64]pasta.NodeSnapshot{
			10: {Name: "server", Class: "test/server"},
		},
		Workers: map[uint64]pasta.WorkerSnapshot{
			1: {ID: 1, Node: 10, Class: "test/server", Name: "server", WorkerName: "listen", StartedAt: time.Now().Add(-time.Second)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ft := &fakeTransport{reads: []api.Message{resp}}
	var stdout, stderr bytes.Buffer

	err = run(context.Background(), []string{"workers", "tree", "--workspace", "main"}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if len(ft.writes) != 1 {
		t.Fatalf("writes = %d, want 1", len(ft.writes))
	}
	if ft.writes[0].Surface != api.SurfaceEngine || ft.writes[0].Method != string(api.MethodEngineWorkersSnapshot) {
		t.Fatalf("request = %#v", ft.writes[0])
	}
	var req api.WorkspaceRequest
	if err := json.Unmarshal(ft.writes[0].Payload, &req); err != nil {
		t.Fatal(err)
	}
	if req.Workspace != "main" {
		t.Fatalf("workspace = %q, want main", req.Workspace)
	}
	for _, want := range []string{"node 10 server", "worker 1", "name=\"listen\"", "age="} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, want substring %q", stdout.String(), want)
		}
	}
}
