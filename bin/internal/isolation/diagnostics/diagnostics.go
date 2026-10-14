// Package diagnostics implements bounded cross-process diagnostic logging for
// isolated roles. Direct mode does not create diagnostic streams.
package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	liblogging "github.com/asciimoth/almagest/lib/logging"
)

const (
	DefaultNormalCapacity   = 192
	DefaultPriorityCapacity = 64
	DefaultFlushTimeout     = 2 * time.Second
	MaxFlushTimeout         = 5 * time.Second
)

var ErrClosed = errors.New("diagnostic emitter is closed")

// Origin is fixed by trusted bootstrap for one process channel. Origin fields
// are not accepted from a record producer.
type Origin struct {
	Role            protocol.Role
	LaunchID        string
	ProcessInstance string
	SessionID       protocol.SessionID
}

func (o Origin) validate() error {
	if len(o.LaunchID) == 0 || len(o.LaunchID) > protocol.MaxDiagnosticSourceBytes ||
		len(o.ProcessInstance) == 0 || len(o.ProcessInstance) > protocol.MaxDiagnosticSourceBytes ||
		!o.SessionID.Valid() || containsUnsafe(o.LaunchID) || containsUnsafe(o.ProcessInstance) ||
		!utf8.ValidString(o.LaunchID) || !utf8.ValidString(o.ProcessInstance) {
		return protocol.ErrInvalidContract
	}
	switch o.Role {
	case protocol.RoleRuntime, protocol.RoleNetworkBroker, protocol.RoleConfigWriter, protocol.RoleCommandHelper:
		return nil
	default:
		return protocol.ErrInvalidContract
	}
}

// Correlation contains optional reviewed identifiers. It does not contain raw
// process metadata, packet data, config content, command slots, or secrets.
type Correlation struct {
	ResourceID         protocol.ResourceID
	ResourceGeneration uint64
	OperationID        string
	Operation          protocol.Operation
	Decision           string
	Outcome            protocol.Outcome
}

// Payload is the only record shape accepted from a child diagnostic stream.
// In particular, it has no role, process-instance, launch, or session fields.
type Payload struct {
	Timestamp          time.Time
	Sequence           uint64
	DroppedBefore      uint64
	Severity           protocol.Severity
	Source             string
	Message            string
	ResourceID         protocol.ResourceID
	ResourceGeneration uint64
	OperationID        string
	Operation          protocol.Operation
	Decision           string
	Outcome            protocol.Outcome
}

func (p Payload) validate() error {
	if err := p.validateWire(); err != nil {
		return err
	}
	if containsUnsafe(p.Source) || containsUnsafe(p.Message) || containsUnsafe(p.OperationID) || containsUnsafe(p.Decision) {
		return protocol.ErrInvalidContract
	}
	return nil
}

func (p Payload) validateWire() error {
	if p.Timestamp.IsZero() || p.Sequence == 0 || !validSeverity(p.Severity) ||
		len(p.Source) == 0 || len(p.Source) > protocol.MaxDiagnosticSourceBytes ||
		len(p.Message) > protocol.MaxDiagnosticMessageBytes || !utf8.ValidString(p.Source) ||
		!utf8.ValidString(p.Message) ||
		len(p.OperationID) > protocol.MaxOperationIDBytes || len(p.Decision) > protocol.MaxDiagnosticSourceBytes ||
		!utf8.ValidString(p.OperationID) || !utf8.ValidString(p.Decision) {
		return protocol.ErrInvalidContract
	}
	if p.ResourceID.Valid() != (p.ResourceGeneration != 0) {
		return protocol.ErrInvalidContract
	}
	if p.Operation != "" && !p.Operation.Valid() {
		return protocol.ErrInvalidContract
	}
	if p.Outcome != "" {
		switch p.Outcome {
		case protocol.OutcomeRejected, protocol.OutcomeCommitted, protocol.OutcomeCompensated, protocol.OutcomeUncertain:
		default:
			return protocol.ErrInvalidContract
		}
	}
	return nil
}

func (p Payload) record(origin Origin) protocol.DiagnosticRecord {
	return protocol.DiagnosticRecord{
		Timestamp: p.Timestamp, Role: origin.Role, LaunchID: origin.LaunchID,
		ProcessInstance: origin.ProcessInstance, SessionID: origin.SessionID,
		Sequence: p.Sequence, DroppedBefore: p.DroppedBefore, Severity: p.Severity,
		Source: p.Source, Message: p.Message, ResourceID: p.ResourceID,
		ResourceGeneration: p.ResourceGeneration, OperationID: p.OperationID,
		Operation: p.Operation, Decision: p.Decision, Outcome: p.Outcome,
	}
}

type WireSink interface {
	WritePayload(context.Context, Payload) error
}

type RecordSink interface {
	WriteRecord(context.Context, protocol.DiagnosticRecord) error
}

type Config struct {
	Origin           Origin
	MinimumSeverity  protocol.Severity
	NormalCapacity   int
	PriorityCapacity int
	FlushTimeout     time.Duration
	Forward          WireSink
	Operator         WireSink
	LocalOperator    RecordSink
	Secrets          []string
	Now              func() time.Time
}

// Emitter sends each record to independent admin-forwarding and operator
// queues. A blocked or failed destination cannot hold the logging caller or the
// other destination.
type Emitter struct {
	origin   Origin
	minimum  protocol.Severity
	redact   *Redactor
	now      func() time.Time
	flush    time.Duration
	seq      atomic.Uint64
	closed   atomic.Bool
	forward  *output
	operator *output
}

type output struct {
	queue *priorityQueue
	done  chan struct{}
	write func(context.Context, Payload) error
	stats outputCounters
}

type outputCounters struct {
	written  atomic.Uint64
	failures atomic.Uint64
}

type OutputStats struct {
	Queued        int
	Written       uint64
	WriteFailures uint64
	Dropped       map[protocol.Severity]uint64
}

type Stats struct {
	Forward  OutputStats
	Operator OutputStats
}

func New(cfg Config) (*Emitter, error) {
	if err := cfg.Origin.validate(); err != nil || !validSeverity(cfg.MinimumSeverity) {
		return nil, protocol.ErrInvalidContract
	}
	if cfg.Forward == nil && cfg.Operator == nil && cfg.LocalOperator == nil || cfg.Operator != nil && cfg.LocalOperator != nil {
		return nil, protocol.ErrInvalidContract
	}
	normal, priority := cfg.NormalCapacity, cfg.PriorityCapacity
	if normal == 0 {
		normal = DefaultNormalCapacity
	}
	if priority == 0 {
		priority = DefaultPriorityCapacity
	}
	if normal < 0 || priority < 1 || normal+priority > 4096 {
		return nil, protocol.ErrLimitExceeded
	}
	flush := cfg.FlushTimeout
	if flush == 0 {
		flush = DefaultFlushTimeout
	}
	if flush < 0 || flush > MaxFlushTimeout {
		return nil, protocol.ErrLimitExceeded
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	e := &Emitter{origin: cfg.Origin, minimum: cfg.MinimumSeverity, redact: NewRedactor(cfg.Secrets...), now: now, flush: flush}
	if cfg.Forward != nil {
		e.forward = newOutput(normal, priority, cfg.Forward.WritePayload)
	}
	if cfg.Operator != nil {
		e.operator = newOutput(normal, priority, cfg.Operator.WritePayload)
	}
	if cfg.LocalOperator != nil {
		e.operator = newOutput(normal, priority, func(ctx context.Context, p Payload) error {
			return cfg.LocalOperator.WriteRecord(ctx, p.record(cfg.Origin))
		})
	}
	return e, nil
}

func newOutput(normal, priority int, write func(context.Context, Payload) error) *output {
	o := &output{queue: newPriorityQueue(normal, priority), done: make(chan struct{}), write: write}
	go o.run()
	return o
}

func (o *output) run() {
	defer close(o.done)
	for {
		p, ok := o.queue.pop()
		if !ok {
			return
		}
		p.DroppedBefore += o.queue.takePendingDrops()
		if err := o.write(context.Background(), p); err != nil {
			o.stats.failures.Add(1)
			o.queue.markWriteFailure(p.Severity, p.DroppedBefore)
			continue
		}
		o.stats.written.Add(1)
	}
}

func (e *Emitter) Enabled(severity protocol.Severity) bool {
	return e != nil && validSeverity(severity) && severityRank(severity) >= severityRank(e.minimum) && !e.closed.Load()
}

func (e *Emitter) Emit(severity protocol.Severity, source, message string, correlation Correlation) error {
	if !e.Enabled(severity) {
		if e == nil || e.closed.Load() {
			return ErrClosed
		}
		return nil
	}
	message = Sanitize(e.redact.String(Sanitize(message, protocol.MaxDiagnosticMessageBytes)), protocol.MaxDiagnosticMessageBytes)
	source = Sanitize(source, protocol.MaxDiagnosticSourceBytes)
	p := Payload{
		Timestamp: e.now().UTC(), Sequence: e.seq.Add(1), Severity: severity,
		Source: source, Message: message, ResourceID: correlation.ResourceID,
		ResourceGeneration: correlation.ResourceGeneration, OperationID: correlation.OperationID,
		Operation: correlation.Operation, Decision: Sanitize(correlation.Decision, protocol.MaxDiagnosticSourceBytes),
		Outcome: correlation.Outcome,
	}
	if err := p.validate(); err != nil {
		return err
	}
	accepted := false
	for _, out := range []*output{e.forward, e.operator} {
		if out != nil && out.queue.push(p) {
			accepted = true
		}
	}
	if !accepted {
		return protocol.ErrLimitExceeded
	}
	return nil
}

func (e *Emitter) Logger(source string) liblogging.Logger { return &Logger{emitter: e, source: source} }

func (e *Emitter) Stats() Stats {
	return Stats{Forward: outputStats(e.forward), Operator: outputStats(e.operator)}
}

func outputStats(o *output) OutputStats {
	if o == nil {
		return OutputStats{Dropped: map[protocol.Severity]uint64{}}
	}
	return OutputStats{Queued: o.queue.len(), Written: o.stats.written.Load(), WriteFailures: o.stats.failures.Load(), Dropped: o.queue.dropSnapshot()}
}

// Close drains both queues up to the configured bound or the earlier caller
// deadline. It never waits indefinitely for a blocked destination.
func (e *Emitter) Close(ctx context.Context) error {
	if e == nil || !e.closed.CompareAndSwap(false, true) {
		return nil
	}
	for _, out := range []*output{e.forward, e.operator} {
		if out != nil {
			out.queue.close()
		}
	}
	deadline := time.Now().Add(e.flush)
	if caller, ok := ctx.Deadline(); ok && caller.Before(deadline) {
		deadline = caller
	}
	waitCtx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	for _, out := range []*output{e.forward, e.operator} {
		if out == nil {
			continue
		}
		select {
		case <-out.done:
		case <-waitCtx.Done():
			return waitCtx.Err()
		}
	}
	return nil
}

// Logger checks the threshold before it formats arguments.
type Logger struct {
	emitter *Emitter
	source  string
}

func (l *Logger) emit(level protocol.Severity, message string) {
	if l == nil || !l.emitter.Enabled(level) {
		return
	}
	_ = l.emitter.Emit(level, l.source, message, Correlation{})
}

func (l *Logger) formatted(level protocol.Severity, format string, args ...any) {
	if l == nil || !l.emitter.Enabled(level) {
		return
	}
	l.emit(level, fmt.Sprintf(format, args...))
}

func (l *Logger) unformatted(level protocol.Severity, args ...any) {
	if l == nil || !l.emitter.Enabled(level) {
		return
	}
	l.emit(level, fmt.Sprint(args...))
}

func (l *Logger) Debug(args ...any) { l.unformatted(protocol.SeverityDebug, args...) }
func (l *Logger) Debugf(format string, args ...any) {
	l.formatted(protocol.SeverityDebug, format, args...)
}
func (l *Logger) Info(args ...any) { l.unformatted(protocol.SeverityInfo, args...) }
func (l *Logger) Infof(format string, args ...any) {
	l.formatted(protocol.SeverityInfo, format, args...)
}
func (l *Logger) Warn(args ...any) { l.unformatted(protocol.SeverityWarn, args...) }
func (l *Logger) Warnf(format string, args ...any) {
	l.formatted(protocol.SeverityWarn, format, args...)
}
func (l *Logger) Err(args ...any) { l.unformatted(protocol.SeverityError, args...) }
func (l *Logger) Errf(format string, args ...any) {
	l.formatted(protocol.SeverityError, format, args...)
}
func (l *Logger) Fatal(args ...any) { l.unformatted(protocol.SeverityFatal, args...) }
func (l *Logger) Fatalf(format string, args ...any) {
	l.formatted(protocol.SeverityFatal, format, args...)
}

func validSeverity(s protocol.Severity) bool {
	return s == protocol.SeverityDebug || s == protocol.SeverityInfo || s == protocol.SeverityWarn || s == protocol.SeverityError || s == protocol.SeverityFatal
}

func severityRank(s protocol.Severity) int {
	switch s {
	case protocol.SeverityDebug:
		return 0
	case protocol.SeverityInfo:
		return 1
	case protocol.SeverityWarn:
		return 2
	case protocol.SeverityError:
		return 3
	case protocol.SeverityFatal:
		return 4
	default:
		return -1
	}
}
