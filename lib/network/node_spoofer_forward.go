package networknodes

import (
	"context"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"

	"github.com/asciimoth/gonnect"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

func forwardSpooferTCP(ctx context.Context, spawner WorkerSpawner, network NetworkCloser, c net.Conn, ep stack.TransportEndpointID) {
	target, ok := spooferEndpointAddress(ep)
	if !ok || network == nil {
		_ = c.Close()
		return
	}
	if spawner == nil {
		_ = c.Close()
		return
	}
	upstream, err := network.Dial(ctx, "tcp", target)
	if err != nil {
		_ = c.Close()
		return
	}
	closeBoth := sync.OnceFunc(func() {
		_ = c.Close()
		_ = upstream.Close()
	})
	if _, err := spawner.Spawn(func() {
		_, _ = io.Copy(upstream, c)
		closeBoth()
	}, "spoofer tcp upstream"); err != nil {
		closeBoth()
		return
	}
	if _, err := spawner.Spawn(func() {
		_, _ = io.Copy(c, upstream)
		closeBoth()
	}, "spoofer tcp downstream"); err != nil {
		closeBoth()
		return
	}
}

func forwardSpooferUDP(ctx context.Context, spawner WorkerSpawner, network NetworkCloser, c gonnect.PacketConn, ep stack.TransportEndpointID) {
	target, ok := spooferEndpointUDPAddr(ep)
	if !ok || network == nil {
		_ = c.Close()
		return
	}
	if spawner == nil {
		_ = c.Close()
		return
	}
	upstream, err := network.PacketDial(ctx, "udp", target.String())
	if err != nil {
		_ = c.Close()
		return
	}
	closeBoth := sync.OnceFunc(func() {
		_ = c.Close()
		_ = upstream.Close()
	})
	if _, err := spawner.Spawn(func() {
		defer closeBoth()
		buf := make([]byte, 64*1024)
		for {
			n, _, err := c.ReadFrom(buf)
			if err != nil {
				return
			}
			if _, err := upstream.Write(buf[:n]); err != nil {
				return
			}
		}
	}, "spoofer udp upstream"); err != nil {
		closeBoth()
		return
	}
	if _, err := spawner.Spawn(func() {
		defer closeBoth()
		buf := make([]byte, 64*1024)
		for {
			n, err := upstream.Read(buf)
			if err != nil {
				return
			}
			if _, err := c.Write(buf[:n]); err != nil {
				return
			}
		}
	}, "spoofer udp downstream"); err != nil {
		closeBoth()
		return
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
