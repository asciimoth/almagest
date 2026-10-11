// Package commandruntime adapts the runtime command-node contract to the
// restricted command-helper control protocol.
package commandruntime

import (
	"context"
	"fmt"
	"maps"
	"sync"
	"sync/atomic"

	cmdrunner "github.com/asciimoth/almagest/bin/almagestd/command"
	"github.com/asciimoth/almagest/bin/almagestd/daemonservices"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

const (
	queueDepth  = cmdrunner.DefaultQueueSize
	workerCount = 8
)

// Dispatcher owns one authenticated R-to-H peer. Requests carry only a fixed
// command ID and bounded slot values. H selects every process property.
type Dispatcher struct {
	peer    *transport.Peer
	session protocol.Session
	epoch   uint64
	logger  cmdrunner.Logger
	queue   chan cmdrunner.Request
	ctx     context.Context
	cancel  context.CancelFunc
	next    atomic.Uint64
	closed  atomic.Bool
	once    sync.Once
	wg      sync.WaitGroup
}

// New starts a bounded dispatcher and transfers ownership of peer to it.
func New(parent context.Context, peer *transport.Peer, session protocol.Session, graphEpoch uint64, logger cmdrunner.Logger) (*Dispatcher, error) {
	if parent == nil || peer == nil || graphEpoch == 0 {
		return nil, protocol.ErrInvalidContract
	}
	if err := session.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	d := &Dispatcher{peer: peer, session: session, epoch: graphEpoch, logger: logger, queue: make(chan cmdrunner.Request, queueDepth), ctx: ctx, cancel: cancel}
	for range workerCount {
		d.wg.Go(d.run)
	}
	return d, nil
}

// SubmitNodeCommand queues one request. It does not accept argv, an executable,
// an environment, a directory, credentials, or a sandbox selector.
func (d *Dispatcher) SubmitNodeCommand(request cmdrunner.Request) bool {
	if d == nil || d.closed.Load() {
		return false
	}
	request.Slots = maps.Clone(request.Slots)
	select {
	case d.queue <- request:
		return true
	case <-d.ctx.Done():
		return false
	}
}

func (d *Dispatcher) run() {
	for {
		select {
		case <-d.ctx.Done():
			return
		case request := <-d.queue:
			d.dispatch(request)
		}
	}
}

func (d *Dispatcher) dispatch(request cmdrunner.Request) {
	id := fmt.Sprintf("runtime-command-%d", d.next.Add(1))
	meta := protocol.RequestMeta{GraphEpoch: d.epoch, OperationID: id}
	var result protocol.CommandResult
	err := d.peer.CallSettled(d.ctx, protocol.OperationRunNodeCommand, protocol.NodeCommandRequest{
		Meta: meta, CommandID: request.Name, Slots: request.Slots,
	}, &result)
	if err != nil {
		d.logError("isolated command %s failed: %v", request.Name, err)
		return
	}
	if result.OperationID != id || result.Outcome != protocol.OutcomeCommitted {
		d.logError("isolated command %s was not committed: outcome=%s code=%s", request.Name, result.Outcome, result.Code)
		return
	}
	if result.Code != "" || result.ExitCode != 0 {
		d.logWarn("isolated command %s finished: code=%s exit=%d", request.Name, result.Code, result.ExitCode)
		return
	}
	if d.logger != nil {
		d.logger.Infof("isolated command %s finished", request.Name)
	}
}

func (d *Dispatcher) logError(format string, values ...any) {
	if d.logger != nil {
		d.logger.Errf(format, values...)
	}
}

func (d *Dispatcher) logWarn(format string, values ...any) {
	if d.logger != nil {
		d.logger.Warnf(format, values...)
	}
}

// Close rejects new work, cancels active requests, closes the peer, and waits
// for every worker.
func (d *Dispatcher) Close() {
	if d == nil {
		return
	}
	d.once.Do(func() {
		d.closed.Store(true)
		d.cancel()
		_ = d.peer.Close()
	})
	d.wg.Wait()
}

var _ daemonservices.NodeCommandDispatcher = (*Dispatcher)(nil)
