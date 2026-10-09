package configwriter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

type fakeTarget struct {
	data       []byte
	readErr    error
	replaceErr error
	committed  bool
	writes     int
}

func (f *fakeTarget) read(limit int) (targetSnapshot, error) {
	if f.readErr != nil {
		return targetSnapshot{}, f.readErr
	}
	if len(f.data) > limit {
		return targetSnapshot{}, protocol.ErrLimitExceeded
	}
	return targetSnapshot{data: bytes.Clone(f.data)}, nil
}

func (f *fakeTarget) replace(ctx context.Context, _ fileToken, data []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	f.writes++
	if f.committed {
		f.data = bytes.Clone(data)
	}
	if f.replaceErr != nil {
		return f.committed, f.replaceErr
	}
	f.data = bytes.Clone(data)
	return true, nil
}

func (*fakeTarget) close() error { return nil }

func testSession() protocol.Session {
	return protocol.Session{ID: protocol.SessionID{1}, Epoch: 1, Version: protocol.Version}
}

func testMeta(operation string) protocol.RequestMeta {
	return protocol.RequestMeta{RequestID: 1, SessionID: testSession().ID, SessionEpoch: 1, GraphEpoch: 1, OperationID: operation}
}

func fakeWriter(target *fakeTarget) *Writer {
	return &Writer{session: testSession(), target: target, receipts: make(map[string]receipt)}
}

func TestWriterCompareReplaceReceiptsAndOpaqueBytes(t *testing.T) {
	target := &fakeTarget{data: []byte("before")}
	w := fakeWriter(target)
	document, err := w.ReadCurrent(context.Background(), protocol.ReadConfigRequest{Meta: testMeta("")})
	if err != nil || document.Revision != protocol.ConfigRevision([]byte("before")) {
		t.Fatalf("read = %+v, %v", document, err)
	}
	data := []byte{0, 0xff, '{', '\n'}
	req := protocol.ReplaceConfigRequest{Meta: testMeta("write-1"), ExpectedRevision: document.Revision, Data: data}
	result, err := w.Replace(context.Background(), req)
	if err != nil || result.Outcome != protocol.OutcomeCommitted || result.Revision != protocol.ConfigRevision(data) {
		t.Fatalf("replace = %+v, %v", result, err)
	}
	retry, err := w.Replace(context.Background(), req)
	if err != nil || retry != result || target.writes != 1 {
		t.Fatalf("retry = %+v, %v; writes = %d", retry, err, target.writes)
	}
	receipt, err := w.GetResult(context.Background(), protocol.OperationResultRequest{Meta: testMeta(""), OperationID: "write-1"})
	if err != nil || receipt != result {
		t.Fatalf("receipt = %+v, %v", receipt, err)
	}
	if !bytes.Equal(target.data, data) {
		t.Fatalf("target = %x", target.data)
	}
}

func TestWriterConflictsLimitsAndAuthorization(t *testing.T) {
	target := &fakeTarget{data: []byte("current")}
	w := fakeWriter(target)
	stale, err := w.Replace(context.Background(), protocol.ReplaceConfigRequest{
		Meta: testMeta("stale"), ExpectedRevision: protocol.ConfigRevision([]byte("old")), Data: []byte("new"),
	})
	if !errors.Is(err, protocol.ErrOperationConflict) || stale.Code != "conflict" || stale.Revision != protocol.ConfigRevision(target.data) || target.writes != 0 {
		t.Fatalf("stale = %+v, %v", stale, err)
	}
	reused, err := w.Replace(context.Background(), protocol.ReplaceConfigRequest{
		Meta: testMeta("stale"), ExpectedRevision: stale.Revision, Data: []byte("different"),
	})
	if !errors.Is(err, protocol.ErrOperationConflict) || reused.Code != "conflict" || target.writes != 0 {
		t.Fatalf("reuse = %+v, %v", reused, err)
	}
	large := make([]byte, protocol.MaxConfigBytes+1)
	limited, err := w.Replace(context.Background(), protocol.ReplaceConfigRequest{Meta: testMeta("large"), Data: large})
	if !errors.Is(err, protocol.ErrLimitExceeded) || limited.Code != "limit" {
		t.Fatalf("large = %+v, %v", limited, err)
	}
	foreign := testMeta("")
	foreign.SessionID = protocol.SessionID{2}
	if _, err := w.ReadCurrent(context.Background(), protocol.ReadConfigRequest{Meta: foreign}); !errors.Is(err, protocol.ErrForeignResource) {
		t.Fatalf("foreign read = %v", err)
	}
	if _, err := w.GetResult(context.Background(), protocol.OperationResultRequest{Meta: testMeta(""), OperationID: "missing"}); !errors.Is(err, protocol.ErrOperationUnknown) {
		t.Fatalf("missing receipt = %v", err)
	}
}

func TestWriterFailureAndCancellationReceipts(t *testing.T) {
	for _, tc := range []struct {
		name      string
		committed bool
		outcome   protocol.Outcome
		code      string
	}{
		{"before_commit", false, protocol.OutcomeRejected, "internal"},
		{"after_commit", true, protocol.OutcomeUncertain, "recovery"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := &fakeTarget{data: []byte("old"), replaceErr: errors.New("injected failure"), committed: tc.committed}
			w := fakeWriter(target)
			req := protocol.ReplaceConfigRequest{Meta: testMeta(tc.name), ExpectedRevision: protocol.ConfigRevision(target.data), Data: []byte("new")}
			got, err := w.Replace(context.Background(), req)
			if err == nil || got.Outcome != tc.outcome || got.Code != tc.code {
				t.Fatalf("result = %+v, %v", got, err)
			}
			retry, retryErr := w.Replace(context.Background(), req)
			if retry != got || !errors.Is(retryErr, err) || target.writes != 1 {
				t.Fatalf("retry = %+v, %v; writes = %d", retry, retryErr, target.writes)
			}
		})
	}
	target := &fakeTarget{data: []byte("old")}
	w := fakeWriter(target)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := w.Replace(ctx, protocol.ReplaceConfigRequest{Meta: testMeta("canceled"), ExpectedRevision: protocol.ConfigRevision(target.data), Data: []byte("new")})
	if !errors.Is(err, context.Canceled) || got.Outcome != protocol.OutcomeRejected || target.writes != 0 {
		t.Fatalf("canceled = %+v, %v", got, err)
	}
}

func TestWriterConcurrentCompareReplace(t *testing.T) {
	target := &fakeTarget{data: []byte("old")}
	w := fakeWriter(target)
	revision := protocol.ConfigRevision(target.data)
	results := make(chan protocol.ConfigResult, 2)
	var group sync.WaitGroup
	for i := range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			got, _ := w.Replace(context.Background(), protocol.ReplaceConfigRequest{
				Meta: testMeta(fmt.Sprintf("write-%d", i)), ExpectedRevision: revision, Data: []byte{byte(i)},
			})
			results <- got
		}()
	}
	group.Wait()
	close(results)
	committed, rejected := 0, 0
	for got := range results {
		switch got.Outcome {
		case protocol.OutcomeCommitted:
			committed++
		case protocol.OutcomeRejected:
			rejected++
		}
	}
	if committed != 1 || rejected != 1 || target.writes != 1 {
		t.Fatalf("committed = %d, rejected = %d, writes = %d", committed, rejected, target.writes)
	}
}

func TestWriterBoundsReceiptRetention(t *testing.T) {
	target := &fakeTarget{data: []byte("0")}
	w := fakeWriter(target)
	for i := range protocol.MaxOperationReceipts + 1 {
		data := []byte(fmt.Sprintf("%d", i+1))
		got, err := w.Replace(context.Background(), protocol.ReplaceConfigRequest{
			Meta: testMeta(fmt.Sprintf("operation-%d", i)), ExpectedRevision: protocol.ConfigRevision(target.data), Data: data,
		})
		if err != nil || got.Outcome != protocol.OutcomeCommitted {
			t.Fatalf("operation %d = %+v, %v", i, got, err)
		}
	}
	if len(w.receipts) != protocol.MaxOperationReceipts || len(w.order) != protocol.MaxOperationReceipts {
		t.Fatalf("retained %d receipts and %d IDs", len(w.receipts), len(w.order))
	}
	if _, err := w.GetResult(context.Background(), protocol.OperationResultRequest{Meta: testMeta(""), OperationID: "operation-0"}); !errors.Is(err, protocol.ErrOperationUnknown) {
		t.Fatalf("expired receipt = %v", err)
	}
	if _, err := w.GetResult(context.Background(), protocol.OperationResultRequest{Meta: testMeta(""), OperationID: fmt.Sprintf("operation-%d", protocol.MaxOperationReceipts)}); err != nil {
		t.Fatalf("newest receipt = %v", err)
	}
}

func TestWriterHandleRejectsUnboundDataAndUnknownOperation(t *testing.T) {
	w := fakeWriter(&fakeTarget{data: []byte("old")})
	req := protocol.ReplaceConfigRequest{Meta: testMeta("wire"), ExpectedRevision: protocol.ConfigRevision([]byte("old")), Data: []byte("inline")}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Handle(context.Background(), protocol.OperationReplaceConfig, body); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatalf("inline wire data = %v", err)
	}
	if _, err := w.Handle(context.Background(), protocol.OperationCreateTun, nil); !errors.Is(err, protocol.ErrUnsupportedOperation) {
		t.Fatalf("unknown operation = %v", err)
	}
}

func TestWriterTransferBindingAndReadLimit(t *testing.T) {
	target := &fakeTarget{data: []byte("old")}
	w := fakeWriter(target)
	configs := make(chan transport.ConfigTransfer, 1)
	configs <- transport.ConfigTransfer{Transfer: 2, Data: []byte("new")}
	w.configs = configs
	got, err := w.Replace(context.Background(), protocol.ReplaceConfigRequest{
		Meta: testMeta("mismatch"), ExpectedRevision: protocol.ConfigRevision(target.data), Transfer: 1,
	})
	if !errors.Is(err, protocol.ErrInvalidContract) || got.Code != "invalid" || target.writes != 0 {
		t.Fatalf("mismatched transfer = %+v, %v", got, err)
	}

	w = fakeWriter(&fakeTarget{data: make([]byte, protocol.MaxConfigBytes+1)})
	if _, err := w.ReadCurrent(context.Background(), protocol.ReadConfigRequest{Meta: testMeta("")}); !errors.Is(err, protocol.ErrLimitExceeded) {
		t.Fatalf("oversize read = %v", err)
	}
}

func TestWriterControlTransferAndLostReply(t *testing.T) {
	path := testConfigPath(t, []byte("old"))
	w, err := New(testSession(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	a, z := net.Pipe()
	b, y := net.Pipe()
	c, x := net.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Serve(ctx, transport.Lanes{Control: z, Data: y, Diagnostic: x}) }()
	peer, err := transport.New(ctx, transport.Lanes{Control: a, Data: b, Diagnostic: c}, testSession(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	data := bytes.Repeat([]byte("opaque\x00"), 10_000)
	if err := peer.SendConfig(ctx, 1, data); err != nil {
		t.Fatal(err)
	}
	var result protocol.ConfigResult
	err = peer.Call(ctx, protocol.OperationReplaceConfig, protocol.ReplaceConfigRequest{
		Meta: testMeta("wire-write"), ExpectedRevision: protocol.ConfigRevision([]byte("old")), Transfer: 1,
	}, &result)
	if err != nil || result.Outcome != protocol.OutcomeCommitted {
		t.Fatalf("wire replace = %+v, %v", result, err)
	}
	var receipt protocol.ConfigResult
	err = peer.Call(ctx, protocol.OperationGetOperationResult,
		protocol.OperationResultRequest{Meta: testMeta(""), OperationID: "wire-write"}, &receipt)
	if err != nil || receipt != result {
		t.Fatalf("wire receipt = %+v, %v", receipt, err)
	}
	var document protocol.ConfigDocument
	if err := peer.Call(ctx, protocol.OperationReadConfig, protocol.ReadConfigRequest{Meta: testMeta("")}, &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Data) != 0 || document.Transfer == 0 || document.Revision != result.Revision {
		t.Fatalf("wire read = %+v", document)
	}
	select {
	case transfer := <-peer.Configs():
		if transfer.Transfer != document.Transfer || !bytes.Equal(transfer.Data, data) {
			t.Fatalf("read transfer = %d/%d bytes", transfer.Transfer, len(transfer.Data))
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	written, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(written, data) {
		t.Fatalf("written bytes = %d, %v", len(written), err)
	}
	_ = peer.Close()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("writer did not stop after peer loss")
	}
}

func FuzzWriterReceipts(f *testing.F) {
	f.Add([]byte("old"), []byte("new"), "operation", false, false)
	f.Add([]byte{}, []byte{0, 0xff}, "x", true, false)
	f.Fuzz(func(t *testing.T, initial, replacement []byte, operation string, stale, cancel bool) {
		if len(initial) > 4096 || len(replacement) > 4096 || operation == "" || len(operation) > protocol.MaxOperationIDBytes {
			t.Skip()
		}
		target := &fakeTarget{data: bytes.Clone(initial)}
		w := fakeWriter(target)
		expected := protocol.ConfigRevision(initial)
		if stale {
			expected = protocol.ConfigRevision(append(bytes.Clone(initial), 1))
		}
		ctx := context.Background()
		if cancel {
			canceled, stop := context.WithCancel(ctx)
			stop()
			ctx = canceled
		}
		req := protocol.ReplaceConfigRequest{Meta: testMeta(operation), ExpectedRevision: expected, Data: bytes.Clone(replacement)}
		first, firstErr := w.Replace(ctx, req)
		second, secondErr := w.Replace(context.Background(), req)
		if first != second || !sameError(firstErr, secondErr) || target.writes > 1 {
			t.Fatalf("unstable receipt: first=%+v/%v second=%+v/%v writes=%d", first, firstErr, second, secondErr, target.writes)
		}
		if first.Outcome == protocol.OutcomeCommitted && !bytes.Equal(target.data, replacement) {
			t.Fatal("committed bytes do not match")
		}
		if first.Outcome != protocol.OutcomeCommitted && !bytes.Equal(target.data, initial) {
			t.Fatal("rejected operation changed bytes")
		}
	})
}

func sameError(a, b error) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Error() == b.Error()
}
