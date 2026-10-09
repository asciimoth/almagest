package protocol_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

func TestIdentityEncoding(t *testing.T) {
	for _, value := range []string{"", "1", strings.Repeat("0", 32), strings.Repeat("g", 32), strings.Repeat("1", 31), strings.Repeat("1", 33), " " + strings.Repeat("1", 31)} {
		if id, err := protocol.ParseSessionID(value); !errors.Is(err, protocol.ErrInvalidContract) || id.Valid() {
			t.Fatalf("session %q: %v, %v", value, id, err)
		}
		if id, err := protocol.ParseResourceID(value); !errors.Is(err, protocol.ErrInvalidContract) || id.Valid() {
			t.Fatalf("resource %q: %v, %v", value, id, err)
		}
	}
	for _, value := range []string{strings.Repeat("aB", 16), "00000000000000000000000000000001", "10000000000000000000000000000000"} {
		s, err := protocol.ParseSessionID(value)
		if err != nil || !s.Valid() || s.String() != strings.ToLower(value) {
			t.Fatalf("session round trip: %v, %v", s, err)
		}
		r, err := protocol.ParseResourceID(value)
		if err != nil || !r.Valid() || r.String() != s.String() {
			t.Fatalf("resource round trip: %v, %v", r, err)
		}
	}
	seen := make(map[string]bool)
	for range 128 {
		s, err := protocol.NewSessionID()
		if err != nil || !s.Valid() || seen[s.String()] {
			t.Fatalf("generated session: %v, %v", s, err)
		}
		seen[s.String()] = true
		r, err := protocol.NewResourceID()
		if err != nil || !r.Valid() || seen[r.String()] {
			t.Fatalf("generated resource: %v, %v", r, err)
		}
		seen[r.String()] = true
	}
}

func TestSessionAndRequestValidation(t *testing.T) {
	session := testSession(t)
	for _, tc := range []struct {
		name string
		edit func(*protocol.Session)
		want error
	}{
		{"valid", func(*protocol.Session) {}, nil},
		{"zero ID", func(s *protocol.Session) { s.ID = protocol.SessionID{} }, protocol.ErrInvalidContract},
		{"zero epoch", func(s *protocol.Session) { s.Epoch = 0 }, protocol.ErrInvalidContract},
		{"zero version", func(s *protocol.Session) { s.Version = 0 }, protocol.ErrUnsupportedVersion},
		{"future version", func(s *protocol.Session) { s.Version++ }, protocol.ErrUnsupportedVersion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := session
			tc.edit(&s)
			if err := s.Validate(); !errors.Is(err, tc.want) {
				t.Fatalf("session: %v, want %v", err, tc.want)
			}
			if err := testMutationMeta(session, "op").Validate(s, true); !errors.Is(err, tc.want) {
				t.Fatalf("request: %v, want %v", err, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		name     string
		edit     func(*protocol.RequestMeta)
		mutation bool
		want     error
	}{
		{"mutation", func(*protocol.RequestMeta) {}, true, nil},
		{"read", func(m *protocol.RequestMeta) { m.OperationID = "" }, false, nil},
		{"zero request", func(m *protocol.RequestMeta) { m.RequestID = 0 }, true, protocol.ErrInvalidContract},
		{"foreign session", func(m *protocol.RequestMeta) { m.SessionID = protocol.SessionID{} }, true, protocol.ErrForeignResource},
		{"stale session", func(m *protocol.RequestMeta) { m.SessionEpoch++ }, true, protocol.ErrForeignResource},
		{"zero graph", func(m *protocol.RequestMeta) { m.GraphEpoch = 0 }, true, protocol.ErrInvalidContract},
		{"missing operation", func(m *protocol.RequestMeta) { m.OperationID = "" }, true, protocol.ErrInvalidContract},
		{"read operation", func(*protocol.RequestMeta) {}, false, protocol.ErrInvalidContract},
		{"max operation", func(m *protocol.RequestMeta) { m.OperationID = strings.Repeat("x", protocol.MaxOperationIDBytes) }, true, nil},
		{"long operation", func(m *protocol.RequestMeta) { m.OperationID = strings.Repeat("x", protocol.MaxOperationIDBytes+1) }, true, protocol.ErrInvalidContract},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testMutationMeta(session, "op")
			tc.edit(&m)
			if err := m.Validate(session, tc.mutation); !errors.Is(err, tc.want) {
				t.Fatalf("%v, want %v", err, tc.want)
			}
		})
	}
}

func TestCapabilityAndOutcomeValidation(t *testing.T) {
	for _, state := range []protocol.CapabilityState{protocol.CapabilityUnknown, protocol.CapabilityUnsupported, protocol.CapabilityUnavailable, protocol.CapabilitySupported, "", "future"} {
		for _, allow := range []bool{false, true} {
			c := protocol.Capability{State: state, AttemptAllowed: allow}
			defined := state == protocol.CapabilityUnknown || state == protocol.CapabilityUnsupported || state == protocol.CapabilityUnavailable || state == protocol.CapabilitySupported
			blocked := state == protocol.CapabilityUnsupported || state == protocol.CapabilityUnavailable
			if got := c.Validate(); (got == nil) != (defined && (!blocked || !allow)) {
				t.Fatalf("%+v: %v", c, got)
			}
			if defined && c.CanAttempt() != (allow && !blocked) {
				t.Fatalf("attempt: %+v", c)
			}
		}
	}
	for _, outcome := range []protocol.Outcome{protocol.OutcomeRejected, protocol.OutcomeCommitted, protocol.OutcomeCompensated, protocol.OutcomeUncertain, "", "future"} {
		for _, size := range []int{0, 1, protocol.MaxOperationIDBytes, protocol.MaxOperationIDBytes + 1} {
			r := protocol.MutationResult{OperationID: strings.Repeat("x", size), Outcome: outcome}
			valid := size > 0 && size <= protocol.MaxOperationIDBytes && outcome != "" && outcome != "future"
			if err := r.Validate(); (err == nil) != valid {
				t.Fatalf("%+v: %v", r, err)
			}
		}
	}
}

func TestDiagnosticValidation(t *testing.T) {
	session := testSession(t)
	base := protocol.DiagnosticRecord{Timestamp: time.Unix(1, 0), Sequence: 1, ProcessInstance: "process", LaunchID: "launch", SessionID: session.ID, Role: protocol.RoleRuntime, Severity: protocol.SeverityInfo, Source: "test"}
	for _, role := range []protocol.Role{protocol.RoleRuntime, protocol.RoleNetworkBroker, protocol.RoleConfigWriter, protocol.RoleCommandHelper} {
		for _, severity := range []protocol.Severity{protocol.SeverityDebug, protocol.SeverityInfo, protocol.SeverityWarn, protocol.SeverityError, protocol.SeverityFatal} {
			r := base
			r.Role, r.Severity = role, severity
			r.Source = strings.Repeat("s", protocol.MaxDiagnosticSourceBytes)
			r.Message = strings.Repeat("m", protocol.MaxDiagnosticMessageBytes)
			r.OperationID = strings.Repeat("o", protocol.MaxOperationIDBytes)
			if err := r.Validate(); err != nil {
				t.Fatal(err)
			}
		}
	}
	for name, edit := range map[string]func(*protocol.DiagnosticRecord){
		"timestamp":    func(r *protocol.DiagnosticRecord) { r.Timestamp = time.Time{} },
		"sequence":     func(r *protocol.DiagnosticRecord) { r.Sequence = 0 },
		"session":      func(r *protocol.DiagnosticRecord) { r.SessionID = protocol.SessionID{} },
		"process":      func(r *protocol.DiagnosticRecord) { r.ProcessInstance = "" },
		"process ANSI": func(r *protocol.DiagnosticRecord) { r.ProcessInstance = "bad\x1b" },
		"launch":       func(r *protocol.DiagnosticRecord) { r.LaunchID = "" },
		"role":         func(r *protocol.DiagnosticRecord) { r.Role = "future" },
		"severity":     func(r *protocol.DiagnosticRecord) { r.Severity = "future" },
		"empty source": func(r *protocol.DiagnosticRecord) { r.Source = "" },
		"source limit": func(r *protocol.DiagnosticRecord) {
			r.Source = strings.Repeat("s", protocol.MaxDiagnosticSourceBytes+1)
		},
		"source LF":    func(r *protocol.DiagnosticRecord) { r.Source = "a\nb" },
		"source CR":    func(r *protocol.DiagnosticRecord) { r.Source = "a\rb" },
		"message ANSI": func(r *protocol.DiagnosticRecord) { r.Message = "a\x1bb" },
		"message limit": func(r *protocol.DiagnosticRecord) {
			r.Message = strings.Repeat("m", protocol.MaxDiagnosticMessageBytes+1)
		},
		"operation limit": func(r *protocol.DiagnosticRecord) {
			r.OperationID = strings.Repeat("o", protocol.MaxOperationIDBytes+1)
		},
		"operation ANSI":              func(r *protocol.DiagnosticRecord) { r.OperationID = "a\x1bb" },
		"unknown operation":           func(r *protocol.DiagnosticRecord) { r.Operation = "future" },
		"unknown outcome":             func(r *protocol.DiagnosticRecord) { r.Outcome = "future" },
		"generation without resource": func(r *protocol.DiagnosticRecord) { r.ResourceGeneration = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			r := base
			edit(&r)
			if err := r.Validate(); !errors.Is(err, protocol.ErrInvalidContract) {
				t.Fatal(err)
			}
		})
	}
}

func FuzzOpaqueIdentity(f *testing.F) {
	for _, s := range []string{"", strings.Repeat("0", 32), strings.Repeat("AB", 16), "00000000000000000000000000000001", strings.Repeat("g", 32)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, value string) {
		decoded, err := hex.DecodeString(value)
		valid := err == nil && len(decoded) == 16 && !bytes.Equal(decoded, make([]byte, 16))
		s, se := protocol.ParseSessionID(value)
		r, re := protocol.ParseResourceID(value)
		if (se == nil) != valid || (re == nil) != valid {
			t.Fatalf("parsers disagree with hex contract: %q", value)
		}
		if valid {
			if !s.Valid() || !r.Valid() || s.String() != strings.ToLower(value) || r.String() != s.String() {
				t.Fatal("identity changed")
			}
		} else if s.Valid() || r.Valid() || !errors.Is(se, protocol.ErrInvalidContract) || !errors.Is(re, protocol.ErrInvalidContract) {
			t.Fatal("invalid identity retained")
		}
	})
}

func FuzzRequestMetadata(f *testing.F) {
	f.Add(uint64(1), uint64(1), uint64(1), "op", true, false)
	f.Add(uint64(1), uint64(1), uint64(1), "", false, false)
	f.Fuzz(func(t *testing.T, request, epoch, graph uint64, operation string, mutation, foreign bool) {
		s := protocol.Session{ID: protocol.SessionID{1}, Epoch: 1, Version: protocol.Version}
		m := protocol.RequestMeta{RequestID: request, SessionID: s.ID, SessionEpoch: epoch, GraphEpoch: graph, OperationID: operation}
		if foreign {
			m.SessionID[0]++
		}
		valid := request != 0 && epoch == s.Epoch && graph != 0 && !foreign
		if mutation {
			valid = valid && len(operation) > 0 && len(operation) <= protocol.MaxOperationIDBytes
		} else {
			valid = valid && operation == ""
		}
		if err := m.Validate(s, mutation); (err == nil) != valid {
			t.Fatalf("%+v, mutation %v: %v", m, mutation, err)
		}
	})
}
