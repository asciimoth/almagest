package debugnodes

import (
	"fmt"
	"testing"

	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/logging"
)

func TestDebugMarkdownClassSupportsManyInstancesAndReconstruction(t *testing.T) {
	const sourceCount = 64

	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	t.Cleanup(w.Close)
	if err := w.AddNodeClass(DebugMarkdownClass{}); err != nil {
		t.Fatalf("AddNodeClass: %v", err)
	}

	sources := make([]uint64, 0, sourceCount)
	for i := range sourceCount {
		name := fmt.Sprintf("markdown %02d", i)
		id, err := w.AddNodeByClass(NodeTypeDebugMarkdown, name)
		if err != nil {
			t.Fatalf("AddNodeByClass %q: %v", name, err)
		}
		sources = append(sources, id)
		assertLiveMarkdownNode(t, w, id, name)
	}

	clipboard := w.Copy(sources)
	pasted := w.Paste(clipboard)
	if len(pasted) != sourceCount {
		t.Fatalf("Paste returned %d nodes, want %d", len(pasted), sourceCount)
	}
	allIDs := make(map[uint64]struct{}, sourceCount*2)
	for _, id := range append(append([]uint64(nil), sources...), pasted...) {
		if _, duplicate := allIDs[id]; duplicate {
			t.Fatalf("node ID %d was reused", id)
		}
		allIDs[id] = struct{}{}
		assertLiveMarkdownNode(t, w, id, "")
	}

	if err := w.RemoveNodes(sources); err != nil {
		t.Fatalf("RemoveNodes: %v", err)
	}
	assertMarkdownNodesPresent(t, w, sources, false)
	assertMarkdownNodesPresent(t, w, pasted, true)

	// Undo reconstructs all removed implementations through the class factory.
	w.Undo()
	assertMarkdownNodesPresent(t, w, sources, true)
	assertMarkdownNodesPresent(t, w, pasted, true)

	w.Redo()
	assertMarkdownNodesPresent(t, w, sources, false)
	w.Undo()
	assertMarkdownNodesPresent(t, w, sources, true)

	nodes, err := w.NodesByClass(NodeTypeDebugMarkdown)
	if err != nil {
		t.Fatalf("NodesByClass: %v", err)
	}
	if len(nodes) != sourceCount*2 {
		t.Fatalf("Markdown node count = %d, want %d", len(nodes), sourceCount*2)
	}
}

func assertLiveMarkdownNode(t *testing.T, w *pasta.Workspace, id uint64, name string) {
	t.Helper()
	snapshot, ok := w.NodeSnapshot(id)
	if !ok {
		t.Fatalf("node %d is missing", id)
	}
	if snapshot.Class != NodeTypeDebugMarkdown || snapshot.Placeholder {
		t.Fatalf("node %d snapshot = %#v, want live Markdown node", id, snapshot)
	}
	if name != "" && snapshot.Name != name {
		t.Fatalf("node %d name = %q, want %q", id, snapshot.Name, name)
	}
}

func assertMarkdownNodesPresent(t *testing.T, w *pasta.Workspace, ids []uint64, want bool) {
	t.Helper()
	for _, id := range ids {
		_, present := w.NodeSnapshot(id)
		if present != want {
			t.Fatalf("node %d present = %t, want %t", id, present, want)
		}
		if present {
			assertLiveMarkdownNode(t, w, id, "")
		}
	}
}
