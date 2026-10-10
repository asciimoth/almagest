package network

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/tun"
)

// SocketBackend preserves the selected backend's outbound and LocalNet scope.
// It must return only objects it owns, including on partial failure.
type SocketBackend interface {
	OpenSocket(context.Context, protocol.SocketOptions) (OwnedTun, error)
	ListInterfaces(context.Context) (protocol.InterfaceSnapshot, error)
	ResolverSnapshot(context.Context) (protocol.ResolverSnapshot, error)
}

func validateSocket(o protocol.SocketOptions) error {
	if o.Kind == protocol.SocketResolver {
		if o != (protocol.SocketOptions{Purpose: protocol.NetworkOutbound, Kind: protocol.SocketResolver}) {
			return protocol.ErrInvalidContract
		}
		return nil
	}
	if o.Purpose != protocol.NetworkOutbound && o.Purpose != protocol.NetworkLocal {
		return protocol.ErrInvalidContract
	}
	tcp := o.Kind == protocol.SocketDialTCP || o.Kind == protocol.SocketListenTCP
	if tcp {
		if o.Network != "tcp" && o.Network != "tcp4" && o.Network != "tcp6" {
			return protocol.ErrInvalidContract
		}
	} else {
		switch o.Kind {
		case protocol.SocketDialUDP, protocol.SocketListenUDP, protocol.SocketPacketUDP, protocol.SocketMulticastUDP:
		default:
			return protocol.ErrUnsupportedOperation
		}
		if o.Network != "udp" && o.Network != "udp4" && o.Network != "udp6" {
			return protocol.ErrInvalidContract
		}
	}
	dial := o.Kind == protocol.SocketDialTCP || o.Kind == protocol.SocketDialUDP
	if dial && (!o.Remote.IsValid() || o.Remote.Port() == 0) || !dial && o.Remote.IsValid() {
		return protocol.ErrInvalidContract
	}
	for _, ep := range []netip.AddrPort{o.Local, o.Remote} {
		if !ep.IsValid() {
			continue
		}
		if strings.HasSuffix(o.Network, "4") && !ep.Addr().Is4() || strings.HasSuffix(o.Network, "6") && !ep.Addr().Is6() {
			return protocol.ErrInvalidContract
		}
		if len(ep.Addr().Zone()) > 64 {
			return protocol.ErrLimitExceeded
		}
	}
	if o.ReadBuffer > protocol.MaxSessionData || o.WriteBuffer > protocol.MaxSessionData || o.KeepAlive < 0 || o.KeepAlive > 24*time.Hour {
		return protocol.ErrLimitExceeded
	}
	if !o.Deadline.IsZero() {
		return protocol.ErrInvalidContract
	} // clocks are not shared
	if o.Multicast.ControlFlags > uint32(gonnect.ControlDst|gonnect.ControlInterface) {
		return protocol.ErrInvalidContract
	}
	if o.Kind != protocol.SocketMulticastUDP && o.Multicast != (protocol.MulticastOptions{}) {
		return protocol.ErrInvalidContract
	}
	return nil
}

func (b *Broker) OpenSocket(ctx context.Context, req protocol.OpenSocketRequest) (protocol.ResourceResult, error) {
	input := req
	input.Meta.RequestID = 0
	return b.mutate(ctx, req.Meta, protocol.OperationOpenSocket, input, func(ctx context.Context) (protocol.ResourceResult, error) {
		if err := validateSocket(req.Options); err != nil {
			return rejected(req.Meta.OperationID, err), err
		}
		backend, ok := b.backend.(SocketBackend)
		if !ok {
			return rejected(req.Meta.OperationID, protocol.ErrCapabilityUnsupported), protocol.ErrCapabilityUnsupported
		}
		kind := protocol.ResourceSocket
		if req.Options.Kind == protocol.SocketListenTCP {
			kind = protocol.ResourceListener
		}
		b.mu.Lock()
		err := b.socketCapacity(kind)
		b.mu.Unlock()
		if err != nil {
			return rejected(req.Meta.OperationID, err), err
		}
		id, err := protocol.NewResourceID()
		if err != nil {
			return rejected(req.Meta.OperationID, err), err
		}
		r := &resource{ref: protocol.ResourceRef{ID: id, SessionID: b.session.ID, Type: kind, Generation: 1, GraphEpoch: req.Meta.GraphEpoch}}
		b.mu.Lock()
		if b.resources[id] != nil {
			b.mu.Unlock()
			return rejected(req.Meta.OperationID, protocol.ErrInvalidContract), protocol.ErrInvalidContract
		}
		b.resources[id] = r
		b.mu.Unlock()
		native, err := backend.OpenSocket(ctx, req.Options)
		if resolver, ok := native.(*resolverSocket); ok {
			b.mu.Lock()
			if b.data != nil && b.dataReady {
				resolver.reserve = b.data.HoldOutbound
			} else {
				err = protocol.ErrCapabilityUnavailable
			}
			b.mu.Unlock()
		}
		b.mu.Lock()
		r.native = native
		b.mu.Unlock()
		if native == nil {
			var noEffects *NoEffectsError
			if errors.As(err, &noEffects) {
				b.mu.Lock()
				r.state.Closed = true
				b.mu.Unlock()
				return b.result(r, protocol.OutcomeRejected), err
			}
			return b.failed(r, req.Meta.OperationID, err)
		}
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			var observed protocol.ResourceState
			observed, err = native.State(ctx)
			b.mu.Lock()
			r.state = observed
			b.mu.Unlock()
		}
		b.mu.Lock()
		stale := b.closed || b.epoch != req.Meta.GraphEpoch
		b.mu.Unlock()
		if stale && err == nil {
			err = context.Canceled
		}
		if err != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), b.timeout)
			defer cancel()
			if closeErr := b.closeNative(cleanup, r); closeErr != nil {
				return b.failed(r, req.Meta.OperationID, errors.Join(err, closeErr))
			}
			return b.result(r, protocol.OutcomeCompensated), err
		}
		b.mu.Lock()
		r.state.Up = true
		r.state.Revision = 1
		b.mu.Unlock()
		return b.result(r, protocol.OutcomeCommitted), nil
	})
}

// Caller holds mu. Tombstones are bounded by retained creation receipts.
func (b *Broker) socketCapacity(kind protocol.ResourceType) error {
	if b.recovery {
		return protocol.ErrRecoveryRequired
	}
	total, sockets, listeners := 0, 0, 0
	for _, r := range b.resources {
		if !r.state.Closed {
			total++
			if r.ref.Type == protocol.ResourceSocket {
				sockets++
			}
			if r.ref.Type == protocol.ResourceListener {
				listeners++
			}
		}
	}
	if total >= int(b.policy.Limits.MaxLiveResources) || len(b.resources) >= protocol.MaxOperationReceipts {
		return protocol.ErrLimitExceeded
	}
	if p := b.policy.NetworkRestrictions; p != nil {
		if kind == protocol.ResourceSocket && p.MaxSockets > 0 && sockets >= int(p.MaxSockets) || kind == protocol.ResourceListener && p.MaxListeners > 0 && listeners >= int(p.MaxListeners) {
			return protocol.ErrLimitExceeded
		}
	}
	return nil
}

func (b *Broker) ListInterfaces(ctx context.Context, req protocol.ReadRequest) (protocol.InterfaceSnapshot, error) {
	return b.ListNetworkInterfaces(ctx, protocol.InterfaceRequest{Meta: req.Meta, Purpose: protocol.NetworkOutbound})
}
func (b *Broker) ListNetworkInterfaces(ctx context.Context, req protocol.InterfaceRequest) (protocol.InterfaceSnapshot, error) {
	return read(ctx, b, req.Meta, protocol.OperationListInterfaces, func(ctx context.Context) (protocol.InterfaceSnapshot, error) {
		if req.Purpose == "" {
			req.Purpose = protocol.NetworkOutbound
		}
		if req.Purpose != protocol.NetworkOutbound && req.Purpose != protocol.NetworkLocal {
			return protocol.InterfaceSnapshot{}, protocol.ErrInvalidContract
		}
		var result protocol.InterfaceSnapshot
		var err error
		if backend, ok := b.backend.(interface {
			ListNetworkInterfaces(context.Context, protocol.NetworkPurpose) (protocol.InterfaceSnapshot, error)
		}); ok {
			result, err = backend.ListNetworkInterfaces(ctx, req.Purpose)
		} else if backend, ok := b.backend.(SocketBackend); ok && req.Purpose == protocol.NetworkOutbound {
			result, err = backend.ListInterfaces(ctx)
		} else {
			return result, protocol.ErrCapabilityUnsupported
		}
		if err == nil {
			err = b.boundSnapshot(result)
		}
		return result, err
	})
}
func (b *Broker) ResolverSnapshot(ctx context.Context, req protocol.ReadRequest) (protocol.ResolverSnapshot, error) {
	return read(ctx, b, req.Meta, protocol.OperationResolverSnapshot, func(ctx context.Context) (protocol.ResolverSnapshot, error) {
		backend, ok := b.backend.(SocketBackend)
		if !ok {
			return protocol.ResolverSnapshot{}, protocol.ErrCapabilityUnsupported
		}
		result, err := backend.ResolverSnapshot(ctx)
		if err == nil {
			err = b.boundSnapshot(result)
		}
		return result, err
	})
}

func endpoint(a net.Addr) netip.AddrPort {
	switch v := a.(type) {
	case *net.TCPAddr:
		return v.AddrPort()
	case *net.UDPAddr:
		return v.AddrPort()
	case *net.IPAddr:
		addr, ok := netip.AddrFromSlice(v.IP)
		if !ok {
			return netip.AddrPort{}
		}
		if v.Zone != "" {
			addr = addr.WithZone(v.Zone)
		}
		return netip.AddrPortFrom(addr, 0)
	}
	if a == nil {
		return netip.AddrPort{}
	}
	ep, _ := netip.ParseAddrPort(a.String())
	return ep
}

// nativeSocket embeds only broker-owned objects. No object or descriptor from
// this type is returned to the runtime.
type nativeSocket struct {
	tcp       gonnect.TCPConn
	udp       gonnect.UDPConn
	multicast gonnect.MulticastPacketConn
	listener  gonnect.TCPListener
	network   gonnect.Network
}

func (s *nativeSocket) Verify(ctx context.Context) error { return ctx.Err() }
func (s *nativeSocket) Events() <-chan tun.Event         { return nil }
func (s *nativeSocket) Capabilities(context.Context) (protocol.CapabilitySnapshot, error) {
	return protocol.CapabilitySnapshot{}, nil
}
func (s *nativeSocket) Update(context.Context, protocol.UpdateTunRequest) error {
	return protocol.ErrResourceType
}
func (s *nativeSocket) State(context.Context) (protocol.ResourceState, error) {
	state := protocol.ResourceState{Up: true, Revision: 1}
	switch {
	case s.tcp != nil:
		state.Local = endpoint(s.tcp.LocalAddr())
		state.Remote = endpoint(s.tcp.RemoteAddr())
	case s.udp != nil:
		state.Local = endpoint(s.udp.LocalAddr())
		state.Remote = endpoint(s.udp.RemoteAddr())
	case s.listener != nil:
		state.Local = endpoint(s.listener.Addr())
	case s.multicast != nil:
		state.Local = endpoint(s.multicast.LocalAddr())
	}
	return state, nil
}
func (s *nativeSocket) Close(context.Context) error {
	switch {
	case s.tcp != nil:
		return s.tcp.Close()
	case s.udp != nil:
		return s.udp.Close()
	case s.listener != nil:
		return s.listener.Close()
	case s.multicast != nil:
		return s.multicast.Close()
	}
	return protocol.ErrInvalidContract
}
