package diagnostics

import (
	"context"
	"encoding/json"
	"io"
	"sync"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

type RecordSinkFunc func(context.Context, protocol.DiagnosticRecord) error

func (f RecordSinkFunc) WriteRecord(ctx context.Context, record protocol.DiagnosticRecord) error {
	return f(ctx, record)
}

type WireSinkFunc func(context.Context, Payload) error

func (f WireSinkFunc) WritePayload(ctx context.Context, payload Payload) error {
	return f(ctx, payload)
}

// DisplayEntry preserves trusted process identity and request correlation in
// the existing runtime-owned admin cache shape.
func DisplayEntry(record protocol.DiagnosticRecord) api.LogEntry {
	resource := ""
	if record.ResourceID.Valid() {
		resource = record.ResourceID.String()
	}
	session := ""
	if record.SessionID.Valid() {
		session = record.SessionID.String()
	}
	return api.LogEntry{
		At:    record.Timestamp.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		Level: string(record.Severity), Source: Sanitize(record.Source, protocol.MaxDiagnosticSourceBytes), Message: Sanitize(record.Message, protocol.MaxDiagnosticMessageBytes),
		Role: string(record.Role), LaunchID: Sanitize(record.LaunchID, protocol.MaxDiagnosticSourceBytes), ProcessInstance: Sanitize(record.ProcessInstance, protocol.MaxDiagnosticSourceBytes),
		Sequence: record.Sequence, DroppedBefore: record.DroppedBefore, SessionID: session,
		ResourceID: resource, ResourceGeneration: record.ResourceGeneration,
		OperationID: Sanitize(record.OperationID, protocol.MaxOperationIDBytes), Operation: string(record.Operation),
		Decision: Sanitize(record.Decision, protocol.MaxDiagnosticSourceBytes), Outcome: string(record.Outcome),
	}
}

// TextSink writes one sanitized line per record. It never interprets fatal
// severity as a request to exit.
type TextSink struct {
	mu sync.Mutex
	w  io.Writer
}

func NewTextSink(w io.Writer) (*TextSink, error) {
	if w == nil {
		return nil, protocol.ErrInvalidContract
	}
	return &TextSink{w: w}, nil
}

func (s *TextSink) WriteRecord(_ context.Context, record protocol.DiagnosticRecord) error {
	resource := ""
	if record.ResourceID.Valid() {
		resource = record.ResourceID.String()
	}
	line, err := json.Marshal(operatorRecord{
		Timestamp: record.Timestamp.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		Role:      string(record.Role), LaunchID: Sanitize(record.LaunchID, protocol.MaxDiagnosticSourceBytes),
		ProcessInstance: Sanitize(record.ProcessInstance, protocol.MaxDiagnosticSourceBytes),
		SessionID:       record.SessionID.String(), Sequence: record.Sequence, DroppedBefore: record.DroppedBefore,
		Severity: string(record.Severity), Source: Sanitize(record.Source, protocol.MaxDiagnosticSourceBytes),
		Message: Sanitize(record.Message, protocol.MaxDiagnosticMessageBytes), ResourceID: resource,
		ResourceGeneration: record.ResourceGeneration, OperationID: Sanitize(record.OperationID, protocol.MaxOperationIDBytes),
		Operation: string(record.Operation), Decision: Sanitize(record.Decision, protocol.MaxDiagnosticSourceBytes), Outcome: string(record.Outcome),
	})
	if err != nil {
		return err
	}
	line = append(line, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.w.Write(line)
	return err
}

type operatorRecord struct {
	Timestamp          string `json:"timestamp"`
	Role               string `json:"role"`
	LaunchID           string `json:"launch_id"`
	ProcessInstance    string `json:"process_instance"`
	SessionID          string `json:"session_id"`
	Sequence           uint64 `json:"sequence"`
	DroppedBefore      uint64 `json:"dropped_before,omitempty"`
	Severity           string `json:"severity"`
	Source             string `json:"source"`
	Message            string `json:"message"`
	ResourceID         string `json:"resource_id,omitempty"`
	ResourceGeneration uint64 `json:"resource_generation,omitempty"`
	OperationID        string `json:"operation_id,omitempty"`
	Operation          string `json:"operation,omitempty"`
	Decision           string `json:"decision,omitempty"`
	Outcome            string `json:"outcome,omitempty"`
}

// CaptureWriter converts bounded stderr or panic writes into attributed
// records. It retains no input and always reports the input as consumed.
type CaptureWriter struct {
	Emitter  *Emitter
	Severity protocol.Severity
	Source   string
}

func (w CaptureWriter) Write(data []byte) (int, error) {
	if w.Emitter != nil {
		_ = w.Emitter.Emit(w.Severity, w.Source, string(data), Correlation{})
	}
	return len(data), nil
}
