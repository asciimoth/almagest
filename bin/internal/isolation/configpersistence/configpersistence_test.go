package configpersistence

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/almagestd/daemonservices"
	"github.com/asciimoth/almagest/bin/internal/isolation/configwriter"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

type fakePeer struct {
	call    func(context.Context, protocol.Operation, any, any) error
	send    func(context.Context, uint64, []byte) error
	configs chan transport.ConfigTransfer
	done    chan struct{}
	err     error
}

func (p *fakePeer) Call(ctx context.Context, op protocol.Operation, request, result any) error {
	return p.call(ctx, op, request, result)
}
func (p *fakePeer) SendConfig(ctx context.Context, id uint64, data []byte) error {
	return p.send(ctx, id, data)
}
func (p *fakePeer) Configs() <-chan transport.ConfigTransfer { return p.configs }
func (p *fakePeer) Done() <-chan struct{}                    { return p.done }
func (p *fakePeer) Err() error                               { return p.err }

func testSession() protocol.Session {
	return protocol.Session{ID: protocol.SessionID{1}, Epoch: 1, Version: protocol.Version}
}

func TestPersistenceEndToEnd(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.hujson")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	writer, err := configwriter.New(testSession(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	a, z := net.Pipe()
	b, y := net.Pipe()
	c, x := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- writer.Serve(ctx, transport.Lanes{Control: z, Data: y, Diagnostic: x}) }()
	remote, err := transport.New(ctx, transport.Lanes{Control: a, Data: b, Diagnostic: c}, testSession(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = remote.Close() })
	persistence, err := New(remote, testSession(), 1, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	document, err := persistence.ReadCurrent()
	if err != nil || !bytes.Equal(document.Data, []byte("before")) {
		t.Fatalf("ReadCurrent() = %q, %v", document.Data, err)
	}
	result := persistence.Replace(document.Revision, "operation-1", []byte("after"))
	if err := result.Err(); err != nil || result.Outcome != daemonservices.ConfigOutcomeCommitted {
		t.Fatalf("Replace() = %+v, %v", result, err)
	}
	receipt, err := persistence.GetResult("operation-1")
	if err != nil || receipt != result {
		t.Fatalf("GetResult() = %+v, %v", receipt, err)
	}
	conflict := persistence.Replace(document.Revision, "operation-2", []byte("stale"))
	if !errors.Is(conflict.Err(), daemonservices.ErrConfigRevisionConflict) || conflict.Revision != result.Revision {
		t.Fatalf("conflict = %+v, %v", conflict, conflict.Err())
	}
	written, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(written, []byte("after")) {
		t.Fatalf("target = %q, %v", written, err)
	}
}

func TestPersistenceRecoversLostReplaceReplyWithoutReplay(t *testing.T) {
	var sends, replacements, queries int
	peer := &fakePeer{configs: make(chan transport.ConfigTransfer), done: make(chan struct{})}
	peer.send = func(context.Context, uint64, []byte) error { sends++; return nil }
	peer.call = func(_ context.Context, op protocol.Operation, request, output any) error {
		switch op {
		case protocol.OperationReplaceConfig:
			replacements++
			return context.DeadlineExceeded
		case protocol.OperationGetOperationResult:
			queries++
			if queries < 3 {
				return protocol.ErrOperationUnknown
			}
			operation := request.(protocol.OperationResultRequest).OperationID
			*output.(*protocol.ConfigResult) = protocol.ConfigResult{
				MutationResult: protocol.MutationResult{OperationID: operation, Outcome: protocol.OutcomeCommitted},
				Revision:       protocol.ConfigRevision([]byte("new")),
			}
			return nil
		default:
			return protocol.ErrUnsupportedOperation
		}
	}
	persistence, err := newPersistence(peer, testSession(), 1, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	result := persistence.Replace(protocol.ConfigRevision([]byte("old")), "lost-reply", []byte("new"))
	if err := result.Err(); err != nil || result.Outcome != daemonservices.ConfigOutcomeCommitted {
		t.Fatalf("Replace() = %+v, %v", result, err)
	}
	if sends != 1 || replacements != 1 || queries != 3 {
		t.Fatalf("calls = sends %d replacements %d queries %d", sends, replacements, queries)
	}
}

func TestPersistenceReportsRecoveryWhenWriterDiesAfterTransfer(t *testing.T) {
	done := make(chan struct{})
	close(done)
	peer := &fakePeer{
		configs: make(chan transport.ConfigTransfer), done: done, err: ioErr("writer died"),
		send: func(context.Context, uint64, []byte) error { return nil },
		call: func(_ context.Context, op protocol.Operation, _, _ any) error {
			if op == protocol.OperationReplaceConfig {
				return ioErr("reply lost")
			}
			return ioErr("writer died")
		},
	}
	persistence, err := newPersistence(peer, testSession(), 1, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	result := persistence.Replace("old", "writer-death", []byte("new"))
	if result.Outcome != daemonservices.ConfigOutcomeUncertain || !errors.Is(result.Err(), daemonservices.ErrConfigRecoveryRequired) {
		t.Fatalf("Replace() = %+v, %v", result, result.Err())
	}
}

func TestPersistenceRejectsTransferAndReadBoundaryFailures(t *testing.T) {
	t.Run("transfer", func(t *testing.T) {
		calls := 0
		peer := &fakePeer{
			configs: make(chan transport.ConfigTransfer), done: make(chan struct{}),
			send: func(context.Context, uint64, []byte) error { return ioErr("transfer failed") },
			call: func(context.Context, protocol.Operation, any, any) error { calls++; return nil },
		}
		persistence, _ := newPersistence(peer, testSession(), 1, time.Second)
		result := persistence.Replace("old", "transfer", []byte("new"))
		if result.Outcome != daemonservices.ConfigOutcomeRejected || result.Err() == nil || calls != 0 {
			t.Fatalf("Replace() = %+v, calls %d", result, calls)
		}
	})
	t.Run("mismatched read transfer", func(t *testing.T) {
		configs := make(chan transport.ConfigTransfer, 1)
		configs <- transport.ConfigTransfer{Transfer: 2, Data: []byte("data")}
		peer := &fakePeer{
			configs: configs, done: make(chan struct{}), send: func(context.Context, uint64, []byte) error { return nil },
			call: func(_ context.Context, op protocol.Operation, _ any, output any) error {
				if op != protocol.OperationReadConfig {
					return protocol.ErrUnsupportedOperation
				}
				*output.(*protocol.ConfigDocument) = protocol.ConfigDocument{Transfer: 1, Revision: protocol.ConfigRevision([]byte("data"))}
				return nil
			},
		}
		persistence, _ := newPersistence(peer, testSession(), 1, time.Second)
		if _, err := persistence.ReadCurrent(); !errors.Is(err, protocol.ErrInvalidContract) {
			t.Fatalf("ReadCurrent() error = %v", err)
		}
	})
}

func TestPersistenceSerializesConcurrentTransfers(t *testing.T) {
	var mu sync.Mutex
	active, maximum := 0, 0
	peer := &fakePeer{configs: make(chan transport.ConfigTransfer), done: make(chan struct{})}
	peer.send = func(context.Context, uint64, []byte) error {
		mu.Lock()
		active++
		maximum = max(maximum, active)
		mu.Unlock()
		time.Sleep(time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()
		return nil
	}
	peer.call = func(_ context.Context, op protocol.Operation, request, output any) error {
		if op != protocol.OperationReplaceConfig {
			return protocol.ErrUnsupportedOperation
		}
		req := request.(protocol.ReplaceConfigRequest)
		*output.(*protocol.ConfigResult) = protocol.ConfigResult{MutationResult: protocol.MutationResult{OperationID: req.Meta.OperationID, Outcome: protocol.OutcomeCommitted}, Revision: req.ExpectedRevision}
		return nil
	}
	persistence, _ := newPersistence(peer, testSession(), 1, time.Second)
	var group sync.WaitGroup
	for i := range 32 {
		group.Add(1)
		go func() {
			defer group.Done()
			result := persistence.Replace("revision", "concurrent-"+string(rune('A'+i)), []byte("data"))
			if result.Err() != nil {
				t.Errorf("Replace() = %+v", result)
			}
		}()
	}
	group.Wait()
	if maximum != 1 {
		t.Fatalf("maximum concurrent transfers = %d, want 1", maximum)
	}
}

type ioErr string

func (e ioErr) Error() string { return string(e) }

func FuzzConvertConfigResult(f *testing.F) {
	f.Add("operation", "committed", "", "revision")
	f.Add("operation", "uncertain", "recovery", "revision")
	f.Add("operation", "rejected", "conflict", "revision")
	f.Fuzz(func(t *testing.T, operation, outcome, code, revision string) {
		if operation == "" || len(operation) > protocol.MaxOperationIDBytes {
			t.Skip()
		}
		result := convertResult(protocol.ConfigResult{
			MutationResult: protocol.MutationResult{OperationID: operation, Outcome: protocol.Outcome(outcome), Code: code},
			Revision:       revision,
		})
		if result.OperationID != operation || result.Revision != revision {
			t.Fatalf("conversion lost identity: %+v", result)
		}
		if result.Outcome == daemonservices.ConfigOutcomeCommitted && result.Err() != nil {
			t.Fatalf("committed result has error: %+v", result)
		}
		if result.Outcome != daemonservices.ConfigOutcomeCommitted && result.Err() == nil {
			t.Fatalf("non-committed result has no error: %+v", result)
		}
	})
}
