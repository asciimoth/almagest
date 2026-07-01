package almagest

import (
	"context"
	"net"
	"time"

	"github.com/asciimoth/gonnect"
	netstackhelpers "github.com/asciimoth/gonnect-netstack/helpers"
	netstackspoofer "github.com/asciimoth/gonnect-netstack/spoofer"
	"github.com/asciimoth/gonnect/tun"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

type spooferRuntime struct {
	ctx      context.Context
	cancel   context.CancelFunc
	stack    *stack.Stack
	endpoint stack.LinkEndpoint
	tun      tun.Tun
}

func (n *spooferNode) rebuildStack() {
	n.stopStack()
	if n.tun == nil || n.networkValue == nil {
		_ = n.updateLabel()
		return
	}
	ctx := context.Background()
	if n.engine != nil && n.engine.ctx != nil {
		ctx = n.engine.ctx
	}
	ctx, cancel := context.WithCancel(ctx)
	// Nil spawner is intentional here; see the Gonnect spawner note in helpers.go.
	stackTun := tun.Detach(n.tun, nil, n.pool)
	BindTunResource(n.w, n.id, 0, stackTun)
	network := n.networkValue

	opts := &netstackspoofer.Opts{
		OnTCPConn: func(c net.Conn, ep stack.TransportEndpointID) {
			forwardSpooferTCP(ctx, n.workerSpawner, network, c, ep)
		},
		OnUDPConn: func(c gonnect.PacketConn, ep stack.TransportEndpointID) {
			forwardSpooferUDP(ctx, n.workerSpawner, network, c, ep)
		},
		TCPSendBufferSize:    n.config.tcpSendBufferSize,
		TCPReceiveBufferSize: n.config.tcpReceiveBufferSize,
		TTL:                  n.config.ttl,
		ICMPBurst:            n.config.icmpBurst,
		ICMPLimit:            n.config.icmpLimit,
		CongestionControlAlg: n.config.congestionControlAlg,
		DisableNagle:         n.config.disableNagle,
		DisableTCPModRecBuff: n.config.disableTCPModRecBuff,
		TCPKeepAlive:         n.config.tcpKeepAlive,
		TCPKeepAliveIdle:     time.Duration(n.config.tcpKeepAliveIdle) * time.Second,
		TCPKeepaliveInterval: time.Duration(n.config.tcpKeepAliveInterval) * time.Second,
		TCPKeepaliveCount:    n.config.tcpKeepAliveCount,
		TCPForwardWnd:        n.config.tcpForwardWnd,
		TCPForwardAttempts:   n.config.tcpForwardAttempts,
		NetStackOpts: &netstackhelpers.Opts{
			MTU:         n.config.mtu,
			DisableSACK: n.config.disableSACK,
		},
	}
	opts.WithTunEndpoint(stackTun, n.config.queueLength)
	st, err := opts.Launch()
	if err != nil {
		cancel()
		if opts.Endpoint != nil {
			closeLinkEndpoint(opts.Endpoint)
			waitLinkEndpoint(opts.Endpoint)
		} else {
			_ = stackTun.Close()
		}
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return
	}
	n.formError = ""
	n.runtime = &spooferRuntime{
		ctx:      ctx,
		cancel:   cancel,
		stack:    st,
		endpoint: opts.Endpoint,
		tun:      stackTun,
	}
	_ = n.updateLabel()
}

func (n *spooferNode) stopStack() {
	rt := n.runtime
	if rt == nil {
		return
	}
	n.runtime = nil
	rt.cancel()
	if rt.stack != nil {
		rt.stack.Close()
	}
	if rt.endpoint != nil {
		closeLinkEndpoint(rt.endpoint)
		waitLinkEndpoint(rt.endpoint)
	} else if rt.tun != nil {
		_ = rt.tun.Close()
	}
}

func closeLinkEndpoint(endpoint stack.LinkEndpoint) {
	if closer, ok := endpoint.(interface{ Close() }); ok {
		closer.Close()
	}
}

func waitLinkEndpoint(endpoint stack.LinkEndpoint) {
	if waiter, ok := endpoint.(interface{ Wait() }); ok {
		waiter.Wait()
	}
}
