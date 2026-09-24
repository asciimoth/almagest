package main

import (
	"testing"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/kataras/golog"
)

func TestSanitizeLogMessageDropsNonASCIIAndControlBytes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "printable ascii unchanged",
			in:   "abc XYZ 123 !@#$%^&*()[]{}",
			want: "abc XYZ 123 !@#$%^&*()[]{}",
		},
		{
			name: "line breaks dropped",
			in:   "first\nsecond\rthird\r\nfourth",
			want: "firstsecondthirdfourth",
		},
		{
			name: "controls and unicode dropped",
			in:   "ok\t\x1b[31m bad Привет \u202e end\x7f",
			want: "ok[31m bad   end",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeLogMessage(tt.in); got != tt.want {
				t.Fatalf("sanitizeLogMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLogFactoryCachesOrdersAndPublishesEntries(t *testing.T) {
	factory := NewLogFactory(LogCacheConfig{Debug: 2, Info: 1, Warn: 1, Error: 1, Fatal: 1})
	factory.level = logLevelEnabled{debug: true, info: true, warn: true, err: true, fatal: true}

	var published []api.LogEntry
	id, initial := factory.Subscribe(func(entry api.LogEntry) { published = append(published, entry) })
	if id == 0 || len(initial) != 0 {
		t.Fatalf("Subscribe() = %d, %#v", id, initial)
	}

	engine := factory.EngineLogger().(*Logger)
	daemon := factory.DaemonLogger().(*Logger)
	workspace := factory.WorkspaceLogger().(*Logger)
	node := factory.NodeLogger(42, "test/Node").(*Logger)
	engine.Debug(" first\n")
	engine.Debugf("%s", "second")
	daemon.Info(" info ")
	workspace.Infof("%s", "workspace")
	node.Warn("warn")
	node.Warnf("%s", "new warn")
	node.Err("error")
	node.Errf("%s", "new error")

	if len(published) != 8 {
		t.Fatalf("published entries = %d, want 8", len(published))
	}
	last := published[len(published)-1]
	if last.Source != "node" || last.NodeID != 42 || last.NodeClass != "test/Node" || last.Message != "new error" {
		t.Fatalf("last entry = %#v", last)
	}
	if factory.TotalCacheSize() != 6 {
		t.Fatalf("TotalCacheSize() = %d, want 6", factory.TotalCacheSize())
	}

	_, snapshot := factory.Subscribe(func(api.LogEntry) {})
	if len(snapshot) != 5 {
		t.Fatalf("snapshot entries = %d, want 5", len(snapshot))
	}
	if snapshot[0].Message != "first" || snapshot[1].Message != "second" {
		t.Fatalf("retained debug entries = %#v, want first and second", snapshot[:2])
	}
	factory.Unsubscribe(id)
	before := len(published)
	engine.Debug("after unsubscribe")
	if len(published) != before {
		t.Fatal("unsubscribed callback received an entry")
	}
}

func TestLogFactoryDisabledLevelsAndEmptyRings(t *testing.T) {
	levels := newLogLevelEnabled(golog.DisableLevel)
	factory := NewLogFactory(LogCacheConfig{})
	logger := &Logger{level: levels, logf: factory}
	logger.Debug("debug")
	logger.Debugf("%s", "debug")
	logger.Info("info")
	logger.Infof("%s", "info")
	logger.Warn("warn")
	logger.Warnf("%s", "warn")
	logger.Err("error")
	logger.Errf("%s", "error")
	if factory.TotalCacheSize() != 0 {
		t.Fatalf("TotalCacheSize() = %d, want 0", factory.TotalCacheSize())
	}

	ring := newLogRing(-1)
	ring.append(api.LogEntry{Message: "discarded"})
	if got := ring.ordered(); got != nil {
		t.Fatalf("empty ring ordered() = %#v, want nil", got)
	}
	(&Logger{}).emit("info", "discarded")
}

func TestLogRingWrapsInChronologicalOrder(t *testing.T) {
	ring := newLogRing(2)
	ring.append(api.LogEntry{At: "3", Message: "third"})
	ring.append(api.LogEntry{At: "1", Message: "first"})
	ring.append(api.LogEntry{At: "2", Message: "second"})
	got := ring.ordered()
	if len(got) != 2 || got[0].Message != "first" || got[1].Message != "second" {
		t.Fatalf("ordered() = %#v", got)
	}

	factory := NewLogFactory(LogCacheConfig{Info: 2, Warn: 2})
	factory.append(api.LogEntry{At: "3", Level: "info", Message: "later"})
	factory.append(api.LogEntry{At: "1", Level: "warn", Message: "earlier"})
	_, snapshot := factory.Subscribe(func(api.LogEntry) {})
	if len(snapshot) != 2 || snapshot[0].Message != "earlier" || snapshot[1].Message != "later" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}
