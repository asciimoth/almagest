package almagest

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

type wireGuardTrackedBindReceiveError struct {
	generation       uint64
	openGeneration   uint64
	intentionalClose bool
	err              error
}

func (e wireGuardTrackedBindReceiveError) Error() string {
	return e.err.Error()
}

func (e wireGuardTrackedBindReceiveError) Unwrap() error {
	return e.err
}

func (e wireGuardTrackedBindReceiveError) Format(s fmt.State, verb rune) {
	_, _ = fmt.Fprintf(s, "%"+string(verb), e.err)
}

func (n *wireGuardNode) nextBindGenerationLocked() uint64 {
	n.currentBindGen++
	return n.currentBindGen
}
