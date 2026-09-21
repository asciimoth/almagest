package networknodes

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asciimoth/gonnect"
	netstackspoofer "github.com/asciimoth/gonnect-netstack/spoofer"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

const spooferUDPIdleTimeout = 2 * time.Minute

type spooferForwardErrorReporter func(error)

type preparedSpooferTCP struct {
	spawner  WorkerSpawner
	upstream net.Conn
	report   spooferForwardErrorReporter

	closeOnce sync.Once
	closeErr  error
}

var _ netstackspoofer.PreparedTCPHandler = (*preparedSpooferTCP)(nil)

func prepareSpooferTCP(
	ctx context.Context,
	spawner WorkerSpawner,
	network NetworkCloser,
	ep stack.TransportEndpointID,
	report spooferForwardErrorReporter,
) (netstackspoofer.PreparedTCPHandler, error) {
	target, ok := spooferEndpointAddress(ep)
	if !ok {
		return nil, errors.New("invalid intercepted TCP target")
	}
	if network == nil {
		return nil, errors.New("TCP output network is unavailable")
	}
	if spawner == nil {
		return nil, errors.New("TCP worker spawner is unavailable")
	}
	upstream, err := network.Dial(ctx, "tcp", target)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", target, err)
	}
	return &preparedSpooferTCP{spawner: spawner, upstream: upstream, report: report}, nil
}

func (p *preparedSpooferTCP) HandleTCP(c net.Conn) {
	if p == nil {
		if c != nil {
			_ = c.Close()
		}
		return
	}
	if c == nil {
		p.reportError(errors.New("accepted TCP connection is nil"))
		_ = p.Close()
		return
	}
	if p.upstream == nil || p.spawner == nil {
		_ = c.Close()
		p.reportError(errors.New("prepared TCP forwarder is incomplete"))
		_ = p.Close()
		return
	}

	closeBoth := sync.OnceFunc(func() {
		_ = c.Close()
		_ = p.Close()
	})
	if _, err := p.spawner.Spawn(func() {
		_, _ = io.Copy(p.upstream, c)
		closeBoth()
	}, "spoofer tcp upstream"); err != nil {
		p.reportError(fmt.Errorf("start TCP upstream worker: %w", err))
		closeBoth()
		return
	}
	if _, err := p.spawner.Spawn(func() {
		_, _ = io.Copy(c, p.upstream)
		closeBoth()
	}, "spoofer tcp downstream"); err != nil {
		p.reportError(fmt.Errorf("start TCP downstream worker: %w", err))
		closeBoth()
	}
}

func (p *preparedSpooferTCP) Close() error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		if p.upstream != nil {
			p.closeErr = p.upstream.Close()
		}
	})
	return p.closeErr
}

func (p *preparedSpooferTCP) reportError(err error) {
	if p != nil && p.report != nil && err != nil {
		p.report(err)
	}
}

func forwardSpooferUDP(
	ctx context.Context,
	spawner WorkerSpawner,
	network NetworkCloser,
	c gonnect.PacketConn,
	ep stack.TransportEndpointID,
	report spooferForwardErrorReporter,
) {
	forwardSpooferUDPWithIdleTimeout(ctx, spawner, network, c, ep, report, spooferUDPIdleTimeout)
}

func forwardSpooferUDPWithIdleTimeout(
	ctx context.Context,
	spawner WorkerSpawner,
	network NetworkCloser,
	c gonnect.PacketConn,
	ep stack.TransportEndpointID,
	report spooferForwardErrorReporter,
	idleTimeout time.Duration,
) {
	target, ok := spooferEndpointUDPAddr(ep)
	if !ok {
		reportSpooferForwardError(report, errors.New("invalid intercepted UDP target"))
		_ = c.Close()
		return
	}
	if network == nil {
		reportSpooferForwardError(report, errors.New("UDP output network is unavailable"))
		_ = c.Close()
		return
	}
	if spawner == nil {
		reportSpooferForwardError(report, errors.New("UDP worker spawner is unavailable"))
		_ = c.Close()
		return
	}
	udpNetwork, bindAddress := spooferUDPBindAddress(target)
	upstream, err := network.ListenPacket(ctx, udpNetwork, bindAddress)
	if err != nil {
		reportSpooferForwardError(report, fmt.Errorf("open packet socket for %s: %w", target, err))
		_ = c.Close()
		return
	}
	closeBoth := sync.OnceFunc(func() {
		_ = c.Close()
		_ = upstream.Close()
	})
	var deadlineMu sync.Mutex
	touch := func() error {
		if idleTimeout <= 0 {
			return nil
		}
		deadlineMu.Lock()
		defer deadlineMu.Unlock()
		deadline := time.Now().Add(idleTimeout)
		if err := c.SetReadDeadline(deadline); err != nil {
			return fmt.Errorf("set intercepted UDP idle deadline: %w", err)
		}
		if err := upstream.SetReadDeadline(deadline); err != nil {
			return fmt.Errorf("set upstream UDP idle deadline: %w", err)
		}
		return nil
	}
	if err := touch(); err != nil {
		reportSpooferForwardError(report, err)
		closeBoth()
		return
	}
	if _, err := spawner.Spawn(func() {
		defer closeBoth()
		buf := make([]byte, 64*1024)
		for {
			n, _, err := c.ReadFrom(buf)
			if err != nil {
				return
			}
			if err := touch(); err != nil {
				return
			}
			if _, err := upstream.WriteTo(buf[:n], target); err != nil {
				return
			}
		}
	}, "spoofer udp upstream"); err != nil {
		reportSpooferForwardError(report, fmt.Errorf("start UDP upstream worker: %w", err))
		closeBoth()
		return
	}
	if _, err := spawner.Spawn(func() {
		defer closeBoth()
		buf := make([]byte, 64*1024)
		for {
			n, source, err := upstream.ReadFrom(buf)
			if err != nil {
				return
			}
			if !spooferUDPReplyHostMatches(source, target) {
				continue
			}
			if err := touch(); err != nil {
				return
			}
			if _, err := c.Write(buf[:n]); err != nil {
				return
			}
		}
	}, "spoofer udp downstream"); err != nil {
		reportSpooferForwardError(report, fmt.Errorf("start UDP downstream worker: %w", err))
		closeBoth()
	}
}

func spooferUDPBindAddress(target *net.UDPAddr) (network, address string) {
	if target != nil && target.IP.To4() == nil {
		return "udp6", "[::]:0"
	}
	return "udp4", "0.0.0.0:0"
}

func spooferUDPReplyHostMatches(source net.Addr, target *net.UDPAddr) bool {
	if source == nil || target == nil {
		return false
	}
	if sourceUDP, ok := source.(*net.UDPAddr); ok {
		return sourceUDP.IP.Equal(target.IP)
	}
	host, _, err := net.SplitHostPort(source.String())
	if err != nil {
		return false
	}
	if zone := strings.LastIndexByte(host, '%'); zone >= 0 {
		host = host[:zone]
	}
	return net.ParseIP(host).Equal(target.IP)
}

func reportSpooferForwardError(report spooferForwardErrorReporter, err error) {
	if report != nil && err != nil {
		report(err)
	}
}

func spooferEndpointAddress(ep stack.TransportEndpointID) (string, bool) {
	addr, ok := spooferEndpointAddr(ep)
	if !ok {
		return "", false
	}
	return net.JoinHostPort(addr.String(), strconv.Itoa(int(ep.LocalPort))), true
}

func spooferEndpointUDPAddr(ep stack.TransportEndpointID) (*net.UDPAddr, bool) {
	addr, ok := spooferEndpointAddr(ep)
	if !ok {
		return nil, false
	}
	return &net.UDPAddr{IP: net.IP(addr.AsSlice()), Port: int(ep.LocalPort)}, true
}

func spooferEndpointAddr(ep stack.TransportEndpointID) (netip.Addr, bool) {
	addr, ok := netip.AddrFromSlice(ep.LocalAddress.AsSlice())
	if !ok || !addr.IsValid() || ep.LocalPort == 0 {
		return netip.Addr{}, false
	}
	return addr, true
}

func spooferEndpointFamily(ep stack.TransportEndpointID) string {
	addr, ok := spooferEndpointAddr(ep)
	if !ok {
		return "unknown"
	}
	if addr.Is4() {
		return "ipv4"
	}
	return "ipv6"
}
