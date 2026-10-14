// Package configwriter implements the isolated fixed-target config writer used
// by config updates, autosave, and snapshot restore.
package configwriter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

type targetSnapshot struct {
	data  []byte
	token fileToken
}

type fixedTarget interface {
	read(int) (targetSnapshot, error)
	replace(context.Context, fileToken, []byte) (bool, error)
	close() error
}

type receipt struct {
	input  [32]byte
	result protocol.ConfigResult
	err    error
}

// Writer owns one target selected at construction. Requests cannot change its
// path or approved metadata.
type Writer struct {
	session protocol.Session
	target  fixedTarget

	mu       sync.Mutex
	receipts map[string]receipt
	order    []string
	nextDrop int

	serveMu  sync.Mutex
	configs  <-chan transport.ConfigTransfer
	peer     *transport.Peer
	ready    chan struct{}
	serving  bool
	nextSend uint64
}

// New opens and validates one fixed target. The target must already exist.
func New(session protocol.Session, path string) (*Writer, error) {
	if err := session.Validate(); err != nil {
		return nil, err
	}
	target, err := openTarget(path)
	if err != nil {
		return nil, err
	}
	return &Writer{session: session, target: target, receipts: make(map[string]receipt)}, nil
}

// Close releases the held target-directory handle.
func (w *Writer) Close() error {
	if w == nil || w.target == nil {
		return nil
	}
	return w.target.close()
}

func (w *Writer) authorize(meta protocol.RequestMeta, mutation bool) error {
	if w == nil || w.target == nil {
		return protocol.ErrCapabilityUnavailable
	}
	return meta.Validate(w.session, mutation)
}

// ReadCurrent returns bounded opaque bytes and their content revision.
func (w *Writer) ReadCurrent(_ context.Context, req protocol.ReadConfigRequest) (protocol.ConfigDocument, error) {
	if err := w.authorize(req.Meta, false); err != nil {
		return protocol.ConfigDocument{}, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	snapshot, err := w.target.read(protocol.MaxConfigBytes)
	if err != nil {
		return protocol.ConfigDocument{}, err
	}
	return protocol.ConfigDocument{Data: bytes.Clone(snapshot.data), Revision: protocol.ConfigRevision(snapshot.data)}, nil
}

// Replace compares the current revision, durably replaces the fixed target,
// and retains the outcome under the operation ID.
func (w *Writer) Replace(ctx context.Context, req protocol.ReplaceConfigRequest) (protocol.ConfigResult, error) {
	if err := w.authorize(req.Meta, true); err != nil {
		return protocol.ConfigResult{}, err
	}
	if req.Transfer != 0 {
		if len(req.Data) != 0 {
			return w.rejectRequest(req, "invalid", protocol.ErrInvalidContract)
		}
		data, err := w.consumeTransfer(ctx, req.Transfer)
		if err != nil {
			return w.rejectRequest(req, errorCode(err), err)
		}
		req.Data = data
	}
	if len(req.Data) > protocol.MaxConfigBytes {
		return w.rejectRequest(req, "limit", protocol.ErrLimitExceeded)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	input := inputHash(req.ExpectedRevision, req.Transfer, req.Data)
	if prior, ok := w.receipts[req.Meta.OperationID]; ok {
		if prior.input != input {
			return conflictResult(req.Meta.OperationID, prior.result.Revision), protocol.ErrOperationConflict
		}
		return prior.result, prior.err
	}
	if err := ctx.Err(); err != nil {
		return w.retain(input, result(req.Meta.OperationID, protocol.OutcomeRejected, "canceled", ""), err)
	}
	snapshot, err := w.target.read(protocol.MaxConfigBytes)
	if err != nil {
		return w.retain(input, result(req.Meta.OperationID, protocol.OutcomeRejected, errorCode(err), ""), err)
	}
	currentRevision := protocol.ConfigRevision(snapshot.data)
	if req.ExpectedRevision != currentRevision {
		return w.retain(input, conflictResult(req.Meta.OperationID, currentRevision), protocol.ErrOperationConflict)
	}
	committed, err := w.target.replace(ctx, snapshot.token, req.Data)
	newRevision := protocol.ConfigRevision(req.Data)
	if err != nil {
		outcome := protocol.OutcomeRejected
		revision := currentRevision
		code := errorCode(err)
		if committed {
			outcome, revision, code = protocol.OutcomeUncertain, newRevision, "recovery"
		}
		return w.retain(input, result(req.Meta.OperationID, outcome, code, revision), err)
	}
	return w.retain(input, result(req.Meta.OperationID, protocol.OutcomeCommitted, "", newRevision), nil)
}

// GetResult returns a bounded retained receipt. It never replays a write.
func (w *Writer) GetResult(_ context.Context, req protocol.OperationResultRequest) (protocol.ConfigResult, error) {
	if err := w.authorize(req.Meta, false); err != nil {
		return protocol.ConfigResult{}, err
	}
	if req.OperationID == "" || len(req.OperationID) > protocol.MaxOperationIDBytes {
		return protocol.ConfigResult{}, protocol.ErrInvalidContract
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	prior, ok := w.receipts[req.OperationID]
	if !ok {
		return protocol.ConfigResult{}, protocol.ErrOperationUnknown
	}
	return prior.result, prior.err
}

func (w *Writer) retain(input [32]byte, value protocol.ConfigResult, err error) (protocol.ConfigResult, error) {
	if len(w.order) < protocol.MaxOperationReceipts {
		w.order = append(w.order, value.OperationID)
	} else {
		delete(w.receipts, w.order[w.nextDrop])
		w.order[w.nextDrop] = value.OperationID
		w.nextDrop = (w.nextDrop + 1) % protocol.MaxOperationReceipts
	}
	w.receipts[value.OperationID] = receipt{input: input, result: value, err: err}
	return value, err
}

func (w *Writer) rejectRequest(req protocol.ReplaceConfigRequest, code string, err error) (protocol.ConfigResult, error) {
	id := req.Meta.OperationID
	if id == "" {
		return protocol.ConfigResult{}, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	input := inputHash(req.ExpectedRevision, req.Transfer, req.Data)
	if prior, ok := w.receipts[id]; ok {
		if prior.input != input {
			return conflictResult(id, prior.result.Revision), protocol.ErrOperationConflict
		}
		return prior.result, prior.err
	}
	return w.retain(input, result(id, protocol.OutcomeRejected, code, ""), err)
}

func result(id string, outcome protocol.Outcome, code, revision string) protocol.ConfigResult {
	return protocol.ConfigResult{MutationResult: protocol.MutationResult{OperationID: id, Outcome: outcome, Code: code}, Revision: revision}
}

func conflictResult(id, revision string) protocol.ConfigResult {
	return result(id, protocol.OutcomeRejected, "conflict", revision)
}

func inputHash(expected string, transfer uint64, data []byte) [32]byte {
	h := sha256.New()
	_, _ = io.WriteString(h, expected)
	_, _ = h.Write([]byte{0, byte(transfer >> 56), byte(transfer >> 48), byte(transfer >> 40), byte(transfer >> 32), byte(transfer >> 24), byte(transfer >> 16), byte(transfer >> 8), byte(transfer)})
	_, _ = h.Write(data)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func errorCode(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, protocol.ErrLimitExceeded):
		return "limit"
	case errors.Is(err, protocol.ErrInvalidContract):
		return "invalid"
	case errors.Is(err, protocol.ErrOperationConflict):
		return "conflict"
	default:
		return "internal"
	}
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) != 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func (w *Writer) consumeTransfer(ctx context.Context, id uint64) ([]byte, error) {
	w.serveMu.Lock()
	configs := w.configs
	w.serveMu.Unlock()
	if configs == nil {
		return nil, protocol.ErrInvalidContract
	}
	select {
	case transfer := <-configs:
		if transfer.Transfer != id {
			return nil, protocol.ErrInvalidContract
		}
		return transfer.Data, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Handle dispatches the closed config-writer control surface.
func (w *Writer) Handle(ctx context.Context, op protocol.Operation, body json.RawMessage) (any, error) {
	switch op {
	case protocol.OperationReadConfig:
		var req protocol.ReadConfigRequest
		if err := transport.Decode(body, &req); err != nil {
			return nil, err
		}
		document, err := w.ReadCurrent(ctx, req)
		if err != nil {
			return nil, err
		}
		transfer, err := w.sendDocument(ctx, document.Data)
		if err != nil {
			return nil, err
		}
		document.Data, document.Transfer = nil, transfer
		return document, nil
	case protocol.OperationReplaceConfig:
		var req protocol.ReplaceConfigRequest
		if err := transport.Decode(body, &req); err != nil {
			return nil, err
		}
		if req.Transfer == 0 || len(req.Data) != 0 {
			return nil, protocol.ErrInvalidContract
		}
		value, err := w.Replace(ctx, req)
		if value.OperationID != "" {
			return value, nil
		}
		return nil, err
	case protocol.OperationGetOperationResult:
		var req protocol.OperationResultRequest
		if err := transport.Decode(body, &req); err != nil {
			return nil, err
		}
		value, err := w.GetResult(ctx, req)
		if value.OperationID != "" {
			return value, nil
		}
		return nil, err
	default:
		return nil, protocol.ErrUnsupportedOperation
	}
}

// Serve owns an authenticated config-writer channel until cancellation or
// peer loss. Config transfers are consumed only by a matching Replace call.
func (w *Writer) Serve(ctx context.Context, lanes transport.Lanes) error {
	w.serveMu.Lock()
	if w.serving {
		w.serveMu.Unlock()
		lanes.Close()
		return protocol.ErrInvalidContract
	}
	w.serving = true
	w.ready = make(chan struct{})
	w.serveMu.Unlock()
	// The config writer profile must not receive network data or admin lanes.
	if lanes.Data != nil || lanes.Diagnostic != nil {
		lanes.Close()
		return protocol.ErrInvalidContract
	}
	peer, err := transport.NewControl(ctx, lanes.Control, w.session, w.Handle)
	if err != nil {
		w.serveMu.Lock()
		w.serving = false
		close(w.ready)
		w.ready = nil
		w.serveMu.Unlock()
		return err
	}
	w.serveMu.Lock()
	w.configs = peer.Configs()
	w.peer = peer
	w.nextSend = 0
	close(w.ready)
	w.serveMu.Unlock()
	defer func() {
		w.serveMu.Lock()
		w.configs = nil
		w.peer = nil
		w.ready = nil
		w.serving = false
		w.serveMu.Unlock()
		_ = peer.Close()
	}()
	<-peer.Done()
	return peer.Err()
}

func (w *Writer) sendDocument(ctx context.Context, data []byte) (uint64, error) {
	w.serveMu.Lock()
	ready := w.ready
	w.serveMu.Unlock()
	if ready == nil {
		return 0, protocol.ErrInvalidContract
	}
	select {
	case <-ready:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	w.serveMu.Lock()
	peer := w.peer
	if peer == nil || w.nextSend == ^uint64(0) {
		w.serveMu.Unlock()
		return 0, protocol.ErrLimitExceeded
	}
	w.nextSend++
	id := w.nextSend
	w.serveMu.Unlock()
	if err := peer.SendConfig(ctx, id, data); err != nil {
		return 0, err
	}
	return id, nil
}
