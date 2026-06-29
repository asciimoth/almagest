package almagest

import (
	"fmt"
	"os"

	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
)

type debugLogTun struct {
	tun.Tun
	logger pasta.Logger
}

func (t *debugLogTun) IsNative() bool { return t.Tun != nil && t.Tun.IsNative() }
func (t *debugLogTun) Close() error {
	if t.logger != nil {
		t.logger.Debug("tun.Close")
	}
	if t.Tun == nil {
		return nil
	}
	return t.Tun.Close()
}

func (t *debugLogTun) Wait() {
	waitTun(t.Tun)
}

func (t *debugLogTun) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	n, err := t.Tun.Read(bufs, sizes, offset)
	for i := 0; i < n && i < len(bufs) && i < len(sizes); i++ {
		start := min(offset, len(bufs[i]))
		end := min(start+sizes[i], len(bufs[i]))
		logPayload(t.logger, fmt.Sprintf("tun.Read packet=%d", i), bufs[i][start:end], err)
	}
	if n == 0 || err != nil {
		logResult(t.logger, "tun.Read", err)
	}
	return n, err
}

func (t *debugLogTun) Write(bufs [][]byte, offset int) (int, error) {
	n, err := t.Tun.Write(bufs, offset)
	for i := 0; i < n && i < len(bufs); i++ {
		start := min(offset, len(bufs[i]))
		logPayload(t.logger, fmt.Sprintf("tun.Write packet=%d", i), bufs[i][start:], err)
	}
	if n == 0 || err != nil {
		logResult(t.logger, "tun.Write", err)
	}
	return n, err
}

var (
	_ tun.Tun                      = (*debugLogTun)(nil)
	_ interface{ File() *os.File } = (*debugLogTun)(nil)
)
