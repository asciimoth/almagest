package logging

import (
	"fmt"
	"sync"
	"time"

	"github.com/asciimoth/pasta/pasta"
)

var (
	_ Logger     = (*DebugLogger)(nil)
	_ LogFactory = (*DebugLogFactory)(nil)
)

// DebugLogLevel identifies the severity of a debug log entry.
type DebugLogLevel string

const (
	DebugLogLevelDebug DebugLogLevel = "debug"
	DebugLogLevelInfo  DebugLogLevel = "info"
	DebugLogLevelWarn  DebugLogLevel = "warn"
	DebugLogLevelError DebugLogLevel = "error"
	DebugLogLevelFatal DebugLogLevel = "fatal"
)

// DebugLogSource identifies which runtime component produced a log entry.
type DebugLogSource struct {
	Name      string
	NodeID    uint64
	NodeClass string
}

// DebugLogEntry is one log record emitted by DebugLogger.
type DebugLogEntry struct {
	At      time.Time
	Level   DebugLogLevel
	Source  DebugLogSource
	Message string
}

// DebugLogFactory creates in-memory loggers for tests and debugging.
//
// Factory-created loggers must not block runtime workers when a test is not
// actively draining logs. Entries receives best-effort channel copies for
// callers that want to select on log activity; Drain returns recent entries
// stored by the factory since the previous drain. When the configured buffer is
// full, the oldest entries are dropped.
type DebugLogFactory struct {
	mu      sync.Mutex
	entries []DebugLogEntry
	entryCh chan DebugLogEntry
	limit   int
}

// NewDebugLogFactory returns a LogFactory that records entries in memory.
func NewDebugLogFactory(buffer int) *DebugLogFactory {
	if buffer < 0 {
		buffer = 0
	}
	limit := buffer
	if limit == 0 {
		limit = 1
	}
	return &DebugLogFactory{
		entryCh: make(chan DebugLogEntry, buffer),
		limit:   limit,
	}
}

// Entries returns the channel used by loggers created from this factory.
func (f *DebugLogFactory) Entries() <-chan DebugLogEntry {
	if f == nil {
		return nil
	}
	return f.entryCh
}

// Drain removes and returns all entries recorded by factory-created loggers
// since the previous drain.
func (f *DebugLogFactory) Drain() []DebugLogEntry {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	entries := f.entries
	f.entries = nil
	f.mu.Unlock()

	for {
		select {
		case <-f.entryCh:
		default:
			return entries
		}
	}
}

func (f *DebugLogFactory) record(entry DebugLogEntry) {
	if f == nil {
		return
	}
	f.mu.Lock()
	if len(f.entries) >= f.limit {
		copy(f.entries, f.entries[1:])
		f.entries[len(f.entries)-1] = entry
	} else {
		f.entries = append(f.entries, entry)
	}
	f.mu.Unlock()
	select {
	case f.entryCh <- entry:
	default:
	}
}

// EngineLogger returns the logger used by an engine.
func (f *DebugLogFactory) EngineLogger() Logger {
	return f.logger(DebugLogSource{Name: "engine"})
}

// WorkspaceLogger returns the logger used by a workspace.
func (f *DebugLogFactory) WorkspaceLogger() pasta.Logger {
	return f.logger(DebugLogSource{Name: "workspace"})
}

// NodeLogger returns the logger used by one node.
func (f *DebugLogFactory) NodeLogger(id uint64, class string) pasta.Logger {
	return f.logger(DebugLogSource{Name: "node", NodeID: id, NodeClass: class})
}

func (f *DebugLogFactory) logger(source DebugLogSource) *DebugLogger {
	if f == nil {
		return &DebugLogger{source: source}
	}
	return &DebugLogger{factory: f, source: source}
}

// DebugLogger records log entries to a factory or caller-owned channel.
type DebugLogger struct {
	factory *DebugLogFactory
	entries chan<- DebugLogEntry
	source  DebugLogSource
}

// NewDebugLogger returns a logger that writes to entries with the given source.
func NewDebugLogger(entries chan<- DebugLogEntry, source DebugLogSource) *DebugLogger {
	return &DebugLogger{entries: entries, source: source}
}

func (l *DebugLogger) Debug(args ...any) {
	l.emit(DebugLogLevelDebug, fmt.Sprint(args...))
}

func (l *DebugLogger) Debugf(format string, args ...any) {
	l.emit(DebugLogLevelDebug, fmt.Sprintf(format, args...))
}

func (l *DebugLogger) Info(args ...any) {
	l.emit(DebugLogLevelInfo, fmt.Sprint(args...))
}

func (l *DebugLogger) Infof(format string, args ...any) {
	l.emit(DebugLogLevelInfo, fmt.Sprintf(format, args...))
}

func (l *DebugLogger) Warn(args ...any) {
	l.emit(DebugLogLevelWarn, fmt.Sprint(args...))
}

func (l *DebugLogger) Warnf(format string, args ...any) {
	l.emit(DebugLogLevelWarn, fmt.Sprintf(format, args...))
}

func (l *DebugLogger) Err(args ...any) {
	l.emit(DebugLogLevelError, fmt.Sprint(args...))
}

func (l *DebugLogger) Errf(format string, args ...any) {
	l.emit(DebugLogLevelError, fmt.Sprintf(format, args...))
}

func (l *DebugLogger) Fatal(args ...any) {
	l.emit(DebugLogLevelFatal, fmt.Sprint(args...))
}

func (l *DebugLogger) Fatalf(format string, args ...any) {
	l.emit(DebugLogLevelFatal, fmt.Sprintf(format, args...))
}

func (l *DebugLogger) emit(level DebugLogLevel, message string) {
	if l == nil {
		return
	}
	entry := DebugLogEntry{
		At:      time.Now().UTC(),
		Level:   level,
		Source:  l.source,
		Message: message,
	}
	if l.factory != nil {
		l.factory.record(entry)
		return
	}
	if l.entries != nil {
		l.entries <- entry
	}
}
