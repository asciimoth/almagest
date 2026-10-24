package webdemo

import (
	"fmt"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/pasta/pasta"
)

// BrowserLogSink writes one entry to the browser console.
type BrowserLogSink func(api.LogEntry)

// LogFactory is a browser logger with a bounded API cache.
type LogFactory struct {
	mu      sync.Mutex
	limit   int
	entries []api.LogEntry
	sink    BrowserLogSink
	nextID  uint64
	subs    map[uint64]func(api.LogEntry)
}

func NewLogFactory(cacheSize int, sink BrowserLogSink) *LogFactory {
	if cacheSize < 1 {
		cacheSize = 1
	}
	return &LogFactory{limit: cacheSize, sink: sink, subs: map[uint64]func(api.LogEntry){}}
}

func (f *LogFactory) EngineLogger() logging.Logger  { return f.logger("engine", 0, "") }
func (f *LogFactory) WorkspaceLogger() pasta.Logger { return f.logger("workspace", 0, "") }
func (f *LogFactory) NodeLogger(id uint64, class string) pasta.Logger {
	return f.logger("node", id, class)
}

func (f *LogFactory) logger(source string, nodeID uint64, class string) *browserLogger {
	return &browserLogger{factory: f, source: source, nodeID: nodeID, nodeClass: class}
}

func (f *LogFactory) record(entry api.LogEntry) {
	f.mu.Lock()
	if len(f.entries) == f.limit {
		copy(f.entries, f.entries[1:])
		f.entries[len(f.entries)-1] = entry
	} else {
		f.entries = append(f.entries, entry)
	}
	subs := make([]func(api.LogEntry), 0, len(f.subs))
	for _, subscriber := range f.subs {
		subs = append(subs, subscriber)
	}
	f.mu.Unlock()
	if f.sink != nil {
		f.sink(entry)
	}
	for _, subscriber := range subs {
		subscriber(entry)
	}
}

func (f *LogFactory) Snapshot() []api.LogEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append(make([]api.LogEntry, 0, len(f.entries)), f.entries...)
}

func (f *LogFactory) CacheSize() int { return f.limit }

func (f *LogFactory) Subscribe(callback func(api.LogEntry)) (uint64, []api.LogEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	f.subs[f.nextID] = callback
	return f.nextID, append(make([]api.LogEntry, 0, len(f.entries)), f.entries...)
}

func (f *LogFactory) Unsubscribe(id uint64) { f.mu.Lock(); delete(f.subs, id); f.mu.Unlock() }

type browserLogger struct {
	factory   *LogFactory
	source    string
	nodeID    uint64
	nodeClass string
}

func (l *browserLogger) emit(level string, message string) {
	l.factory.record(api.LogEntry{
		At: time.Now().UTC().Format(time.RFC3339Nano), Level: level, Source: l.source,
		Message: message, NodeID: l.nodeID, NodeClass: l.nodeClass,
	})
}

func (l *browserLogger) Debug(args ...any) { l.emit("debug", fmt.Sprint(args...)) }
func (l *browserLogger) Debugf(format string, args ...any) {
	l.emit("debug", fmt.Sprintf(format, args...))
}
func (l *browserLogger) Info(args ...any) { l.emit("info", fmt.Sprint(args...)) }
func (l *browserLogger) Infof(format string, args ...any) {
	l.emit("info", fmt.Sprintf(format, args...))
}
func (l *browserLogger) Warn(args ...any) { l.emit("warn", fmt.Sprint(args...)) }
func (l *browserLogger) Warnf(format string, args ...any) {
	l.emit("warn", fmt.Sprintf(format, args...))
}
func (l *browserLogger) Err(args ...any) { l.emit("error", fmt.Sprint(args...)) }
func (l *browserLogger) Errf(format string, args ...any) {
	l.emit("error", fmt.Sprintf(format, args...))
}
func (l *browserLogger) Fatal(args ...any) { l.emit("fatal", fmt.Sprint(args...)) }
func (l *browserLogger) Fatalf(format string, args ...any) {
	l.emit("fatal", fmt.Sprintf(format, args...))
}

var _ logging.LogFactory = (*LogFactory)(nil)
