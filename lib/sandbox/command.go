package sandbox

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
)

// Run starts a command and waits for its stable exit result.
func Run(ctx context.Context, command Command) (Exit, error) {
	if err := command.Start(ctx); err != nil {
		return Exit{}, err
	}
	return command.Wait()
}

// Output captures stdout up to limit bytes. On overflow it kills the tree and
// drains the pipes. The command must obey the bounded pipe shutdown contract.
func Output(ctx context.Context, command Command, limit int64) ([]byte, error) {
	return capture(ctx, command, limit, false)
}

// CombinedOutput captures both streams in arrival order, up to limit bytes.
func CombinedOutput(ctx context.Context, command Command, limit int64) ([]byte, error) {
	return capture(ctx, command, limit, true)
}

type captureBuffer struct {
	mu    sync.Mutex
	data  []byte
	limit int64
}

func (b *captureBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := int64(len(p))
	left := b.limit - int64(len(b.data))
	if n > left {
		b.data = append(b.data, p[:int(left)]...)
		return int(left), ErrQuota
	}
	b.data = append(b.data, p...)
	return len(p), nil
}
func capture(ctx context.Context, command Command, limit int64, combined bool) ([]byte, error) {
	if limit < 1 || limit > 64<<20 {
		return nil, ErrInvalid
	}
	if bounded, ok := command.(interface{ OutputLimit() int64 }); ok && limit > bounded.OutputLimit() {
		return nil, ErrQuota
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	defer func() { _ = stdout.Close() }()
	streams := []io.ReadCloser{stdout}
	if combined {
		stderr, e := command.StderrPipe()
		if e != nil {
			return nil, e
		}
		defer func() { _ = stderr.Close() }()
		streams = append(streams, stderr)
	}
	if err = command.Start(ctx); err != nil {
		return nil, err
	}
	buffer := &captureBuffer{limit: limit}
	var wg sync.WaitGroup
	copyErrors := make(chan error, len(streams))
	for _, stream := range streams {
		wg.Go(func() {
			_, e := io.Copy(buffer, stream)
			// Command.Wait can close an os/exec pipe after the process exits while
			// the copy goroutine is consuming its final bytes. The close is the
			// normal bounded-pipe shutdown signal, not an output failure.
			if errors.Is(e, os.ErrClosed) {
				return
			}
			if e != nil {
				killErr := command.Kill()
				_, drainErr := io.Copy(io.Discard, stream)
				copyErrors <- errors.Join(e, killErr, drainErr)
			}
		})
	}
	_, err = command.Wait()
	wg.Wait()
	close(copyErrors)
	for e := range copyErrors {
		err = errors.Join(err, e)
	}
	return buffer.data, err
}
