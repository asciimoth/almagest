package main

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/pasta/pasta"
	"github.com/kataras/golog"
)

var (
	_ lib.Logger = &Logger{}
)

// TODO: Improve performance
type Logger struct {
	prefix string
	level  logLevelEnabled
	logf   *LogFactory
	source logSource
}

type logLevelEnabled struct {
	debug bool
	info  bool
	warn  bool
	err   bool
	fatal bool
}

func newLogLevelEnabled(level golog.Level) logLevelEnabled {
	return logLevelEnabled{
		debug: level >= golog.DebugLevel,
		info:  level >= golog.InfoLevel,
		warn:  level >= golog.WarnLevel,
		err:   level >= golog.ErrorLevel,
		fatal: level >= golog.FatalLevel,
	}
}

type LogFactory struct {
	level logLevelEnabled
	mu    sync.RWMutex
	next  uint64
	subs  map[uint64]func(api.LogEntry)
	cache map[string]*logRing
}

type logSource struct {
	name      string
	nodeID    uint64
	nodeClass string
}

type logRing struct {
	entries []api.LogEntry
	next    int
	count   int
}

func NewLogFactory(cfg LogCacheConfig) *LogFactory {
	cache := map[string]*logRing{
		"debug": newLogRing(cfg.Debug),
		"info":  newLogRing(cfg.Info),
		"warn":  newLogRing(cfg.Warn),
		"error": newLogRing(cfg.Error),
		"fatal": newLogRing(cfg.Fatal),
	}
	return &LogFactory{
		level: newLogLevelEnabled(golog.Default.Level),
		subs:  map[uint64]func(api.LogEntry){},
		cache: cache,
	}
}

func (l *LogFactory) EngineLogger() lib.Logger {
	return &Logger{
		prefix: "[engine]",
		level:  l.level,
		logf:   l,
		source: logSource{name: "engine"},
	}
}

func (l *LogFactory) WorkspaceLogger() pasta.Logger {
	return &Logger{
		prefix: "[workspace]",
		level:  l.level,
		logf:   l,
		source: logSource{name: "workspace"},
	}
}

func (l *LogFactory) NodeLogger(id uint64, class string) pasta.Logger {
	return &Logger{
		prefix: fmt.Sprintf("[%s %d]", class, id),
		level:  l.level,
		logf:   l,
		source: logSource{name: "node", nodeID: id, nodeClass: class},
	}
}

func newLogRing(size int) *logRing {
	if size <= 0 {
		return &logRing{}
	}
	return &logRing{entries: make([]api.LogEntry, size)}
}

func (r *logRing) append(entry api.LogEntry) {
	if len(r.entries) == 0 {
		return
	}
	r.entries[r.next] = entry
	r.next = (r.next + 1) % len(r.entries)
	if r.count < len(r.entries) {
		r.count++
	}
}

func (r *logRing) ordered() []api.LogEntry {
	if r.count == 0 {
		return nil
	}
	out := make([]api.LogEntry, 0, r.count)
	start := 0
	if r.count == len(r.entries) {
		start = r.next
	}
	for i := 0; i < r.count; i++ {
		idx := (start + i) % len(r.entries)
		out = append(out, r.entries[idx])
	}
	return out
}

func (l *Logger) Debug(args ...any) {
	if !l.level.debug {
		return
	}
	l.emit("debug", fmt.Sprint(args...))

	args = append([]any{l.prefix}, args...)
	golog.Debug(args...)
}

func (l *Logger) Debugf(format string, args ...any) {
	if !l.level.debug {
		return
	}
	l.emit("debug", fmt.Sprintf(format, args...))
	golog.Debugf(l.prefix+" "+format, args...)
}

func (l *Logger) Info(args ...any) {
	if !l.level.info {
		return
	}
	l.emit("info", fmt.Sprint(args...))

	args = append([]any{l.prefix}, args...)
	golog.Info(args...)
}

func (l *Logger) Infof(format string, args ...any) {
	if !l.level.info {
		return
	}
	l.emit("info", fmt.Sprintf(format, args...))
	golog.Infof(l.prefix+" "+format, args...)
}

func (l *Logger) Warn(args ...any) {
	if !l.level.warn {
		return
	}
	l.emit("warn", fmt.Sprint(args...))

	args = append([]any{l.prefix}, args...)
	golog.Warn(args...)
}

func (l *Logger) Warnf(format string, args ...any) {
	if !l.level.warn {
		return
	}
	l.emit("warn", fmt.Sprintf(format, args...))
	golog.Warnf(l.prefix+" "+format, args...)
}

func (l *Logger) Err(args ...any) {
	if !l.level.err {
		return
	}
	l.emit("error", fmt.Sprint(args...))

	args = append([]any{l.prefix}, args...)
	golog.Error(args...)
}

func (l *Logger) Errf(format string, args ...any) {
	if !l.level.err {
		return
	}
	l.emit("error", fmt.Sprintf(format, args...))
	golog.Errorf(l.prefix+" "+format, args...)
}

func (l *Logger) Fatal(args ...any) {
	l.emit("fatal", fmt.Sprint(args...))
	if !l.level.fatal {
		os.Exit(1)
	}

	args = append([]any{l.prefix}, args...)
	golog.Fatal(args...)
}

func (l *Logger) Fatalf(format string, args ...any) {
	l.emit("fatal", fmt.Sprintf(format, args...))
	if !l.level.fatal {
		os.Exit(1)
	}

	golog.Fatalf(l.prefix+" "+format, args...)
}

func (l *Logger) emit(level, message string) {
	if l.logf == nil {
		return
	}
	l.logf.append(api.LogEntry{
		At:        time.Now().UTC().Format(time.RFC3339Nano),
		Level:     level,
		Source:    l.source.name,
		Message:   strings.TrimSpace(message),
		NodeID:    l.source.nodeID,
		NodeClass: l.source.nodeClass,
	})
}

func (l *LogFactory) append(entry api.LogEntry) {
	l.mu.Lock()
	if ring := l.cache[entry.Level]; ring != nil {
		ring.append(entry)
	}
	callbacks := make([]func(api.LogEntry), 0, len(l.subs))
	for _, callback := range l.subs {
		callbacks = append(callbacks, callback)
	}
	l.mu.Unlock()

	for _, callback := range callbacks {
		callback(entry)
	}
}

func (l *LogFactory) Subscribe(callback func(api.LogEntry)) (uint64, []api.LogEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.next++
	id := l.next
	l.subs[id] = callback
	return id, l.snapshotLocked()
}

func (l *LogFactory) TotalCacheSize() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	total := 0
	for _, ring := range l.cache {
		if ring == nil {
			continue
		}
		total += len(ring.entries)
	}
	return total
}

func (l *LogFactory) Unsubscribe(id uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.subs, id)
}

func (l *LogFactory) snapshotLocked() []api.LogEntry {
	out := make([]api.LogEntry, 0)
	for _, level := range []string{"debug", "info", "warn", "error", "fatal"} {
		ring := l.cache[level]
		if ring == nil {
			continue
		}
		out = append(out, ring.ordered()...)
	}
	slices.SortStableFunc(out, func(a, b api.LogEntry) int {
		return strings.Compare(a.At, b.At)
	})
	return out
}
