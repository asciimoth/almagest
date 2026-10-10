package network

import (
	"context"
	"errors"
	"net/netip"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/gonnect"
	gdns "github.com/asciimoth/gonnect/dns"
)

func numeric(ep netip.AddrPort) string {
	if !ep.IsValid() {
		return ""
	}
	return ep.String()
}

func (s *SystemBackend) OpenSocket(ctx context.Context, o protocol.SocketOptions) (OwnedTun, error) {
	if err := validateSocket(o); err != nil {
		return nil, &NoEffectsError{err}
	}
	if o.Kind == protocol.SocketResolver {
		provider := s.system.OutDNS()
		if provider == nil {
			return nil, &NoEffectsError{protocol.ErrCapabilityUnavailable}
		}
		return newResolverSocket(provider), nil
	}
	network := s.system.OutNet()
	if o.Purpose == protocol.NetworkLocal {
		network = s.system.LocalNet()
	}
	if network == nil {
		return nil, &NoEffectsError{protocol.ErrCapabilityUnavailable}
	}
	socket := &nativeSocket{network: network}
	var err error
	switch o.Kind {
	case protocol.SocketDialTCP:
		socket.tcp, err = network.DialTCP(ctx, o.Network, numeric(o.Local), numeric(o.Remote))
	case protocol.SocketListenTCP:
		socket.listener, err = network.ListenTCP(ctx, o.Network, numeric(o.Local))
	case protocol.SocketDialUDP:
		socket.udp, err = network.DialUDP(ctx, o.Network, numeric(o.Local), numeric(o.Remote))
	case protocol.SocketListenUDP, protocol.SocketPacketUDP:
		socket.udp, err = network.ListenUDPConfig(ctx, nil, o.Network, numeric(o.Local))
	case protocol.SocketMulticastUDP:
		socket.multicast, err = network.ListenMulticastUDP(ctx, o.Network, numeric(o.Local), gonnect.MulticastOptions{ReuseAddr: o.Multicast.ReuseAddr, ReusePort: o.Multicast.ReusePort, RecvAnyIf: o.Multicast.RecvAnyIf, ControlFlags: gonnect.ControlFlags(o.Multicast.ControlFlags)})
	default:
		return nil, &NoEffectsError{protocol.ErrUnsupportedOperation}
	}
	if socket.tcp == nil && socket.udp == nil && socket.listener == nil && socket.multicast == nil {
		if err == nil {
			err = protocol.ErrCapabilityUnavailable
		}
		return nil, &NoEffectsError{nativeError(err)}
	}
	if err != nil {
		return socket, nativeError(err)
	}
	if socket.tcp != nil {
		if err = socket.tcp.SetNoDelay(o.NoDelay); err != nil {
			return socket, err
		}
		if o.KeepAlive > 0 {
			if err = socket.tcp.SetKeepAlive(true); err == nil {
				err = socket.tcp.SetKeepAlivePeriod(o.KeepAlive)
			}
			if err != nil {
				return socket, err
			}
		}
	}
	for _, setting := range []struct {
		n    uint32
		read bool
	}{{o.ReadBuffer, true}, {o.WriteBuffer, false}} {
		if setting.n == 0 {
			continue
		}
		var target any = socket.tcp
		if socket.udp != nil {
			target = socket.udp
		}
		if socket.multicast != nil {
			target = socket.multicast
		}
		if setting.read {
			if v, ok := target.(interface{ SetReadBuffer(int) error }); ok {
				err = v.SetReadBuffer(int(setting.n))
			} else {
				err = protocol.ErrCapabilityUnsupported
			}
		} else {
			if v, ok := target.(interface{ SetWriteBuffer(int) error }); ok {
				err = v.SetWriteBuffer(int(setting.n))
			} else {
				err = protocol.ErrCapabilityUnsupported
			}
		}
		if err != nil {
			return socket, err
		}
	}
	return socket, nil
}

func (s *SystemBackend) ListInterfaces(ctx context.Context) (protocol.InterfaceSnapshot, error) {
	return s.ListNetworkInterfaces(ctx, protocol.NetworkOutbound)
}
func (s *SystemBackend) ListNetworkInterfaces(ctx context.Context, purpose protocol.NetworkPurpose) (protocol.InterfaceSnapshot, error) {
	var result protocol.InterfaceSnapshot
	if err := ctx.Err(); err != nil {
		return result, err
	}
	network := s.system.OutNet()
	if purpose == protocol.NetworkLocal {
		network = s.system.LocalNet()
	}
	if network == nil {
		return result, protocol.ErrCapabilityUnavailable
	}
	interfaces, err := network.Interfaces()
	if err != nil {
		return result, nativeError(err)
	}
	if len(interfaces) > protocol.MaxInterfaceSnapshotItems {
		return result, protocol.ErrLimitExceeded
	}
	count := 0
	for _, iface := range interfaces {
		if iface.Index() < 0 || iface.MTU() < -1 || len(iface.Name()) > 256 || len(iface.ID()) > 256 || len(iface.HardwareAddr()) > 32 {
			return result, protocol.ErrInvalidContract
		}
		item := protocol.Interface{ID: iface.ID(), HardwareAddress: append([]byte(nil), iface.HardwareAddr()...), Index: uint32(iface.Index()), Name: iface.Name(), MTU: int64(iface.MTU()), Flags: uint32(iface.Flags())}
		addresses, e := iface.Addrs()
		if e != nil {
			return result, nativeError(e)
		}
		multicast, e := iface.MulticastAddrs()
		if e != nil {
			return result, nativeError(e)
		}
		count += len(addresses) + len(multicast)
		if count > protocol.MaxAddressOrRouteSetItems {
			return result, protocol.ErrLimitExceeded
		}
		for _, addr := range addresses {
			p, e := netip.ParsePrefix(addr.String())
			if e != nil {
				return result, protocol.ErrCapabilityUnsupported
			}
			item.Addresses = append(item.Addresses, p)
		}
		for _, addr := range multicast {
			p, e := netip.ParseAddr(addr.String())
			if e != nil {
				return result, protocol.ErrCapabilityUnsupported
			}
			item.Multicast = append(item.Multicast, p)
		}
		result.Interfaces = append(result.Interfaces, item)
	}
	return result, nil
}

// ResolverSnapshotSource is a trusted bootstrap input. Current sysnet DNS
// providers do not all expose their upstream list. An absent source is an
// explicit capability gap; reading R's host resolver files is not a fallback.
type ResolverSnapshotSource func(context.Context) (protocol.ResolverSnapshot, error)

func (s *SystemBackend) SetResolverSnapshotSource(source ResolverSnapshotSource) {
	s.resolverSource = source
}
func (s *SystemBackend) ResolverSnapshot(ctx context.Context) (protocol.ResolverSnapshot, error) {
	if s.resolverSource == nil {
		return protocol.ResolverSnapshot{}, protocol.ErrCapabilityUnsupported
	}
	result, err := s.resolverSource(ctx)
	if err != nil {
		return protocol.ResolverSnapshot{}, nativeError(err)
	}
	if result.Revision == 0 || len(result.Servers) > 128 || len(result.Search) > 128 {
		return protocol.ResolverSnapshot{}, protocol.ErrLimitExceeded
	}
	for _, ep := range result.Servers {
		if !ep.IsValid() || ep.Port() == 0 {
			return protocol.ResolverSnapshot{}, protocol.ErrInvalidContract
		}
	}
	for _, name := range result.Search {
		if len(name) > 253 {
			return protocol.ResolverSnapshot{}, protocol.ErrLimitExceeded
		}
	}
	result.Servers = append([]netip.AddrPort(nil), result.Servers...)
	result.Search = append([]string(nil), result.Search...)
	return result, nil
}

func socketError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, gdns.ErrNoUpstream):
		return "dns_no_upstream"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, gonnect.ErrUnsupported), errors.Is(err, protocol.ErrCapabilityUnsupported):
		return "unsupported"
	}
	return dataError(err)
}
