package wireguardnodes

import (
	"fmt"
	"sync/atomic"

	conn "github.com/asciimoth/batchudp"
)

type wireGuardTrackedBind struct {
	conn.Bind
	generation uint64
	state      *wireGuardTrackedBindState
}

type wireGuardTrackedBindState struct {
	open    atomic.Uint64
	closing atomic.Uint64
}

func (b wireGuardTrackedBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	fns, actualPort, err := b.Bind.Open(port)
	if err != nil {
		return nil, actualPort, err
	}
	var openGeneration uint64
	if b.state != nil {
		openGeneration = b.state.open.Add(1)
	}
	for i, fn := range fns {
		fns[i] = b.wrapReceiveFunc(fn, openGeneration)
	}
	return fns, actualPort, nil
}

func (b wireGuardTrackedBind) Close() error {
	if b.state != nil {
		b.state.closing.Store(b.state.open.Load())
	}
	return b.Bind.Close()
}

func (b wireGuardTrackedBind) wrapReceiveFunc(fn conn.ReceiveFunc, openGeneration uint64) conn.ReceiveFunc {
	return func(packets [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
		n, err := fn(packets, sizes, eps)
		if err != nil {
			// WireGuard receive errors are reported by wgo after the receive
			// function returns. Almagest needs generation metadata at that
			// boundary because a closed receive routine can mean two very
			// different things:
			//
			//   - the current bind died unexpectedly and must be rebuilt
			//   - an older/current bind was intentionally closed during a
			//     detach, rebind, device down, or runtime replacement
			//
			// Do not rely on the underlying error value alone for that decision.
			// In particular, net.ErrClosed is both a normal close sentinel and
			// the shape of several real active-bind failures.
			intentionalClose := b.state != nil && openGeneration != 0 && openGeneration <= b.state.closing.Load()
			err = wireGuardTrackedBindReceiveError{
				generation:       b.generation,
				openGeneration:   openGeneration,
				intentionalClose: intentionalClose,
				err:              err,
			}
		}
		return n, err
	}
}

// wireGuardTrackedBindReceiveError carries the lifecycle metadata that wgo's
// receive loop does not know about when it reports a bind receive failure.
//
// wgo's receive loop treats errors matching net.ErrClosed as normal shutdown
// before it notifies SubscribeReceiveErrors subscribers. If this wrapper unwraps
// to the original net.ErrClosed, an unexpected close of the active bind becomes
// indistinguishable from a deliberate device/bind shutdown at the wgo boundary.
// Almagest then never sees the receive-loop failure, keeps the stale bind object
// installed, and can publish "up" while inbound packets are blackholed.
//
// Do not implement Unwrap or Is on this type. Its underlying error must remain
// opaque to errors.Is until Almagest has recovered this concrete type and
// inspected generation and intentionalClose below.
//
// The wrapped error is intentionally opaque to errors.Is outside this package.
// Almagest still unwraps it manually after errors.As has recovered this concrete
// type, because by then the generation and intentionalClose metadata have been
// preserved and handleReceiveError can make the lifecycle decision explicitly.
type wireGuardTrackedBindReceiveError struct {
	generation       uint64
	openGeneration   uint64
	intentionalClose bool
	err              error
}

func (e wireGuardTrackedBindReceiveError) Error() string {
	return e.err.Error()
}

// Format preserves the underlying error's formatting without exposing it
// through Unwrap/Is to upstream sentinel suppression.
func (e wireGuardTrackedBindReceiveError) Format(s fmt.State, verb rune) {
	_, _ = fmt.Fprintf(s, "%"+string(verb), e.err)
}

func (n *wireGuardNode) nextBindGenerationLocked() uint64 {
	n.currentBindGen++
	return n.currentBindGen
}
