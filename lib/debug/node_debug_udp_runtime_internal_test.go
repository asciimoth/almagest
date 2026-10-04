package debugnodes

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/gonnect"
)

type blockingUDPListenNetwork struct {
	gonnect.Network

	started  chan struct{}
	canceled chan struct{}
	once     sync.Once
}

func newBlockingUDPListenNetwork() *blockingUDPListenNetwork {
	return &blockingUDPListenNetwork{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
	}
}

func (n *blockingUDPListenNetwork) ListenUDP(ctx context.Context, _, _ string) (gonnect.UDPConn, error) {
	n.once.Do(func() { close(n.started) })
	<-ctx.Done()
	close(n.canceled)
	return nil, ctx.Err()
}

func (*blockingUDPListenNetwork) Close() error { return nil }

func TestRunUDPPingServerWorkerStopCancelsBlockedOpen(t *testing.T) {
	network := newBlockingUDPListenNetwork()
	stop := make(chan struct{})
	done := make(chan struct{})
	logger := logging.NewDebugLogFactory(8).EngineLogger()
	cfg := udpPingConfig{
		network:    "udp4",
		socketMode: udpSocketListen,
		localHost:  "127.0.0.1",
		localPort:  9099,
	}

	go func() {
		defer close(done)
		runUDPPingServerWorker(context.Background(), logger, nil, 0, stop, cfg, network)
	}()

	select {
	case <-network.started:
	case <-time.After(time.Second):
		t.Fatal("UDP server did not start opening the socket")
	}
	close(stop)
	select {
	case <-network.canceled:
	case <-time.After(time.Second):
		t.Fatal("UDP server stop did not cancel the blocked socket open")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("UDP server worker did not stop after socket open was canceled")
	}
}

func TestRunUDPPingServerWorkerParentCancelCancelsBlockedOpen(t *testing.T) {
	network := newBlockingUDPListenNetwork()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	logger := logging.NewDebugLogFactory(8).EngineLogger()

	go func() {
		defer close(done)
		runUDPPingServerWorker(ctx, logger, nil, 0, make(chan struct{}), udpPingConfig{
			network: "udp4", socketMode: udpSocketListen,
			localHost: "127.0.0.1", localPort: 9099,
		}, network)
	}()

	select {
	case <-network.started:
	case <-time.After(time.Second):
		t.Fatal("UDP server did not start opening the socket")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("UDP server worker did not stop after parent context cancellation")
	}
	select {
	case <-network.canceled:
	default:
		t.Fatal("UDP socket open did not observe parent context cancellation")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("parent context error = %v, want context canceled", ctx.Err())
	}
}
