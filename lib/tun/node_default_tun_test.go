package tunnodes

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/asciimoth/gonnect/dns"
	gonnecttun "github.com/asciimoth/gonnect/tun"
)

func TestDefaultTunOnStopClosesActiveDefaultTun(t *testing.T) {
	dtun := newDefaultTunStopRecorder(1500)
	node := &defaultTunNode{
		dtun:         dtun,
		hasEffective: true,
	}

	node.OnStop()

	if got := dtun.closeCount(); got != 1 {
		t.Fatalf("DefaultTun close count after OnStop = %d, want 1", got)
	}
	if node.dtun != nil {
		t.Fatal("DefaultTun node kept dtun after OnStop")
	}
	if node.hasEffective {
		t.Fatal("DefaultTun node kept effective config after OnStop")
	}
}

type defaultTunStopRecorder struct {
	gonnecttun.Tun
	peer gonnecttun.Tun

	mtu          int
	closeCounter atomic.Int32
}

func newDefaultTunStopRecorder(mtu int) *defaultTunStopRecorder {
	if mtu <= 0 {
		mtu = 1500
	}
	base, peer := gonnecttun.Pipe(1, mtu, 0, 0)
	return &defaultTunStopRecorder{Tun: base, peer: peer, mtu: mtu}
}

func (t *defaultTunStopRecorder) SetDns(dns.Interface) {}

func (t *defaultTunStopRecorder) MTU() (int, error) {
	return t.mtu, nil
}

func (t *defaultTunStopRecorder) Close() error {
	t.closeCounter.Add(1)
	return errors.Join(t.Tun.Close(), t.peer.Close())
}

func (t *defaultTunStopRecorder) closeCount() int {
	return int(t.closeCounter.Load())
}
