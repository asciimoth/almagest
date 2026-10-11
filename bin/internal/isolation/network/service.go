package network

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/supervision"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

// Service connects an unused journaled broker to an already-started restricted
// process set. Bootstrap must authenticate the lanes and apply N's final
// profile before Run. Construction does not start a server or native operation.
// The caller transfers exclusive ownership of the broker and lanes on success.
type Service struct {
	broker     *Broker
	lanes      transport.Lanes
	supervisor *supervision.Supervisor
	timeout    time.Duration
	done       chan error
	mu         sync.Mutex
	used       bool
	active     bool
	serving    bool
	uncertain  bool
	ctx        context.Context
	cancel     context.CancelFunc
}

// NewService binds readiness and policy identity to the actual broker. The
// corresponding Options fields are not accepted as caller-supplied proofs.
// A production composition cannot use a broker without a durable journal.
func NewService(broker *Broker, lanes transport.Lanes, reconciler supervision.Reconciler, perEffect time.Duration, options supervision.Options, degraded supervision.Degraded, processes ...supervision.Process) (*Service, error) {
	if broker == nil || reconciler == nil || perEffect <= 0 || lanes.Control == nil || lanes.Data == nil || lanes.Diagnostic == nil {
		return nil, supervision.ErrInvalidComposition
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if broker.journal == nil || broker.serviceAssigned || broker.closed || broker.data != nil || broker.applicationUp || len(broker.resources) != 0 || len(broker.receipts) != 0 {
		return nil, supervision.ErrInvalidComposition
	}
	s := &Service{broker: broker, lanes: lanes, timeout: options.ShutdownTimeout, done: make(chan error, 1)}
	options.ApplicationReady = broker.applicationReady
	options.PolicyDigest = broker.session.PolicyDigest
	host := serviceHost{BrokerHost: supervision.BrokerHost{Journal: broker.journal, Reconciler: reconciler, Broker: broker, PerEffect: perEffect}, service: s}
	var err error
	s.supervisor, err = supervision.New(host, degraded, options, processes...)
	if err != nil {
		return nil, err
	}
	broker.serviceAssigned = true
	return s, nil
}

func (s *Service) Ready() <-chan struct{} { return s.supervisor.Ready() }

func (s *Service) Status() supervision.Status {
	status := s.supervisor.Status()
	s.mu.Lock()
	status.RecoveryRequired = status.RecoveryRequired || s.uncertain
	s.mu.Unlock()
	return status
}

// Run is single-use. Journal reconciliation precedes all runtime RPC handling.
// Any server termination cancels supervision; there is no in-place restart or
// native fallback. Shutdown joins the server within the configured bound.
func (s *Service) Run(parent context.Context) error {
	if parent == nil {
		return supervision.ErrInvalidComposition
	}
	s.mu.Lock()
	if s.used {
		s.mu.Unlock()
		return supervision.ErrInvalidComposition
	}
	s.used, s.active = true, true
	s.ctx, s.cancel = context.WithCancel(parent)
	s.mu.Unlock()
	err := s.supervisor.Run(s.ctx)
	s.mu.Lock()
	s.active = false
	s.cancel()
	serving := s.serving
	s.mu.Unlock()
	s.lanes.Close()
	if !serving {
		return err
	}
	timer := time.NewTimer(s.timeout)
	defer timer.Stop()
	select {
	case serveErr := <-s.done:
		return errors.Join(err, serveErr)
	case <-timer.C:
		s.mu.Lock()
		s.uncertain = true
		s.mu.Unlock()
		return errors.Join(err, supervision.ErrShutdownIncomplete, context.DeadlineExceeded)
	}
}

type serviceHost struct {
	supervision.BrokerHost
	service *Service
}

func (h serviceHost) Recover(ctx context.Context) error {
	if err := h.BrokerHost.Recover(ctx); err != nil {
		return err
	}
	s := h.service
	s.mu.Lock()
	defer s.mu.Unlock()
	// A slow reconciler can complete after Supervisor's recovery deadline.
	// Never let that late completion start a server after shutdown.
	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.active || s.serving || s.ctx.Err() != nil {
		return supervision.ErrInvalidComposition
	}
	s.serving = true
	go func() {
		err := s.broker.Serve(s.ctx, s.lanes)
		if s.ctx.Err() == nil {
			err = errors.Join(supervision.ErrRuntimeLost, err)
			s.cancel()
		}
		s.done <- err
	}()
	return nil
}
