package nodekit

import (
	"reflect"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/formular"
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

func TestRestoredPortHelpers(t *testing.T) {
	restored := &pasta.NodeInitData{LeftPorts: []uint64{11, 12}, RightPorts: []uint64{21}}
	if got := RestoredLeft(restored, 1); got != 12 {
		t.Fatalf("RestoredLeft() = %d, want 12", got)
	}
	if got := RestoredRight(restored, 0); got != 21 {
		t.Fatalf("RestoredRight() = %d, want 21", got)
	}
	for _, got := range []uint64{
		RestoredLeft(nil, 0), RestoredLeft(restored, -1), RestoredLeft(restored, 2),
		RestoredRight(nil, 0), RestoredRight(restored, -1), RestoredRight(restored, 1),
	} {
		if got != 0 {
			t.Errorf("out-of-range restored port = %d, want 0", got)
		}
	}
}

func TestSleepOrStopped(t *testing.T) {
	stop := make(chan struct{})
	close(stop)
	if SleepOrStopped(stop, time.Second) {
		t.Fatal("SleepOrStopped() = true after stop")
	}
	if !SleepOrStopped(make(chan struct{}), time.Millisecond) {
		t.Fatal("SleepOrStopped() = false after timer")
	}
}

func TestAppendCappedLogAndLogsBlock(t *testing.T) {
	var logs []formular.LogLine
	AppendCappedLog(nil, 2, "info", "ignored")
	AppendCappedLog(&logs, 0, "info", "ignored")
	AppendCappedLog(&logs, 2, "info", "first")
	AppendCappedLog(&logs, 2, "warn", "second")
	AppendCappedLog(&logs, 2, "error", "third")
	want := []formular.LogLine{{Level: "warn", Text: "second"}, {Level: "error", Text: "third"}}
	if !reflect.DeepEqual(logs, want) {
		t.Fatalf("logs = %#v, want %#v", logs, want)
	}
	block := LogsBlock("logs", "Worker", 4, logs)
	if block.ID != "logs" || block.Order != 4 || block.Generation != 1 || len(block.Items) != 1 {
		t.Fatalf("LogsBlock() = %#v", block)
	}
	logs[0].Text = "changed"
	if block.Items[0].Logs[0].Text != "second" {
		t.Fatal("LogsBlock retained caller-owned log slice")
	}
}
