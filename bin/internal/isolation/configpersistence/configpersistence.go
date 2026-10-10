// Package configpersistence adapts the runtime side of the isolation config
// writer protocol to the daemon's fixed-target persistence contract.
package configpersistence

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/asciimoth/almagest/bin/almagestd/daemonservices"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

const defaultOperationTimeout = 30 * time.Second

type peer interface {
	Call(context.Context, protocol.Operation, any, any) error
	SendConfig(context.Context, uint64, []byte) error
	Configs() <-chan transport.ConfigTransfer
	Done() <-chan struct{}
	Err() error
}

// Persistence sends bounded opaque config transactions to C. The adapter
// serializes the transfer lane because a transfer is consumed by exactly one
// following operation.
type Persistence struct {
	peer       peer
	session    protocol.Session
	graphEpoch uint64
	timeout    time.Duration

	mu           sync.Mutex
	nextTransfer atomic.Uint64
}

// New constructs a runtime adapter for an authenticated config-writer peer.
// The caller retains ownership of the peer.
func New(p *transport.Peer, session protocol.Session, graphEpoch uint64, timeout time.Duration) (*Persistence, error) {
	return newPersistence(p, session, graphEpoch, timeout)
}

func newPersistence(p peer, session protocol.Session, graphEpoch uint64, timeout time.Duration) (*Persistence, error) {
	if p == nil || graphEpoch == 0 {
		return nil, protocol.ErrInvalidContract
	}
	if err := session.Validate(); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = defaultOperationTimeout
	}
	return &Persistence{peer: p, session: session, graphEpoch: graphEpoch, timeout: timeout}, nil
}

func (p *Persistence) meta(operationID string) protocol.RequestMeta {
	return protocol.RequestMeta{GraphEpoch: p.graphEpoch, OperationID: operationID}
}

func (p *Persistence) operationContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), p.timeout)
}

// ReadCurrent reads one complete document and verifies its content revision.
func (p *Persistence) ReadCurrent() (daemonservices.ConfigDocument, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ctx, cancel := p.operationContext()
	defer cancel()

	var document protocol.ConfigDocument
	if err := p.peer.Call(ctx, protocol.OperationReadConfig, protocol.ReadConfigRequest{Meta: p.meta("")}, &document); err != nil {
		return daemonservices.ConfigDocument{}, fmt.Errorf("read isolated config: %w", err)
	}
	if document.Transfer == 0 || len(document.Data) != 0 {
		return daemonservices.ConfigDocument{}, protocol.ErrInvalidContract
	}
	select {
	case transfer, ok := <-p.peer.Configs():
		if !ok {
			return daemonservices.ConfigDocument{}, io.ErrClosedPipe
		}
		if transfer.Transfer != document.Transfer || protocol.ConfigRevision(transfer.Data) != document.Revision {
			return daemonservices.ConfigDocument{}, protocol.ErrInvalidContract
		}
		return daemonservices.ConfigDocument{Data: transfer.Data, Revision: document.Revision}, nil
	case <-ctx.Done():
		return daemonservices.ConfigDocument{}, ctx.Err()
	case <-p.peer.Done():
		return daemonservices.ConfigDocument{}, p.peer.Err()
	}
}

// Replace transfers the candidate once and resolves a lost reply through the
// retained operation receipt. It never replays the replacement.
func (p *Persistence) Replace(expectedRevision, operationID string, data []byte) daemonservices.ConfigOperationResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	if operationID == "" || len(operationID) > protocol.MaxOperationIDBytes {
		return rejected(operationID, "invalid", "invalid config persistence operation ID", "")
	}
	if len(data) > protocol.MaxConfigBytes {
		return rejected(operationID, "limit", protocol.ErrLimitExceeded.Error(), "")
	}
	transfer := p.nextTransfer.Add(1)
	if transfer == 0 {
		return rejected(operationID, "limit", protocol.ErrLimitExceeded.Error(), "")
	}
	ctx, cancel := p.operationContext()
	defer cancel()
	if err := p.peer.SendConfig(ctx, transfer, data); err != nil {
		return rejected(operationID, "transfer", fmt.Sprintf("transfer config: %v", err), expectedRevision)
	}

	request := protocol.ReplaceConfigRequest{
		Meta: p.meta(operationID), ExpectedRevision: expectedRevision, Transfer: transfer,
	}
	var result protocol.ConfigResult
	if err := p.peer.Call(ctx, protocol.OperationReplaceConfig, request, &result); err == nil {
		return convertResult(result)
	}
	return p.recoverResult(operationID)
}

// GetResult queries a retained operation receipt without replaying a write.
func (p *Persistence) GetResult(operationID string) (daemonservices.ConfigOperationResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ctx, cancel := p.operationContext()
	defer cancel()
	return p.getResult(ctx, operationID)
}

func (p *Persistence) recoverResult(operationID string) daemonservices.ConfigOperationResult {
	ctx, cancel := p.operationContext()
	defer cancel()
	ticker := time.NewTicker(min(10*time.Millisecond, p.timeout))
	defer ticker.Stop()
	for {
		result, err := p.getResult(ctx, operationID)
		if err == nil {
			return result
		}
		if !errors.Is(err, daemonservices.ErrConfigOperationUnknown) {
			return uncertain(operationID, fmt.Sprintf("lost config-writer reply and result lookup failed: %v", err))
		}
		select {
		case <-ctx.Done():
			return uncertain(operationID, "lost config-writer reply and operation result is unknown")
		case <-p.peer.Done():
			return uncertain(operationID, fmt.Sprintf("config writer stopped before operation status was known: %v", p.peer.Err()))
		case <-ticker.C:
		}
	}
}

func (p *Persistence) getResult(ctx context.Context, operationID string) (daemonservices.ConfigOperationResult, error) {
	if operationID == "" || len(operationID) > protocol.MaxOperationIDBytes {
		return daemonservices.ConfigOperationResult{}, protocol.ErrInvalidContract
	}
	var result protocol.ConfigResult
	err := p.peer.Call(ctx, protocol.OperationGetOperationResult,
		protocol.OperationResultRequest{Meta: p.meta(""), OperationID: operationID}, &result)
	if errors.Is(err, protocol.ErrOperationUnknown) {
		return daemonservices.ConfigOperationResult{}, daemonservices.ErrConfigOperationUnknown
	}
	if err != nil {
		return daemonservices.ConfigOperationResult{}, err
	}
	return convertResult(result), nil
}

func convertResult(result protocol.ConfigResult) daemonservices.ConfigOperationResult {
	if err := result.Validate(); err != nil {
		return rejected(result.OperationID, "invalid", err.Error(), result.Revision)
	}
	switch result.Outcome {
	case protocol.OutcomeCommitted:
		return daemonservices.ConfigOperationResult{
			OperationID: result.OperationID, Revision: result.Revision,
			Outcome: daemonservices.ConfigOutcomeCommitted, Code: result.Code,
		}
	case protocol.OutcomeUncertain:
		return daemonservices.ConfigOperationResult{
			OperationID: result.OperationID, Revision: result.Revision,
			Outcome: daemonservices.ConfigOutcomeUncertain, Code: result.Code,
			Error: "config writer could not confirm durable replacement",
		}
	default:
		message := "config writer rejected replacement"
		if result.Code != "" {
			message += ": " + result.Code
		}
		if result.Code == "conflict" {
			message = daemonservices.ErrConfigRevisionConflict.Error()
		}
		return rejected(result.OperationID, result.Code, message, result.Revision)
	}
}

func rejected(operationID, code, message, revision string) daemonservices.ConfigOperationResult {
	return daemonservices.ConfigOperationResult{
		OperationID: operationID, Revision: revision, Outcome: daemonservices.ConfigOutcomeRejected, Code: code, Error: message,
	}
}

func uncertain(operationID, message string) daemonservices.ConfigOperationResult {
	return daemonservices.ConfigOperationResult{
		OperationID: operationID, Outcome: daemonservices.ConfigOutcomeUncertain, Code: "recovery", Error: message,
	}
}

var _ daemonservices.ConfigPersistence = (*Persistence)(nil)
