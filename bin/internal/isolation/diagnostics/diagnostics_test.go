package diagnostics

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

func testOrigin(role protocol.Role) Origin {
	var session protocol.SessionID
	session[len(session)-1] = 1
	return Origin{Role: role, LaunchID: "launch-1", ProcessInstance: "instance-1", SessionID: session}
}

func testPayload(sequence uint64, severity protocol.Severity, message string) Payload {
	return Payload{Timestamp: time.Unix(1, 2).UTC(), Sequence: sequence, Severity: severity, Source: "test", Message: message}
}

type recordStore struct {
	mu      sync.Mutex
	records []protocol.DiagnosticRecord
	notify  chan struct{}
}

func newRecordStore() *recordStore { return &recordStore{notify: make(chan struct{}, 4096)} }

func (s *recordStore) WriteRecord(_ context.Context, record protocol.DiagnosticRecord) error {
	s.mu.Lock()
	s.records = append(s.records, record)
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
	return nil
}

func (s *recordStore) snapshot() []protocol.DiagnosticRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]protocol.DiagnosticRecord(nil), s.records...)
}

func waitRecords(t *testing.T, store *recordStore, count int) []protocol.DiagnosticRecord {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if records := store.snapshot(); len(records) >= count {
			return records
		}
		select {
		case <-store.notify:
		case <-deadline:
			t.Fatalf("received %d records, want %d", len(store.snapshot()), count)
		}
	}
}

func TestEmitterKeepsOperatorOutputIndependentFromStoppedRuntime(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	forward := WireSinkFunc(func(context.Context, Payload) error {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		return nil
	})
	operator := newRecordStore()
	emitter, err := New(Config{
		Origin: testOrigin(protocol.RoleNetworkBroker), MinimumSeverity: protocol.SeverityDebug,
		NormalCapacity: 2, PriorityCapacity: 2, FlushTimeout: 100 * time.Millisecond,
		Forward: forward, LocalOperator: operator,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := emitter.Emit(protocol.SeverityInfo, "network", "created", Correlation{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("forwarding sink did not block")
	}
	record := waitRecords(t, operator, 1)[0]
	if record.Role != protocol.RoleNetworkBroker || record.ProcessInstance != "instance-1" || record.Message != "created" {
		t.Fatalf("operator record = %+v", record)
	}
	close(release)
	if err := emitter.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestChildOperatorCollectorAssignsOrigin(t *testing.T) {
	left, right := net.Pipe()
	stream, err := NewStreamSink(left)
	if err != nil {
		t.Fatal(err)
	}
	emitterOrigin := testOrigin(protocol.RoleCommandHelper)
	emitterOrigin.ProcessInstance = "untrusted-local-label"
	emitter, err := New(Config{Origin: emitterOrigin, MinimumSeverity: protocol.SeverityInfo, Operator: stream})
	if err != nil {
		t.Fatal(err)
	}
	trustedOrigin := emitterOrigin
	trustedOrigin.ProcessInstance = "launcher-assigned"
	store := newRecordStore()
	collector, err := NewCollector(trustedOrigin, store)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- collector.Run(ctx, right) }()
	if err := emitter.Emit(protocol.SeverityInfo, "command", "started", Correlation{}); err != nil {
		t.Fatal(err)
	}
	record := waitRecords(t, store, 1)[0]
	if record.ProcessInstance != "launcher-assigned" {
		t.Fatalf("operator origin = %+v", record)
	}
	if err := emitter.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("operator collector did not stop")
	}
}

func TestEmitterPriorityBoundsDropsAndVisibleRecovery(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var got []Payload
	sink := WireSinkFunc(func(_ context.Context, payload Payload) error {
		if payload.Sequence == 1 {
			close(started)
			<-release
		}
		mu.Lock()
		got = append(got, payload)
		mu.Unlock()
		return nil
	})
	emitter, err := New(Config{Origin: testOrigin(protocol.RoleConfigWriter), MinimumSeverity: protocol.SeverityDebug, NormalCapacity: 2, PriorityCapacity: 2, Forward: sink})
	if err != nil {
		t.Fatal(err)
	}
	_ = emitter.Emit(protocol.SeverityDebug, "config", "block", Correlation{})
	<-started
	for i := range 100 {
		_ = emitter.Emit(protocol.SeverityDebug, "config", fmt.Sprintf("debug-%d", i), Correlation{})
	}
	_ = emitter.Emit(protocol.SeverityWarn, "config", "warning", Correlation{})
	_ = emitter.Emit(protocol.SeverityError, "config", "error", Correlation{})
	_ = emitter.Emit(protocol.SeverityInfo, "config", "audit", Correlation{Operation: protocol.OperationReplaceConfig, Decision: "allow", Outcome: protocol.OutcomeCommitted})
	stats := emitter.Stats().Forward
	if stats.Queued > 4 || stats.Dropped[protocol.SeverityDebug] == 0 {
		t.Fatalf("bounded stats = %+v", stats)
	}
	close(release)
	if err := emitter.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	foundWarning, foundError, foundAudit, visibleDrop := false, false, false, false
	for _, payload := range got {
		foundWarning = foundWarning || payload.Message == "warning"
		foundError = foundError || payload.Message == "error"
		foundAudit = foundAudit || payload.Message == "audit"
		visibleDrop = visibleDrop || payload.DroppedBefore != 0
	}
	if !foundWarning || !foundError || !foundAudit || !visibleDrop {
		t.Fatalf("records after overload = %+v", got)
	}
}

func TestEmitterBoundedFlushWithBlockedSink(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	emitter, err := New(Config{
		Origin: testOrigin(protocol.RoleCommandHelper), MinimumSeverity: protocol.SeverityInfo,
		FlushTimeout: 20 * time.Millisecond,
		Forward: WireSinkFunc(func(context.Context, Payload) error {
			close(started)
			<-release
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = emitter.Emit(protocol.SeverityInfo, "command", "blocked", Correlation{})
	<-started
	begin := time.Now()
	if err := emitter.Close(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close error = %v", err)
	}
	if elapsed := time.Since(begin); elapsed > 500*time.Millisecond {
		t.Fatalf("Close took %v", elapsed)
	}
	close(release)
}

func TestEmitterCountsWriteFailuresAsDrops(t *testing.T) {
	var calls atomic.Uint64
	emitter, err := New(Config{
		Origin: testOrigin(protocol.RoleNetworkBroker), MinimumSeverity: protocol.SeverityInfo,
		Forward: WireSinkFunc(func(context.Context, Payload) error { calls.Add(1); return errors.New("sink failed") }),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = emitter.Emit(protocol.SeverityInfo, "network", "lost", Correlation{})
	deadline := time.Now().Add(time.Second)
	for emitter.Stats().Forward.WriteFailures == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	stats := emitter.Stats().Forward
	if calls.Load() == 0 || stats.WriteFailures != 1 || stats.Dropped[protocol.SeverityInfo] != 1 {
		t.Fatalf("failure stats = %+v, calls = %d", stats, calls.Load())
	}
	if err := emitter.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type countedStringer struct{ calls atomic.Uint64 }

func (s *countedStringer) String() string { s.calls.Add(1); return "formatted" }

func TestLoggerFiltersBeforeFormattingAndFatalIsData(t *testing.T) {
	store := newRecordStore()
	emitter, err := New(Config{Origin: testOrigin(protocol.RoleNetworkBroker), MinimumSeverity: protocol.SeverityWarn, LocalOperator: store})
	if err != nil {
		t.Fatal(err)
	}
	value := &countedStringer{}
	logger := emitter.Logger("network")
	logger.Debug(value)
	logger.Infof("%s", value)
	if value.calls.Load() != 0 {
		t.Fatal("disabled records were formatted")
	}
	logger.Fatal("remote fatal")
	record := waitRecords(t, store, 1)[0]
	if record.Severity != protocol.SeverityFatal || record.Message != "remote fatal" {
		t.Fatalf("fatal record = %+v", record)
	}
	if err := emitter.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSanitizeRedactAndCapturePanic(t *testing.T) {
	store := newRecordStore()
	emitter, err := New(Config{
		Origin: testOrigin(protocol.RoleCommandHelper), MinimumSeverity: protocol.SeverityDebug,
		LocalOperator: store, Secrets: []string{"literal-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	writer := CaptureWriter{Emitter: emitter, Severity: protocol.SeverityError, Source: "stderr\x1b[31m"}
	input := "panic\npassword=hunter2 token: abc config={full body here} packet=[01 02] pid=123 literal-secret \x1b[2J\u202esecret"
	if n, err := writer.Write([]byte(input)); err != nil || n != len(input) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	record := waitRecords(t, store, 1)[0]
	for _, forbidden := range []string{"\n", "\x1b", "hunter2", "abc", "full body", "01 02", "123", "literal-secret", "\u202e"} {
		if strings.Contains(record.Source+record.Message, forbidden) {
			t.Fatalf("unsafe value %q in %+v", forbidden, record)
		}
	}
	if strings.Count(record.Message, "[REDACTED]") < 6 {
		t.Fatalf("message was not redacted: %q", record.Message)
	}
	_ = emitter.Close(context.Background())
}

func TestCollectorAssignsTrustedOriginTracksGapsAndAdaptsDisplay(t *testing.T) {
	store := newRecordStore()
	origin := testOrigin(protocol.RoleNetworkBroker)
	collector, err := NewCollector(origin, store, "broker-secret")
	if err != nil {
		t.Fatal(err)
	}
	first := testPayload(3, protocol.SeverityFatal, "fatal\n\x1b[31m token=bad broker-secret")
	first.Source = "network\nforged"
	first.DroppedBefore = 1
	if err := collector.Collect(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := testPayload(6, protocol.SeverityWarn, "continued")
	if err := collector.Collect(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	records := store.snapshot()
	if len(records) != 2 || records[0].Role != origin.Role || records[0].SessionID != origin.SessionID || records[1].DroppedBefore != 2 {
		t.Fatalf("collected records = %+v", records)
	}
	if strings.Contains(records[0].Message, "bad") || strings.Contains(records[0].Message, "broker-secret") || strings.ContainsAny(records[0].Message+records[0].Source, "\n\x1b") {
		t.Fatalf("receiver did not redact: %q", records[0].Message)
	}
	stats := collector.Stats()
	if stats.Received != 2 || stats.SequenceGaps != 2 || stats.ReportedDrops != 1 {
		t.Fatalf("collector stats = %+v", stats)
	}
	entry := DisplayEntry(records[0])
	if entry.Role != string(origin.Role) || entry.ProcessInstance != origin.ProcessInstance || entry.Level != "fatal" || entry.SessionID != origin.SessionID.String() {
		t.Fatalf("display entry = %+v", entry)
	}
	if err := collector.Collect(context.Background(), first); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatalf("replayed sequence error = %v", err)
	}
}

func TestFrameRoundTripStrictLimitsAndForgedOrigin(t *testing.T) {
	payload := testPayload(1, protocol.SeverityInfo, "safe")
	frame, err := Encode(payload)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeFrame(frame)
	if err != nil || decoded.Message != payload.Message || decoded.Timestamp != payload.Timestamp {
		t.Fatalf("DecodeFrame = %+v, %v", decoded, err)
	}
	for name, mutation := range map[string]func([]byte) []byte{
		"truncated": func(b []byte) []byte { return b[:len(b)-1] },
		"trailing":  func(b []byte) []byte { return append(b, 'x') },
		"oversized": func([]byte) []byte { return make([]byte, MaxFrameBytes+1) },
		"zero":      func(b []byte) []byte { binary.BigEndian.PutUint32(b, 0); return b },
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeFrame(mutation(bytes.Clone(frame))); err == nil {
				t.Fatal("malformed frame accepted")
			}
		})
	}
	body := append(bytes.TrimSuffix(frame[4:], []byte("}")), []byte(`,"Role":"network_broker"}`)...)
	forged := make([]byte, len(body)+4)
	binary.BigEndian.PutUint32(forged, uint32(len(body)))
	copy(forged[4:], body)
	if _, err := DecodeFrame(forged); err == nil {
		t.Fatal("producer-selected origin was accepted")
	}

	tooLarge := payload
	tooLarge.Message = strings.Repeat("x", protocol.MaxDiagnosticMessageBytes+1)
	if _, err := Encode(tooLarge); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatalf("oversized record error = %v", err)
	}
}

func TestStreamCollectorPeerFailureAndTextSafety(t *testing.T) {
	left, right := net.Pipe()
	sink, err := NewStreamSink(left)
	if err != nil {
		t.Fatal(err)
	}
	store := newRecordStore()
	collector, err := NewCollector(testOrigin(protocol.RoleConfigWriter), store)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- collector.Run(ctx, right) }()
	if err := sink.WritePayload(context.Background(), testPayload(1, protocol.SeverityInfo, "one")); err != nil {
		t.Fatal(err)
	}
	waitRecords(t, store, 1)
	_ = left.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("peer failure returned nil")
		}
	case <-time.After(time.Second):
		t.Fatal("collector did not detect peer failure")
	}

	var output bytes.Buffer
	textSink, _ := NewTextSink(&output)
	record := testPayload(1, protocol.SeverityFatal, "fatal\n\x1b[31m").record(testOrigin(protocol.RoleCommandHelper))
	if err := textSink.WriteRecord(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	line := output.String()
	if strings.Count(line, "\n") != 1 || strings.Contains(line, "\x1b") || !strings.Contains(line, `"severity":"fatal"`) {
		t.Fatalf("unsafe operator line = %q", line)
	}
	var parsed operatorRecord
	if err := json.Unmarshal(output.Bytes(), &parsed); err != nil || parsed.Role != string(protocol.RoleCommandHelper) || parsed.Message != "fatal??[31m" {
		t.Fatalf("operator JSON = %+v, %v", parsed, err)
	}
}

func TestCollectorCancellationInterruptsIdleRead(t *testing.T) {
	left, right := net.Pipe()
	defer func() { _ = left.Close() }()
	defer func() { _ = right.Close() }()
	collector, err := NewCollector(testOrigin(protocol.RoleConfigWriter), newRecordStore())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- collector.Run(ctx, right) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("idle read ignored cancellation")
	}
}

func TestInvalidConfigurationAndCorrelation(t *testing.T) {
	valid := Config{Origin: testOrigin(protocol.RoleRuntime), MinimumSeverity: protocol.SeverityInfo, LocalOperator: newRecordStore()}
	for name, edit := range map[string]func(*Config){
		"role":     func(c *Config) { c.Origin.Role = "forged" },
		"session":  func(c *Config) { c.Origin.SessionID = protocol.SessionID{} },
		"severity": func(c *Config) { c.MinimumSeverity = "trace" },
		"no sinks": func(c *Config) { c.LocalOperator = nil },
		"two operator sinks": func(c *Config) {
			c.Operator = WireSinkFunc(func(context.Context, Payload) error { return nil })
		},
		"capacity":    func(c *Config) { c.NormalCapacity = 4096; c.PriorityCapacity = 2 },
		"flush":       func(c *Config) { c.FlushTimeout = MaxFlushTimeout + time.Nanosecond },
		"no priority": func(c *Config) { c.PriorityCapacity = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := valid
			edit(&cfg)
			if emitter, err := New(cfg); err == nil || emitter != nil {
				t.Fatalf("New = %v, %v", emitter, err)
			}
		})
	}
	emitter, _ := New(valid)
	var resource protocol.ResourceID
	resource[0] = 1
	if err := emitter.Emit(protocol.SeverityInfo, "runtime", "bad", Correlation{ResourceID: resource}); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatalf("invalid correlation error = %v", err)
	}
	_ = emitter.Close(context.Background())
}

func FuzzDiagnosticFrame(f *testing.F) {
	seed, err := Encode(testPayload(1, protocol.SeverityInfo, "seed"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 2, '{', '}'})
	f.Fuzz(func(t *testing.T, frame []byte) {
		payload, err := DecodeFrame(frame)
		if err != nil {
			return
		}
		if payload.validateWire() != nil {
			t.Fatal("decoder returned invalid payload")
		}
		canonical, err := Encode(payload)
		if err != nil {
			if payload.validate() == nil {
				t.Fatal(err)
			}
			return
		}
		again, err := DecodeFrame(canonical)
		if err != nil || again.Sequence != payload.Sequence || again.Message != payload.Message {
			t.Fatalf("canonical round trip changed payload: %+v, %v", again, err)
		}
	})
}

func FuzzSanitizeAndRedact(f *testing.F) {
	f.Add("plain", 32)
	f.Add("secret=x\n\x1b[31m", 64)
	f.Add("\xff\xfe", 4)
	f.Add("İPASSWORD='two words'", 128)
	f.Fuzz(func(t *testing.T, value string, limit int) {
		if limit < 0 || limit > protocol.MaxDiagnosticMessageBytes {
			return
		}
		got := Sanitize(NewRedactor("known-secret").String(Sanitize(value+" known-secret", limit)), limit)
		if len(got) > limit || !utf8.ValidString(got) || containsUnsafe(got) || strings.Contains(got, "known-secret") {
			t.Fatalf("unsafe result %q for limit %d", got, limit)
		}
	})
}
