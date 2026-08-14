package logging

import (
	"testing"
	"time"
)

func TestDebugLogFactoryRecordsEntries(t *testing.T) {
	logf := NewDebugLogFactory(3)

	logf.EngineLogger().Debug("engine ready")
	logf.WorkspaceLogger().Infof("workspace %s", "ready")
	logf.NodeLogger(42, "test/Node").Err("node failed")

	entries := logf.Drain()
	if len(entries) != 3 {
		t.Fatalf("entry count = %d, want 3", len(entries))
	}
	if entries[0].Level != DebugLogLevelDebug || entries[0].Source.Name != "engine" || entries[0].Message != "engine ready" {
		t.Fatalf("engine entry = %#v", entries[0])
	}
	if entries[1].Level != DebugLogLevelInfo || entries[1].Source.Name != "workspace" || entries[1].Message != "workspace ready" {
		t.Fatalf("workspace entry = %#v", entries[1])
	}
	if entries[2].Level != DebugLogLevelError || entries[2].Source.NodeID != 42 || entries[2].Source.NodeClass != "test/Node" || entries[2].Message != "node failed" {
		t.Fatalf("node entry = %#v", entries[2])
	}
}

func TestDebugLoggerCanUseCallerOwnedChannel(t *testing.T) {
	entries := make(chan DebugLogEntry, 1)
	logger := NewDebugLogger(entries, DebugLogSource{Name: "custom"})

	logger.Fatalf("fatal %d", 7)

	entry := <-entries
	if entry.Level != DebugLogLevelFatal {
		t.Fatalf("level = %q, want %q", entry.Level, DebugLogLevelFatal)
	}
	if entry.Source.Name != "custom" {
		t.Fatalf("source name = %q, want custom", entry.Source.Name)
	}
	if entry.Message != "fatal 7" {
		t.Fatalf("message = %q, want fatal 7", entry.Message)
	}
}

func TestDebugLogFactoryDoesNotBlockWhenEntriesChannelIsFull(t *testing.T) {
	logf := NewDebugLogFactory(1)
	logger := logf.EngineLogger()

	logger.Info("first")
	done := make(chan struct{})
	go func() {
		logger.Info("second")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("factory logger blocked on full entries channel")
	}

	entries := logf.Drain()
	if len(entries) != 1 {
		t.Fatalf("entry count = %d, want 1", len(entries))
	}
	if entries[0].Message != "second" {
		t.Fatalf("message = %q, want second", entries[0].Message)
	}
}
