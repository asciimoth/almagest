package networknodes

import (
	"context"
	"errors"
	"time"

	"github.com/asciimoth/gonnect"
	netstackhelpers "github.com/asciimoth/gonnect-netstack/helpers"
	netstackspoofer "github.com/asciimoth/gonnect-netstack/spoofer"
	"github.com/asciimoth/gonnect/tun"
	"golang.org/x/time/rate"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

const (
	spooferForwardLogInterval = time.Second
	spooferForwardLogBurst    = 4
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
	ctx, cancel := context.WithCancel(n.Context())
	// Nil spawner is intentional here; see the Gonnect spawner note in helpers.go.
	stackTun := tun.Detach(n.tun, nil, n.pool)
	BindTunResource(n.W, n.ID, 0, stackTun)
	network := n.networkValue
	networkNode := n.networkNode
	report := func(protocol string, ep stack.TransportEndpointID, err error) {
		n.reportSpooferForwardError(ctx, protocol, ep, networkNode, network, err)
	}

	opts := &netstackspoofer.Opts{
		PrepareTCP: func(prepareCtx context.Context, ep stack.TransportEndpointID) (netstackspoofer.PreparedTCPHandler, error) {
			return prepareSpooferTCP(prepareCtx, n.WorkerSpawner, network, ep, func(err error) {
				report("tcp", ep, err)
			})
		},
		OnTCPError: func(ep stack.TransportEndpointID, err error) {
			report("tcp", ep, err)
		},
		OnUDPConn: func(c gonnect.PacketConn, ep stack.TransportEndpointID) {
			forwardSpooferUDP(ctx, n.WorkerSpawner, network, c, ep, func(err error) {
				report("udp", ep, err)
			})
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
	st, err := opts.LaunchContext(ctx)
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

func (n *spooferNode) reportSpooferForwardError(
	ctx context.Context,
	protocol string,
	ep stack.TransportEndpointID,
	networkNode uint64,
	network NetworkCloser,
	err error,
) {
	if n == nil || n.Logger == nil || err == nil {
		return
	}
	if ctx != nil && ctx.Err() != nil && errors.Is(err, context.Canceled) {
		return
	}
	n.forwardLogOnce.Do(func() {
		n.forwardLogLimiter = rate.NewLimiter(rate.Every(spooferForwardLogInterval), spooferForwardLogBurst)
	})
	if n.forwardLogLimiter == nil || !n.forwardLogLimiter.Allow() {
		return
	}
	target, ok := spooferEndpointAddress(ep)
	if !ok {
		target = "unknown"
	}
	n.Logger.Warnf(
		"Spoofer %s forwarding failed: target=%s family=%s networkNode=%d network=%T: %v",
		protocol, target, spooferEndpointFamily(ep), networkNode, network, err,
	)
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
