package main

import (
	"fmt"
	"github.com/asciimoth/almagest/lib/logging"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/pasta/pasta"
	"github.com/kataras/golog"
)

var (
	_ logging.Logger = &Logger{}
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
	level   logLevelEnabled
	mu      sync.RWMutex
	next    uint64
	subs    map[uint64]*logSubscriber
	cache   map[string]*logRing
	closed  bool
	dropped atomic.Uint64
}

const logSubscriberQueueSize = 64

// logSubscriber isolates a logging caller from one slow admin subscriber.
// Stopping a subscriber never waits for its callback. A callback can be
// blocked in an operating-system write that the log service does not own.
type logSubscriber struct {
	callback func(api.LogEntry)
	entries  chan api.LogEntry
	done     chan struct{}
	once     sync.Once
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
		subs:  map[uint64]*logSubscriber{},
		cache: cache,
	}
}

func (l *LogFactory) EngineLogger() logging.Logger {
	return &Logger{
		prefix: "[engine]",
		level:  l.level,
		logf:   l,
		source: logSource{name: "engine"},
	}
}

func (l *LogFactory) DaemonLogger() logging.Logger {
	return l.RuntimeLogger()
}

// RuntimeLogger returns the local logger for daemon runtime orchestration.
func (l *LogFactory) RuntimeLogger() logging.Logger {
	return &Logger{
		prefix: "[daemon]",
		level:  l.level,
		logf:   l,
		source: logSource{name: "daemon"},
	}
}

// NetworkLogger returns the local logger for host-network control.
func (l *LogFactory) NetworkLogger() logging.Logger {
	return l.roleLogger("network", "network")
}

// ConfigLogger returns the local logger for fixed-target config persistence.
func (l *LogFactory) ConfigLogger() logging.Logger {
	return l.roleLogger("config", "config")
}

// CommandLogger returns the local logger for allowed commands and callbacks.
func (l *LogFactory) CommandLogger() logging.Logger {
	// Command records historically use the daemon source in the admin API.
	// Keep that contract while the operator output gets a command role label.
	return l.roleLogger("command", "daemon")
}

func (l *LogFactory) roleLogger(role, source string) logging.Logger {
	return &Logger{
		prefix: "[" + role + "]",
		level:  l.level,
		logf:   l,
		source: logSource{name: source},
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
	message := sanitizeLogMessage(fmt.Sprint(args...))
	l.emit("debug", message)

	golog.Debug(l.prefix + " " + message)
}

func (l *Logger) Debugf(format string, args ...any) {
	if !l.level.debug {
		return
	}
	message := sanitizeLogMessage(fmt.Sprintf(format, args...))
	l.emit("debug", message)
	golog.Debug(l.prefix + " " + message)
}

func (l *Logger) Info(args ...any) {
	if !l.level.info {
		return
	}
	message := sanitizeLogMessage(fmt.Sprint(args...))
	l.emit("info", message)

	golog.Info(l.prefix + " " + message)
}

func (l *Logger) Infof(format string, args ...any) {
	if !l.level.info {
		return
	}
	message := sanitizeLogMessage(fmt.Sprintf(format, args...))
	l.emit("info", message)
	golog.Info(l.prefix + " " + message)
}

func (l *Logger) Warn(args ...any) {
	if !l.level.warn {
		return
	}
	message := sanitizeLogMessage(fmt.Sprint(args...))
	l.emit("warn", message)

	golog.Warn(l.prefix + " " + message)
}

func (l *Logger) Warnf(format string, args ...any) {
	if !l.level.warn {
		return
	}
	message := sanitizeLogMessage(fmt.Sprintf(format, args...))
	l.emit("warn", message)
	golog.Warn(l.prefix + " " + message)
}

func (l *Logger) Err(args ...any) {
	if !l.level.err {
		return
	}
	message := sanitizeLogMessage(fmt.Sprint(args...))
	l.emit("error", message)

	golog.Error(l.prefix + " " + message)
}

func (l *Logger) Errf(format string, args ...any) {
	if !l.level.err {
		return
	}
	message := sanitizeLogMessage(fmt.Sprintf(format, args...))
	l.emit("error", message)
	golog.Error(l.prefix + " " + message)
}

func (l *Logger) Fatal(args ...any) {
	if !l.level.fatal {
		os.Exit(1)
	}

	message := sanitizeLogMessage(fmt.Sprint(args...))
	l.emit("fatal", message)
	golog.Fatal(l.prefix + " " + message)
}

func (l *Logger) Fatalf(format string, args ...any) {
	if !l.level.fatal {
		os.Exit(1)
	}

	message := sanitizeLogMessage(fmt.Sprintf(format, args...))
	l.emit("fatal", message)
	golog.Fatal(l.prefix + " " + message)
}

func sanitizeLogMessage(message string) string {
	for i := 0; i < len(message); i++ {
		if message[i] < 0x20 || message[i] > 0x7e {
			return filterLogMessage(message, i)
		}
	}
	return message
}

func filterLogMessage(message string, firstUnsafe int) string {
	var out strings.Builder
	out.Grow(len(message))
	out.WriteString(message[:firstUnsafe])
	for i := firstUnsafe; i < len(message); i++ {
		if message[i] >= 0x20 && message[i] <= 0x7e {
			out.WriteByte(message[i])
		}
	}
	return out.String()
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
	if l.closed {
		l.mu.Unlock()
		return
	}
	for _, subscriber := range l.subs {
		select {
		case subscriber.entries <- entry:
		default:
			l.dropped.Add(1)
		}
	}
	l.mu.Unlock()
}

func (l *LogFactory) Subscribe(callback func(api.LogEntry)) (uint64, []api.LogEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || callback == nil {
		return 0, l.snapshotLocked()
	}
	l.next++
	id := l.next
	subscriber := &logSubscriber{
		callback: callback,
		entries:  make(chan api.LogEntry, logSubscriberQueueSize),
		done:     make(chan struct{}),
	}
	l.subs[id] = subscriber
	go subscriber.run()
	return id, l.snapshotLocked()
}

func (s *logSubscriber) run() {
	for {
		select {
		case <-s.done:
			return
		case entry := <-s.entries:
			s.callback(entry)
		}
	}
}

func (s *logSubscriber) stop() {
	s.once.Do(func() { close(s.done) })
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
	subscriber := l.subs[id]
	delete(l.subs, id)
	l.mu.Unlock()
	if subscriber != nil {
		subscriber.stop()
	}
}

// DroppedSubscriberEntries reports records discarded because a subscriber's
// bounded delivery queue was full.
func (l *LogFactory) DroppedSubscriberEntries() uint64 {
	return l.dropped.Load()
}

// Close stops log delivery. It does not wait for callbacks that are blocked in
// subscriber-owned I/O, so log shutdown cannot hold application shutdown open.
func (l *LogFactory) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	subscribers := make([]*logSubscriber, 0, len(l.subs))
	for id, subscriber := range l.subs {
		subscribers = append(subscribers, subscriber)
		delete(l.subs, id)
	}
	l.mu.Unlock()
	for _, subscriber := range subscribers {
		subscriber.stop()
	}
	return nil
}

// DeliverDisplayEntry puts an already-shaped diagnostic record in the local
// admin cache and subscriptions. It does not write to operator output and does
// not interpret a fatal severity as a lifecycle instruction.
func (l *LogFactory) DeliverDisplayEntry(entry api.LogEntry) {
	entry.Message = strings.TrimSpace(sanitizeLogMessage(entry.Message))
	l.append(entry)
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
