package debugnodes

import (
	"context"
	"errors"
	"net"

	"github.com/asciimoth/gonnect"
)

type udpPacketConn interface {
	gonnect.UDPConn
}

func openUDPConn(ctx context.Context, netw NetworkCloser, cfg udpPingConfig) (udpPacketConn, error) {
	if cfg.socketMode == udpSocketDial {
		if cfg.remotePort == 0 {
			return nil, errors.New("remote port is required for dial socket mode")
		}
		return netw.DialUDP(ctx, cfg.network, cfg.localAddr(), cfg.remoteAddr())
	}
	return netw.ListenUDP(ctx, cfg.network, cfg.localAddr())
}

func readUDP(conn udpPacketConn, socketMode string, buf []byte) (int, net.Addr, error) {
	if socketMode == udpSocketDial {
		n, err := conn.Read(buf)
		return n, conn.RemoteAddr(), err
	}
	return conn.ReadFrom(buf)
}

func writeUDP(conn udpPacketConn, socketMode string, payload []byte, addr net.Addr) (int, error) {
	if socketMode == udpSocketDial {
		return conn.Write(payload)
	}
	if addr == nil {
		return 0, errors.New("remote address is required for unconnected socket mode")
	}
	return conn.WriteTo(payload, addr)
}

func udpAddr(network, address string) net.Addr {
	addr, err := net.ResolveUDPAddr(network, address)
	if err != nil {
		return &gonnect.NetAddr{Net: network, Addr: address}
	}
	return addr
}
