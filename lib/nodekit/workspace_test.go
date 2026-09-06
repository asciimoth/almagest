package nodekit

import (
	"testing"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/pasta/pasta"
)

func TestInitialNodeNameUsesWorkspaceRecordAndRestoredFallback(t *testing.T) {
	restored := &pasta.NodeInitData{Name: "restored name"}
	if got := InitialNodeName(nil, 0, restored); got != restored.Name {
		t.Fatalf("restored node name = %q, want %q", got, restored.Name)
	}

	w := pasta.NewWorkspace(logging.NewDebugLogFactory(8))
	t.Cleanup(func() { w.Close() })
	nodeName := " current name "
	nodeID, err := w.AddNode(&pasta.BasicNode{}, "test/Node", nodeName)
	if err != nil {
		t.Fatalf("add node: %v", err)
	}
	w.Lock()
	got := InitialNodeName(w, nodeID, restored)
	w.Unlock()
	if got != nodeName {
		t.Fatalf("workspace node name = %q, want exact name %q", got, nodeName)
	}
}
