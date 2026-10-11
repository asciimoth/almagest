//go:build linux && amd64

package networkrole

import (
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

const (
	roleOutputQueueDepth = 256
	roleOutputChunkBytes = 8 << 10
	roleOutputCloseWait  = 2 * time.Second
)

var errRoleOutputIncomplete = errors.New("bounded role output did not stop")

// boundedRoleOutput separates restricted role progress from an operator sink.
// Writes never wait for the sink. A full queue drops the complete input and
// records its size. This output is for diagnostics only, not a data contract.
type boundedRoleOutput struct {
	sink    io.Writer
	queue   chan []byte
	done    chan struct{}
	mu      sync.Mutex
	closed  bool
	dropped atomic.Uint64
}

func newBoundedRoleOutput(sink io.Writer) *boundedRoleOutput {
	if sink == nil {
		return nil
	}
	o := &boundedRoleOutput{sink: sink, queue: make(chan []byte, roleOutputQueueDepth), done: make(chan struct{})}
	go o.run()
	return o
}

func (o *boundedRoleOutput) Write(input []byte) (int, error) {
	if o == nil {
		return len(input), nil
	}
	original := len(input)
	if len(input) > roleOutputChunkBytes {
		input = input[:roleOutputChunkBytes]
		o.dropped.Add(uint64(original - len(input)))
	}
	copyInput := append([]byte(nil), input...)
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		o.dropped.Add(uint64(len(input)))
		return original, nil
	}
	select {
	case o.queue <- copyInput:
	default:
		o.dropped.Add(uint64(len(input)))
	}
	return original, nil
}

func (o *boundedRoleOutput) DroppedBytes() uint64 {
	if o == nil {
		return 0
	}
	return o.dropped.Load()
}

func (o *boundedRoleOutput) Close() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	if !o.closed {
		o.closed = true
		close(o.queue)
	}
	o.mu.Unlock()
	select {
	case <-o.done:
		return nil
	case <-time.After(roleOutputCloseWait):
		return errRoleOutputIncomplete
	}
}

func (o *boundedRoleOutput) run() {
	defer close(o.done)
	for value := range o.queue {
		if _, err := o.sink.Write(value); err != nil {
			o.dropped.Add(uint64(len(value)))
		}
	}
}
