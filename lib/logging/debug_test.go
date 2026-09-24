package logging

import (
	"testing"
	"time"
)

func TestDebugLogFactoryRecordsEngineWorkspaceAndNodeEntries(t *testing.T) {
	logf := NewDebugLogFactory(3)

	logf.EngineLogger().Debug("engine ready")
	logf.WorkspaceLogger().Infof("workspace %s", "ready")
	logf.NodeLogger(42, "test/Node").Err("node failed")

	entries := logf.Drain()
	if len(entries) != 3 {
		t.Fatalf("entry count = %d, want 3", len(entries))
	}

	tests := []struct {
		name    string
		entry   DebugLogEntry
		level   DebugLogLevel
		source  DebugLogSource
		message string
	}{
		{
			name:    "engine",
			entry:   entries[0],
			level:   DebugLogLevelDebug,
			source:  DebugLogSource{Name: "engine"},
			message: "engine ready",
		},
		{
			name:    "workspace",
			entry:   entries[1],
			level:   DebugLogLevelInfo,
			source:  DebugLogSource{Name: "workspace"},
			message: "workspace ready",
		},
		{
			name:    "node",
			entry:   entries[2],
			level:   DebugLogLevelError,
			source:  DebugLogSource{Name: "node", NodeID: 42, NodeClass: "test/Node"},
			message: "node failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.entry.At.IsZero() {
				t.Fatal("entry At is zero")
			}
			if tt.entry.Level != tt.level {
				t.Fatalf("level = %q, want %q", tt.entry.Level, tt.level)
			}
			if tt.entry.Source != tt.source {
				t.Fatalf("source = %#v, want %#v", tt.entry.Source, tt.source)
			}
			if tt.entry.Message != tt.message {
				t.Fatalf("message = %q, want %q", tt.entry.Message, tt.message)
			}
		})
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

func TestDebugLoggerRemainingMethodsAndNilBranches(t *testing.T) {
	logf := NewDebugLogFactory(-1)
	if logf.Entries() == nil {
		t.Fatal("Entries returned nil")
	}
	logger := logf.NodeLogger(1, "test")
	logger.Debugf("debug %d", 1)
	logger.Info("info")
	logger.Warn("warn")
	logger.Warnf("warn %d", 2)
	logger.Errf("error %d", 3)
	logger.Fatal("fatal")
	if entries := logf.Drain(); len(entries) != 1 || entries[0].Level != DebugLogLevelFatal {
		t.Fatalf("entries = %#v", entries)
	}

	var nilFactory *DebugLogFactory
	if nilFactory.Entries() != nil || nilFactory.Drain() != nil {
		t.Fatal("nil factory returned entries")
	}
	nilFactory.record(DebugLogEntry{})
	nilFactory.EngineLogger().Info("discarded")
	var nilLogger *DebugLogger
	nilLogger.Info("discarded")
}
