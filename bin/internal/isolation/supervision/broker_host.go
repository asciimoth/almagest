package supervision

import (
	"context"
	"errors"
	"time"
)

// Revoker is the trusted N-side resource owner. Network brokers implement it
// with Shutdown. The interface contains no launcher or native fallback.
type Revoker interface {
	Shutdown(context.Context) error
}

// BrokerHost connects the durable recovery journal and the live broker to the
// supervisor. Recovery always runs before readiness. Revocation first rejects
// new runtime work, then reconciles records left by cleanup.
type BrokerHost struct {
	Journal    *Journal
	Reconciler Reconciler
	Broker     Revoker
	PerEffect  time.Duration
}

func (h BrokerHost) valid() bool {
	return h.Journal != nil && h.Reconciler != nil && h.Broker != nil && h.PerEffect > 0
}

func (h BrokerHost) Recover(ctx context.Context) error {
	if !h.valid() {
		return ErrInvalidComposition
	}
	_, err := h.Journal.Recover(ctx, h.Reconciler, h.PerEffect)
	return err
}

func (h BrokerHost) Revoke(ctx context.Context) error {
	if !h.valid() {
		return ErrInvalidComposition
	}
	revokeErr := h.Broker.Shutdown(ctx)
	_, recoveryErr := h.Journal.Recover(ctx, h.Reconciler, h.PerEffect)
	return errors.Join(revokeErr, recoveryErr)
}
