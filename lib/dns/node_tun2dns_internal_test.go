package dnsnodes

import (
	"errors"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
)

func TestTun2DnsStalePumpFailureCannotOverwriteReplacementLabel(t *testing.T) {
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(16))
	t.Cleanup(w.Close)
	id, err := w.AddNode(&tun2DNSLabelTestNode{}, "test/Tun2DNSLabel", "label owner")
	if err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if err := w.SetNodeLabel(id, "adapting DNS"); err != nil {
		t.Fatalf("SetNodeLabel: %v", err)
	}
	failedTun, failedPeer := tun.Pipe(1, 1500, 0, 0)
	replacementTun, replacementPeer := tun.Pipe(1, 1500, 0, 0)
	t.Cleanup(func() {
		_ = failedTun.Close()
		_ = failedPeer.Close()
		_ = replacementTun.Close()
		_ = replacementPeer.Close()
	})
	n := &tun2DNSNode{
		currentTun: failedTun,
		generation: 1,
	}
	n.W = w
	n.ID = id

	// Hold the workspace lock after the failure state changes but before its
	// label can publish. This models a replacement payload delivered between
	// the old pump failure and its asynchronous label update.
	w.Lock()
	done := make(chan struct{})
	go func() {
		n.failTunPump(1, failedTun, errors.New("old pump failed"))
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for {
		n.mu.Lock()
		failed := n.tunFailed
		n.mu.Unlock()
		if failed {
			break
		}
		if time.Now().After(deadline) {
			w.Unlock()
			t.Fatal("old pump did not record its failure")
		}
		time.Sleep(time.Millisecond)
	}
	n.mu.Lock()
	n.currentTun = replacementTun
	n.generation = 2
	n.tunFailed = false
	n.lastErr = ""
	n.mu.Unlock()
	w.Unlock()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("old pump failure did not finish")
	}
	snapshot, ok := w.NodeSnapshot(id)
	if !ok {
		t.Fatal("label owner node is missing")
	}
	if snapshot.Label != "adapting DNS" {
		t.Fatalf("node label = %q, want replacement label", snapshot.Label)
	}
}

type tun2DNSLabelTestNode struct {
	pasta.BasicNode
}
