package almagest

import (
	"fmt"
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

// DebugLogFactory creates channel-backed loggers for tests and debugging.
//
// Logging sends to Entries. Use a buffer large enough for the test, or drain the
// channel while the engine is running.
type DebugLogFactory struct {
	entries chan DebugLogEntry
}

// NewDebugLogFactory returns a LogFactory that records entries in a channel.
func NewDebugLogFactory(buffer int) *DebugLogFactory {
	if buffer < 0 {
		buffer = 0
	}
	return &DebugLogFactory{
		entries: make(chan DebugLogEntry, buffer),
	}
}

// Entries returns the channel used by loggers created from this factory.
func (f *DebugLogFactory) Entries() <-chan DebugLogEntry {
	if f == nil {
		return nil
	}
	return f.entries
}

// Drain removes and returns all entries currently buffered in Entries.
func (f *DebugLogFactory) Drain() []DebugLogEntry {
	if f == nil {
		return nil
	}
	var entries []DebugLogEntry
	for {
		select {
		case entry := <-f.entries:
			entries = append(entries, entry)
		default:
			return entries
		}
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
	return &DebugLogger{entries: f.entries, source: source}
}

// DebugLogger sends log entries to a channel.
type DebugLogger struct {
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
	if l == nil || l.entries == nil {
		return
	}
	l.entries <- DebugLogEntry{
		At:      time.Now().UTC(),
		Level:   level,
		Source:  l.source,
		Message: message,
	}
}
