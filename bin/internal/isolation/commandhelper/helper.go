package commandhelper

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"sync"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

type requestSource byte

const (
	sourceNode requestSource = iota + 1
	sourceCallback
)

type receiptKey struct {
	source requestSource
	id     string
}

type receipt struct {
	input  [32]byte
	result protocol.CommandResult
	err    error
	done   chan struct{}
}

// Helper owns validated protected profiles and bounded operation receipts.
// Runtime and broker requests must enter through their distinct handlers.
type Helper struct {
	session  protocol.Session
	config   *compiledConfig
	launcher Launcher
	runs     chan struct{}

	mu       sync.Mutex
	receipts map[receiptKey]*receipt
	order    []receiptKey

	serveMu        sync.Mutex
	runtimePeer    *transport.Peer
	callbackPeer   *transport.Peer
	runtimeActive  bool
	callbackActive bool
	closed         bool
}

// New validates and freezes protected command input.
func New(session protocol.Session, cfg Config, launcher Launcher) (*Helper, error) {
	if err := session.Validate(); err != nil {
		return nil, err
	}
	compiled, err := compileConfig(cfg, launcher)
	if err != nil {
		return nil, err
	}
	return &Helper{session: session, config: compiled, launcher: launcher, runs: make(chan struct{}, maxConcurrentRuns), receipts: make(map[receiptKey]*receipt)}, nil
}

// Close stops both authenticated channels. An in-flight process receives the
// channel cancellation through its handler context.
func (h *Helper) Close() error {
	if h == nil {
		return nil
	}
	h.serveMu.Lock()
	h.closed = true
	peers := []*transport.Peer{h.runtimePeer, h.callbackPeer}
	h.serveMu.Unlock()
	for _, peer := range peers {
		if peer != nil {
			_ = peer.Close()
		}
	}
	return nil
}

// RunNodeCommand executes an AllowNodes profile. It is also the direct
// component-call form of the runtime-channel operation.
func (h *Helper) RunNodeCommand(ctx context.Context, req protocol.NodeCommandRequest) (protocol.CommandResult, error) {
	if err := h.authorize(req.Meta, true); err != nil {
		return protocol.CommandResult{}, err
	}
	command, ok := h.config.commands[req.CommandID]
	if !ok || !command.AllowNodes {
		return h.reject(sourceNode, req.Meta.OperationID, hashNodeRequest(req), "denied", protocol.ErrPolicyDenied)
	}
	return h.run(ctx, sourceNode, req.Meta.OperationID, hashNodeRequest(req), h.config.profiles[command.Profile], req.Slots)
}

// RunCallback executes a broker-only callback whose values H derives from the
// typed event. RuntimeHandler never dispatches this method.
func (h *Helper) RunCallback(ctx context.Context, req protocol.CallbackRequest) (protocol.CommandResult, error) {
	if err := h.authorize(req.Meta, true); err != nil {
		return protocol.CommandResult{}, err
	}
	callback, ok := h.config.callbacks[req.CallbackID]
	input := hashCallbackRequest(req)
	if !ok {
		return h.reject(sourceCallback, req.Meta.OperationID, input, "denied", protocol.ErrPolicyDenied)
	}
	if req.Event != callback.Event || req.Resource.SessionID != h.session.ID || req.Resource.Type != callback.ResourceType || req.Resource.GraphEpoch != req.Meta.GraphEpoch || !req.Resource.ID.Valid() || req.Resource.Generation == 0 {
		return h.reject(sourceCallback, req.Meta.OperationID, input, "invalid", protocol.ErrInvalidContract)
	}
	slots := make(map[string]string, len(callback.Slots))
	for name, field := range callback.Slots {
		switch field {
		case CallbackEvent:
			slots[name] = req.Event
		case CallbackResourceID:
			slots[name] = req.Resource.ID.String()
		case CallbackResourceType:
			slots[name] = string(req.Resource.Type)
		case CallbackResourceGeneration:
			slots[name] = strconv.FormatUint(req.Resource.Generation, 10)
		case CallbackGraphEpoch:
			slots[name] = strconv.FormatUint(req.Resource.GraphEpoch, 10)
		}
	}
	return h.run(ctx, sourceCallback, req.Meta.OperationID, input, h.config.profiles[callback.Profile], slots)
}

// GetResult returns only node-command receipts. It never exposes callback
// receipts to the runtime channel and never replays a command.
func (h *Helper) GetResult(ctx context.Context, req protocol.OperationResultRequest) (protocol.CommandResult, error) {
	return h.getResult(ctx, sourceNode, req)
}

// GetCallbackResult returns only callback receipts to the broker channel.
func (h *Helper) GetCallbackResult(ctx context.Context, req protocol.OperationResultRequest) (protocol.CommandResult, error) {
	return h.getResult(ctx, sourceCallback, req)
}

func (h *Helper) getResult(ctx context.Context, source requestSource, req protocol.OperationResultRequest) (protocol.CommandResult, error) {
	if err := h.authorize(req.Meta, false); err != nil {
		return protocol.CommandResult{}, err
	}
	if req.OperationID == "" || len(req.OperationID) > protocol.MaxOperationIDBytes {
		return protocol.CommandResult{}, protocol.ErrInvalidContract
	}
	h.mu.Lock()
	receipt := h.receipts[receiptKey{source, req.OperationID}]
	h.mu.Unlock()
	if receipt == nil {
		return protocol.CommandResult{}, protocol.ErrOperationUnknown
	}
	select {
	case <-receipt.done:
		return receipt.result, receipt.err
	case <-ctx.Done():
		return protocol.CommandResult{}, ctx.Err()
	}
}

func (h *Helper) run(ctx context.Context, source requestSource, operationID string, input [32]byte, profile *compiledProfile, slots map[string]string) (protocol.CommandResult, error) {
	key := receiptKey{source, operationID}
	h.mu.Lock()
	if previous := h.receipts[key]; previous != nil {
		if previous.input != input {
			h.mu.Unlock()
			return commandResult(operationID, protocol.OutcomeRejected, "conflict", 0), protocol.ErrOperationConflict
		}
		done := previous.done
		h.mu.Unlock()
		select {
		case <-done:
			return previous.result, previous.err
		case <-ctx.Done():
			return protocol.CommandResult{}, ctx.Err()
		}
	}
	receipt := &receipt{input: input, done: make(chan struct{})}
	if !h.addReceiptLocked(key, receipt) {
		h.mu.Unlock()
		return commandResult(operationID, protocol.OutcomeRejected, "limit", 0), protocol.ErrLimitExceeded
	}
	h.mu.Unlock()

	result, err := h.execute(ctx, operationID, profile, slots)
	h.mu.Lock()
	receipt.result, receipt.err = result, err
	close(receipt.done)
	h.mu.Unlock()
	return result, err
}

func (h *Helper) execute(ctx context.Context, operationID string, profile *compiledProfile, slots map[string]string) (protocol.CommandResult, error) {
	execution, err := profile.resolve(slots)
	if err != nil {
		return commandResult(operationID, protocol.OutcomeRejected, errorCode(err), 0), err
	}
	if err := profile.executable.unchanged(); err != nil {
		return commandResult(operationID, protocol.OutcomeRejected, "executable_changed", 0), err
	}
	if err := profile.directory.unchanged(); err != nil {
		return commandResult(operationID, protocol.OutcomeRejected, "directory_changed", 0), err
	}
	if err := acquire(ctx, h.runs); err != nil {
		return commandResult(operationID, protocol.OutcomeRejected, errorCode(err), 0), err
	}
	defer release(h.runs)
	if err := acquire(ctx, profile.semaphore); err != nil {
		return commandResult(operationID, protocol.OutcomeRejected, errorCode(err), 0), err
	}
	defer release(profile.semaphore)
	runCtx, cancel := context.WithTimeout(ctx, profile.profile.Timeout)
	defer cancel()
	result := h.launcher.Run(runCtx, execution)
	if result.Err == nil {
		return commandResult(operationID, protocol.OutcomeCommitted, "", result.ExitCode), nil
	}
	if !result.Started {
		return commandResult(operationID, protocol.OutcomeRejected, "start", result.ExitCode), result.Err
	}
	code := "exit"
	if errors.Is(result.Err, errOutputLimit) {
		code = "output_limit"
	} else if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		code = "timeout"
	} else if errors.Is(result.Err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		code = "canceled"
	} else {
		var exitError *exec.ExitError
		if errors.As(result.Err, &exitError) || result.ExitCode != -1 {
			return commandResult(operationID, protocol.OutcomeCommitted, code, result.ExitCode), result.Err
		}
	}
	// The process started. Its external effects cannot be compensated or
	// reported as rolled back, even if it exits with an error.
	return commandResult(operationID, protocol.OutcomeUncertain, code, result.ExitCode), result.Err
}

func (h *Helper) reject(source requestSource, id string, input [32]byte, code string, err error) (protocol.CommandResult, error) {
	key := receiptKey{source, id}
	h.mu.Lock()
	defer h.mu.Unlock()
	if previous := h.receipts[key]; previous != nil {
		if previous.input != input {
			return commandResult(id, protocol.OutcomeRejected, "conflict", 0), protocol.ErrOperationConflict
		}
		if !isClosed(previous.done) {
			return commandResult(id, protocol.OutcomeRejected, "conflict", 0), protocol.ErrOperationConflict
		}
		return previous.result, previous.err
	}
	receipt := &receipt{input: input, result: commandResult(id, protocol.OutcomeRejected, code, 0), err: err, done: make(chan struct{})}
	close(receipt.done)
	if !h.addReceiptLocked(key, receipt) {
		return commandResult(id, protocol.OutcomeRejected, "limit", 0), protocol.ErrLimitExceeded
	}
	return receipt.result, receipt.err
}

func (h *Helper) addReceiptLocked(key receiptKey, value *receipt) bool {
	if len(h.receipts) >= protocol.MaxOperationReceipts {
		drop := -1
		for i, candidate := range h.order {
			if isClosed(h.receipts[candidate].done) {
				drop = i
				break
			}
		}
		if drop < 0 {
			return false
		}
		delete(h.receipts, h.order[drop])
		copy(h.order[drop:], h.order[drop+1:])
		h.order = h.order[:len(h.order)-1]
	}
	h.order = append(h.order, key)
	h.receipts[key] = value
	return true
}

func (h *Helper) authorize(meta protocol.RequestMeta, mutation bool) error {
	if h == nil || h.config == nil {
		return protocol.ErrCapabilityUnavailable
	}
	h.serveMu.Lock()
	closed := h.closed
	h.serveMu.Unlock()
	if closed {
		return protocol.ErrCapabilityUnavailable
	}
	return meta.Validate(h.session, mutation)
}

// RuntimeHandler is the closed R-to-H surface. Callback requests are denied
// before decoding or execution.
func (h *Helper) RuntimeHandler(ctx context.Context, operation protocol.Operation, body json.RawMessage) (any, error) {
	switch operation {
	case protocol.OperationRunNodeCommand:
		var request protocol.NodeCommandRequest
		if err := transport.Decode(body, &request); err != nil {
			return nil, err
		}
		result, err := h.RunNodeCommand(ctx, request)
		if result.OperationID != "" {
			return result, nil
		}
		return nil, err
	case protocol.OperationGetOperationResult:
		var request protocol.OperationResultRequest
		if err := transport.Decode(body, &request); err != nil {
			return nil, err
		}
		result, err := h.GetResult(ctx, request)
		if result.OperationID != "" {
			return result, nil
		}
		return nil, err
	default:
		return nil, protocol.ErrUnsupportedOperation
	}
}

// CallbackHandler is the closed N-to-H surface. Node commands are denied.
func (h *Helper) CallbackHandler(ctx context.Context, operation protocol.Operation, body json.RawMessage) (any, error) {
	switch operation {
	case protocol.OperationRunCallback:
		var request protocol.CallbackRequest
		if err := transport.Decode(body, &request); err != nil {
			return nil, err
		}
		result, err := h.RunCallback(ctx, request)
		if result.OperationID != "" {
			return result, nil
		}
		return nil, err
	case protocol.OperationGetOperationResult:
		var request protocol.OperationResultRequest
		if err := transport.Decode(body, &request); err != nil {
			return nil, err
		}
		result, err := h.GetCallbackResult(ctx, request)
		if result.OperationID != "" {
			return result, nil
		}
		return nil, err
	default:
		return nil, protocol.ErrUnsupportedOperation
	}
}

// ServeRuntime owns one authenticated runtime channel until peer loss.
func (h *Helper) ServeRuntime(ctx context.Context, lanes transport.Lanes) error {
	return h.serve(ctx, lanes, sourceNode)
}

// ServeCallbacks owns a distinct authenticated broker callback channel.
func (h *Helper) ServeCallbacks(ctx context.Context, lanes transport.Lanes) error {
	return h.serve(ctx, lanes, sourceCallback)
}

func (h *Helper) serve(ctx context.Context, lanes transport.Lanes, source requestSource) error {
	h.serveMu.Lock()
	if h.closed {
		h.serveMu.Unlock()
		lanes.Close()
		return protocol.ErrCapabilityUnavailable
	}
	active := &h.runtimeActive
	handler := h.RuntimeHandler
	if source == sourceCallback {
		active, handler = &h.callbackActive, h.CallbackHandler
	}
	if *active {
		h.serveMu.Unlock()
		lanes.Close()
		return protocol.ErrInvalidContract
	}
	*active = true
	h.serveMu.Unlock()
	if lanes.Data != nil || lanes.Diagnostic != nil {
		lanes.Close()
		return protocol.ErrInvalidContract
	}
	peer, err := transport.NewControl(ctx, lanes.Control, h.session, handler)
	if err != nil {
		return err
	}
	h.serveMu.Lock()
	if h.closed {
		h.serveMu.Unlock()
		_ = peer.Close()
		return protocol.ErrCapabilityUnavailable
	}
	if source == sourceCallback {
		h.callbackPeer = peer
	} else {
		h.runtimePeer = peer
	}
	h.serveMu.Unlock()
	<-peer.Done()
	return peer.Err()
}

func commandResult(id string, outcome protocol.Outcome, code string, exitCode int32) protocol.CommandResult {
	return protocol.CommandResult{MutationResult: protocol.MutationResult{OperationID: id, Outcome: outcome, Code: code}, ExitCode: exitCode}
}

func hashNodeRequest(request protocol.NodeCommandRequest) [32]byte {
	h := sha256.New()
	writeHashString(h, "node")
	writeHashString(h, request.CommandID)
	writeSlots(h, request.Slots)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func hashCallbackRequest(request protocol.CallbackRequest) [32]byte {
	h := sha256.New()
	writeHashString(h, "callback")
	writeHashString(h, request.CallbackID)
	writeHashString(h, request.Event)
	_, _ = h.Write(request.Resource.ID[:])
	_, _ = h.Write(request.Resource.SessionID[:])
	writeHashString(h, string(request.Resource.Type))
	var values [16]byte
	binary.BigEndian.PutUint64(values[:8], request.Resource.Generation)
	binary.BigEndian.PutUint64(values[8:], request.Resource.GraphEpoch)
	_, _ = h.Write(values[:])
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func writeSlots(w io.Writer, slots map[string]string) {
	keys := make([]string, 0, len(slots))
	for key := range slots {
		keys = append(keys, key)
	}
	slicesSort(keys)
	for _, key := range keys {
		writeHashString(w, key)
		writeHashString(w, slots[key])
	}
}

func writeHashString(w io.Writer, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = w.Write(size[:])
	_, _ = io.WriteString(w, value)
}

func slicesSort(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func acquire(ctx context.Context, semaphore chan struct{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case semaphore <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func release(semaphore chan struct{}) { <-semaphore }
func isClosed(done chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func errorCode(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, protocol.ErrLimitExceeded):
		return "limit"
	case errors.Is(err, protocol.ErrPolicyDenied):
		return "denied"
	case errors.Is(err, protocol.ErrOperationConflict):
		return "conflict"
	default:
		return "invalid"
	}
}

var _ protocol.CommandControl = (*Helper)(nil)
