package sandbox

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
)

type outputCommand struct {
	Command
	output           []byte
	stdoutR, stderrR *io.PipeReader
	stdoutW, stderrW *io.PipeWriter
	done             chan struct{}
	once             sync.Once
	killed           bool
	mu               sync.Mutex
}

func (c *outputCommand) StdoutPipe() (io.ReadCloser, error) {
	c.stdoutR, c.stdoutW = io.Pipe()
	return c.stdoutR, nil
}
func (c *outputCommand) StderrPipe() (io.ReadCloser, error) {
	c.stderrR, c.stderrW = io.Pipe()
	return c.stderrR, nil
}
func (c *outputCommand) Start(context.Context) error {
	c.done = make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for _, w := range []*io.PipeWriter{c.stdoutW, c.stderrW} {
			if w != nil {
				wg.Go(func() { _, _ = w.Write(c.output); _ = w.Close() })
			}
		}
		wg.Wait()
		close(c.done)
	}()
	return nil
}
func (c *outputCommand) Wait() (Exit, error) {
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	return Exit{Killed: c.killed}, nil
}
func (c *outputCommand) Kill() error {
	c.mu.Lock()
	c.killed = true
	c.mu.Unlock()
	c.once.Do(func() {
		for _, w := range []*io.PipeWriter{c.stdoutW, c.stderrW} {
			if w != nil {
				_ = w.Close()
			}
		}
	})
	return nil
}
func TestBoundedOutput(t *testing.T) {
	for _, combined := range []bool{false, true} {
		c := &outputCommand{output: []byte("hello")}
		var data []byte
		var err error
		if combined {
			data, err = CombinedOutput(context.Background(), c, 100)
		} else {
			data, err = Output(context.Background(), c, 100)
		}
		expected := "hello"
		if combined {
			expected += "hello"
		}
		if err != nil || string(data) != expected {
			t.Fatalf("%q %v", data, err)
		}
		c = &outputCommand{output: make([]byte, 10000)}
		if combined {
			data, err = CombinedOutput(context.Background(), c, 5)
		} else {
			data, err = Output(context.Background(), c, 5)
		}
		if len(data) != 5 || !errors.Is(err, ErrQuota) || !c.killed {
			t.Fatalf("length=%d killed=%t error=%v", len(data), c.killed, err)
		}
	}
}
